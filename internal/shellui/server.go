package shellui

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/patch/agora-os/internal/appcatalog"
	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
	"github.com/patch/agora-os/internal/webbus"
)

const (
	defaultAdminLogPath     = "/var/log/agent-os/admin-agent.log"
	defaultDecisionLogPath  = "/var/log/agent-os/admin-human-decisions.jsonl"
	defaultShellAuditWSPath = "/api/shell/audit/ws"
	shellSessionTokenTTL    = time.Hour
)

const DefaultShellConfigDir = "/etc/agora-shell"

type Config struct {
	Secret           []byte
	Now              func() time.Time
	AllowedOrigins   map[string]struct{}
	Assets           fs.FS
	DevDir           string
	BusSocket        string
	IsolationSocket  string
	CompositorSocket string
	AuditSocket      string
	AdminLogPath     string
	DecisionLogPath  string
	ShellConfigDir   string
}

type Server struct {
	secret           []byte
	now              func() time.Time
	allowedOrigins   map[string]struct{}
	assets           http.Handler
	busSocket        string
	isolationSocket  string
	compositorSocket string
	auditSocket      string
	adminLogPath     string
	decisionLogPath  string
	shellConfigDir   string
	upgrader         websocket.Upgrader
}

type State struct {
	Agents             []schema.AgentInfo                `json:"agents,omitempty"`
	Surfaces           []schema.CompositorTrackedSurface `json:"surfaces,omitempty"`
	PendingEscalations []schema.AdminEscalationEvent     `json:"pending_escalations,omitempty"`
}

type sessionTokenResponse struct {
	Token     string `json:"token"`
	Role      string `json:"role"`
	ExpiresAt int64  `json:"expires_at"`
	Use       string `json:"use"`
}

type grantRequest struct {
	SurfaceID string                          `json:"surface_id"`
	AgentUID  uint32                          `json:"agent_uid"`
	Actions   []schema.CompositorAccessAction `json:"actions,omitempty"`
}

type surfaceActionHTTPError struct {
	ErrorClass string                       `json:"error_class,omitempty"`
	Result     schema.SurfaceActionResponse `json:"result"`
}

type escalationDecisionRequest struct {
	ID          string                    `json:"id"`
	Decision    schema.EscalationDecision `json:"decision"`
	Constraints []string                  `json:"constraints,omitempty"`
	Notes       string                    `json:"notes,omitempty"`
}

type loggedEscalation struct {
	Timestamp time.Time                 `json:"timestamp"`
	Request   schema.EscalationRequest  `json:"request"`
	Response  schema.EscalationResponse `json:"response"`
}

func New(cfg Config) *Server {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	allowedOrigins := cfg.AllowedOrigins
	if allowedOrigins == nil {
		allowedOrigins = make(map[string]struct{})
	}

	var assets http.Handler = http.NotFoundHandler()
	devDir := strings.TrimSpace(cfg.DevDir)
	if devDir != "" {
		assets = http.FileServer(http.Dir(devDir))
	} else if cfg.Assets != nil {
		assets = http.FileServerFS(cfg.Assets)
	}

	s := &Server{
		secret:           cfg.Secret,
		now:              now,
		allowedOrigins:   allowedOrigins,
		assets:           assets,
		busSocket:        defaultString(cfg.BusSocket, schema.BusSocket),
		isolationSocket:  defaultString(cfg.IsolationSocket, schema.IsolationSocket),
		compositorSocket: defaultString(cfg.CompositorSocket, schema.CompositorControlSocket),
		auditSocket:      defaultString(cfg.AuditSocket, schema.AuditSocket),
		adminLogPath:     defaultString(cfg.AdminLogPath, defaultAdminLogPath),
		decisionLogPath:  defaultString(cfg.DecisionLogPath, defaultDecisionLogPath),
		shellConfigDir:   defaultString(cfg.ShellConfigDir, defaultShellConfigDir()),
	}
	s.upgrader = websocket.Upgrader{CheckOrigin: s.checkOrigin}
	return s
}

func (s *Server) StaticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqPath := r.URL.Path
		if !strings.HasPrefix(reqPath, "/") {
			reqPath = "/" + reqPath
		}
		if strings.HasPrefix(path.Clean(reqPath), "/dist") {
			servePath := strings.TrimPrefix(reqPath, "/dist")
			if servePath == "" {
				servePath = "/"
			}
			clone := r.Clone(r.Context())
			clone.URL.Path = servePath
			s.assets.ServeHTTP(w, clone)
			return
		}
		s.assets.ServeHTTP(w, r)
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cleanPath := path.Clean(strings.TrimPrefix(r.URL.Path, "/api/shell"))
	if strings.HasPrefix(cleanPath, "/widget-proxy/") {
		s.handleWidgetProxy(w, r, strings.TrimPrefix(cleanPath, "/widget-proxy/"))
		return
	}
	switch cleanPath {
	case "/state":
		s.handleState(w, r)
	case "/session-token":
		s.handleSessionToken(w, r)
	case "/grants":
		s.handleGrant(w, r)
	case "/apps":
		s.handleApps(w, r)
	case "/app/launch":
		s.handleAppLaunch(w, r)
	case "/surface/focus":
		s.handleSurfaceFocus(w, r)
	case "/surface/move":
		s.handleSurfaceMove(w, r)
	case "/surface/resize":
		s.handleSurfaceResize(w, r)
	case "/surface/tile":
		s.handleSurfaceTile(w, r)
	case "/surface/always-on-top":
		s.handleSurfaceAlwaysOnTop(w, r)
	case "/surface/fullscreen":
		s.handleSurfaceFullscreen(w, r)
	case "/surface/maximize":
		s.handleSurfaceMaximize(w, r)
	case "/surface/debug-raise":
		s.handleSurfaceDebugRaise(w, r)
	case "/surface/close":
		s.handleSurfaceClose(w, r)
	case "/escalations/decide":
		s.handleEscalationDecision(w, r)
	case "/audit/ws":
		s.handleAuditWS(w, r)
	case "/layout.json":
		s.handleLayoutJSON(w, r)
	case "/theme.css":
		s.handleThemeCSS(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, _, err := s.authenticateHuman(r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	agents, err := s.listAgents()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	surfaces, err := s.listSurfaces()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pending, err := s.pendingEscalations()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, State{
		Agents:             agents,
		Surfaces:           surfaces,
		PendingEscalations: pending,
	})
}

func (s *Server) handleSessionToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRemoteAddr(r.RemoteAddr) {
		http.Error(w, "shell session tokens are available only to loopback clients", http.StatusForbidden)
		return
	}
	expiresAt := s.now().Add(shellSessionTokenTTL).Unix()
	token, err := webbus.MintToken(s.secret, webbus.Claims{Role: webbus.RoleHuman, UID: 0, Exp: expiresAt})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, sessionTokenResponse{
		Token:     token,
		Role:      string(webbus.RoleHuman),
		ExpiresAt: expiresAt,
		Use:       "websocket-subprotocol",
	})
}

func (s *Server) appCatalogPath() string {
	return filepath.Join(s.shellConfigDir, "app-catalog.json")
}

func (s *Server) loadAppCatalog() (appcatalog.Catalog, error) {
	return appcatalog.Load(s.appCatalogPath())
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, _, err := s.authenticateHuman(r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	catalog, err := s.loadAppCatalog()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	entries := make([]schema.AppCatalogEntry, 0, len(catalog.Entries))
	for _, entry := range catalog.PublicEntries() {
		entries = append(entries, schema.AppCatalogEntry{
			ID: entry.ID, Label: entry.Label, Description: entry.Description, Icon: entry.Icon,
			Tags: entry.Tags, State: entry.State, Reason: entry.Reason,
		})
	}
	writeJSON(w, schema.AppCatalogListResponse{Entries: entries})
}

func (s *Server) handleAppLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _, err := s.authenticateHuman(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req schema.AppLaunchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode app launch request: %v", err), http.StatusBadRequest)
		return
	}
	catalog, err := s.loadAppCatalog()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	entry, ok := catalog.Find(req.CatalogID)
	if !ok {
		result := appLaunchDenied(req.CatalogID, uint32(identity.UID), "No app catalog entry: "+req.CatalogID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error_class": "app_not_found", "result": result})
		return
	}
	if !entry.Enabled {
		reason := entry.Reason
		if reason == "" {
			reason = "app catalog entry is disabled"
		}
		result := appLaunchDenied(entry.ID, uint32(identity.UID), reason)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error_class": "app_disabled", "result": result})
		return
	}
	launchReq := schema.LaunchAppRequest{
		SessionID: strings.TrimSpace(req.SessionID),
		Command:   entry.Command, Cwd: entry.Cwd, Env: entry.Env,
		AuditCorrelationID: strings.TrimSpace(req.TurnID),
		ExpectedAppID:      entry.ExpectedAppID, ExpectedTitle: entry.ExpectedTitle,
		Role: entry.Role, Output: entry.Output, WaitSurface: true, WaitTimeoutMs: entry.WaitTimeoutMs,
	}
	if entry.WaitSurface != nil {
		launchReq.WaitSurface = *entry.WaitSurface
	}
	if launchReq.WaitTimeoutMs <= 0 {
		launchReq.WaitTimeoutMs = 10000
	}
	body, resp, err := callResponse(s.compositorSocket, schema.MethodLaunchApp, launchReq)
	if err != nil {
		result := appLaunchDenied(entry.ID, uint32(identity.UID), err.Error())
		status := http.StatusBadGateway
		errorClass := respErrorClass(resp)
		if errorClass == schema.ErrorAppNotReady {
			status = http.StatusGatewayTimeout
		}
		if resp != nil && resp.ErrorMessage != "" {
			result.Reason = resp.ErrorMessage
			result.Error = resp.ErrorMessage
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error_class": errorClass, "result": result})
		return
	}
	var launch schema.LaunchAppResponse
	if err := json.Unmarshal(body, &launch); err != nil {
		http.Error(w, fmt.Sprintf("decode launch response: %v", err), http.StatusBadGateway)
		return
	}
	uid := uint32(identity.UID)
	result := schema.AppLaunchActionResponse{
		Action: "app.launch", CatalogID: entry.ID, AppID: entry.ExpectedAppID, Decision: schema.SurfaceActionAccepted,
		Reason: "launch accepted", Actor: "human-shell", ActorUID: &uid,
		LaunchID: launch.LaunchID, PID: launch.PID, Surface: launch.Surface,
	}
	if result.AppID == "" && launch.Surface != nil {
		result.AppID = launch.Surface.Surface.AppID
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func appLaunchDenied(catalogID string, actorUID uint32, reason string) schema.AppLaunchActionResponse {
	return schema.AppLaunchActionResponse{Action: "app.launch", CatalogID: catalogID, Decision: schema.SurfaceActionDenied, Reason: reason, Error: reason, Actor: "human-shell", ActorUID: &actorUID}
}

func (s *Server) handleGrant(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, _, err := s.authenticateHuman(r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	var req grantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode grant: %v", err), http.StatusBadRequest)
		return
	}
	if req.SurfaceID == "" || req.AgentUID == 0 {
		http.Error(w, "surface_id and agent_uid are required", http.StatusBadRequest)
		return
	}
	if len(req.Actions) == 0 {
		req.Actions = []schema.CompositorAccessAction{
			schema.AccessPointer,
			schema.AccessKeyboard,
			schema.AccessReadPixels,
		}
	}

	body, err := call(s.compositorSocket, schema.MethodGrantViewport, schema.ViewportGrantRequest{
		SurfaceID: req.SurfaceID,
		AgentUID:  req.AgentUID,
		Actions:   req.Actions,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleSurfaceFocus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _, err := s.authenticateHuman(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req schema.FocusSurfaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode focus request: %v", err), http.StatusBadRequest)
		return
	}
	if req.SurfaceID == "" {
		http.Error(w, "surface_id is required", http.StatusBadRequest)
		return
	}
	body, resp, err := callResponse(s.compositorSocket, schema.MethodFocusSurface, req)
	if err != nil {
		result := schema.SurfaceActionResponse{
			Action: "surface.focus", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied,
			Reason: err.Error(), Error: err.Error(), Actor: "human-shell",
		}
		uid := uint32(identity.UID)
		result.ActorUID = &uid
		if resp != nil {
			result.Reason = resp.ErrorMessage
			result.Error = resp.ErrorMessage
			var decoded schema.SurfaceActionResponse
			if decodeErr := json.Unmarshal(resp.Body, &decoded); decodeErr == nil && decoded.Action != "" {
				result = decoded
				if result.Actor == "" {
					result.Actor = "human-shell"
				}
				if result.ActorUID == nil {
					result.ActorUID = &uid
				}
			}
		}
		status := http.StatusBadGateway
		if resp != nil && (resp.ErrorClass == schema.ErrorSurfaceNotFound || resp.ErrorClass == schema.ErrorSurfaceStale) {
			status = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(surfaceActionHTTPError{ErrorClass: respErrorClass(resp), Result: result})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleSurfaceMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _, err := s.authenticateHuman(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req schema.MoveSurfaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode move request: %v", err), http.StatusBadRequest)
		return
	}
	if req.SurfaceID == "" {
		http.Error(w, "surface_id is required", http.StatusBadRequest)
		return
	}
	body, resp, err := callResponse(s.compositorSocket, schema.MethodMoveSurface, req)
	if err != nil {
		target := schema.SurfaceGeometry{X: req.X, Y: req.Y, Width: req.Width, Height: req.Height}
		result := schema.SurfaceActionResponse{
			Action: "surface.move", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied,
			Reason: err.Error(), Error: err.Error(), Actor: "human-shell", TargetGeometry: &target,
		}
		uid := uint32(identity.UID)
		result.ActorUID = &uid
		if resp != nil {
			result.Reason = resp.ErrorMessage
			result.Error = resp.ErrorMessage
			var decoded schema.SurfaceActionResponse
			if decodeErr := json.Unmarshal(resp.Body, &decoded); decodeErr == nil && decoded.Action != "" {
				result = decoded
				if result.Actor == "" {
					result.Actor = "human-shell"
				}
				if result.ActorUID == nil {
					result.ActorUID = &uid
				}
			}
		}
		status := http.StatusBadGateway
		if resp != nil && (resp.ErrorClass == schema.ErrorSurfaceNotFound || resp.ErrorClass == schema.ErrorSurfaceStale || resp.ErrorClass == schema.ErrorBackendUnsupported || resp.ErrorClass == schema.ErrorInvalidCoordinates) {
			status = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(surfaceActionHTTPError{ErrorClass: respErrorClass(resp), Result: result})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleSurfaceResize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _, err := s.authenticateHuman(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req schema.ResizeSurfaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode resize request: %v", err), http.StatusBadRequest)
		return
	}
	if req.SurfaceID == "" {
		http.Error(w, "surface_id is required", http.StatusBadRequest)
		return
	}
	body, resp, err := callResponse(s.compositorSocket, schema.MethodResizeSurface, req)
	if err != nil {
		target := schema.SurfaceGeometry{Width: req.Width, Height: req.Height}
		result := schema.SurfaceActionResponse{
			Action: "surface.resize", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied,
			Reason: err.Error(), Error: err.Error(), Actor: "human-shell", TargetGeometry: &target,
		}
		uid := uint32(identity.UID)
		result.ActorUID = &uid
		if resp != nil {
			result.Reason = resp.ErrorMessage
			result.Error = resp.ErrorMessage
			var decoded schema.SurfaceActionResponse
			if decodeErr := json.Unmarshal(resp.Body, &decoded); decodeErr == nil && decoded.Action != "" {
				result = decoded
				if result.Actor == "" {
					result.Actor = "human-shell"
				}
				if result.ActorUID == nil {
					result.ActorUID = &uid
				}
			}
		}
		status := http.StatusBadGateway
		if resp != nil && (resp.ErrorClass == schema.ErrorSurfaceNotFound || resp.ErrorClass == schema.ErrorSurfaceStale || resp.ErrorClass == schema.ErrorBackendUnsupported || resp.ErrorClass == schema.ErrorInvalidCoordinates) {
			status = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(surfaceActionHTTPError{ErrorClass: respErrorClass(resp), Result: result})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleSurfaceTile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _, err := s.authenticateHuman(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req schema.TileSurfaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode tile request: %v", err), http.StatusBadRequest)
		return
	}
	if req.SurfaceID == "" {
		http.Error(w, "surface_id is required", http.StatusBadRequest)
		return
	}
	body, resp, err := callResponse(s.compositorSocket, schema.MethodTileSurface, req)
	if err != nil {
		result := schema.SurfaceActionResponse{Action: "surface.tile", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied, Reason: err.Error(), Error: err.Error(), Actor: "human-shell"}
		uid := uint32(identity.UID)
		result.ActorUID = &uid
		if resp != nil {
			result.Reason = resp.ErrorMessage
			result.Error = resp.ErrorMessage
			var decoded schema.SurfaceActionResponse
			if decodeErr := json.Unmarshal(resp.Body, &decoded); decodeErr == nil && decoded.Action != "" {
				result = decoded
				if result.Actor == "" {
					result.Actor = "human-shell"
				}
				if result.ActorUID == nil {
					result.ActorUID = &uid
				}
			}
		}
		status := http.StatusBadGateway
		if resp != nil && (resp.ErrorClass == schema.ErrorSurfaceNotFound || resp.ErrorClass == schema.ErrorSurfaceStale || resp.ErrorClass == schema.ErrorBackendUnsupported || resp.ErrorClass == schema.ErrorInvalidCoordinates) {
			status = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(surfaceActionHTTPError{ErrorClass: respErrorClass(resp), Result: result})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleSurfaceAlwaysOnTop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _, err := s.authenticateHuman(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req schema.AlwaysOnTopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode always_on_top request: %v", err), http.StatusBadRequest)
		return
	}
	if req.SurfaceID == "" {
		http.Error(w, "surface_id is required", http.StatusBadRequest)
		return
	}
	body, resp, err := callResponse(s.compositorSocket, schema.MethodAlwaysOnTop, req)
	if err != nil {
		value := req.Enabled
		state := schema.SurfaceState{AlwaysOnTop: &value}
		result := schema.SurfaceActionResponse{Action: "surface.always_on_top", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied, Reason: err.Error(), Error: err.Error(), Actor: "human-shell", TargetState: &state, AlwaysOnTop: &value}
		uid := uint32(identity.UID)
		result.ActorUID = &uid
		if resp != nil {
			result.Reason = resp.ErrorMessage
			result.Error = resp.ErrorMessage
			var decoded schema.SurfaceActionResponse
			if decodeErr := json.Unmarshal(resp.Body, &decoded); decodeErr == nil && decoded.Action != "" {
				result = decoded
				if result.Actor == "" {
					result.Actor = "human-shell"
				}
				if result.ActorUID == nil {
					result.ActorUID = &uid
				}
			}
		}
		status := http.StatusBadGateway
		if resp != nil && (resp.ErrorClass == schema.ErrorSurfaceNotFound || resp.ErrorClass == schema.ErrorSurfaceStale || resp.ErrorClass == schema.ErrorBackendUnsupported) {
			status = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(surfaceActionHTTPError{ErrorClass: respErrorClass(resp), Result: result})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleSurfaceFullscreen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _, err := s.authenticateHuman(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req schema.FullscreenSurfaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode fullscreen request: %v", err), http.StatusBadRequest)
		return
	}
	if req.SurfaceID == "" {
		http.Error(w, "surface_id is required", http.StatusBadRequest)
		return
	}
	body, resp, err := callResponse(s.compositorSocket, schema.MethodFullscreenSurface, req)
	if err != nil {
		value := req.Enabled
		state := schema.SurfaceState{Fullscreen: &value}
		result := schema.SurfaceActionResponse{Action: "surface.fullscreen", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied, Reason: err.Error(), Error: err.Error(), Actor: "human-shell", TargetState: &state, Fullscreen: &value}
		uid := uint32(identity.UID)
		result.ActorUID = &uid
		if resp != nil {
			result.Reason = resp.ErrorMessage
			result.Error = resp.ErrorMessage
			var decoded schema.SurfaceActionResponse
			if decodeErr := json.Unmarshal(resp.Body, &decoded); decodeErr == nil && decoded.Action != "" {
				result = decoded
				if result.Actor == "" {
					result.Actor = "human-shell"
				}
				if result.ActorUID == nil {
					result.ActorUID = &uid
				}
			}
		}
		status := http.StatusBadGateway
		if resp != nil && (resp.ErrorClass == schema.ErrorSurfaceNotFound || resp.ErrorClass == schema.ErrorSurfaceStale || resp.ErrorClass == schema.ErrorBackendUnsupported) {
			status = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(surfaceActionHTTPError{ErrorClass: respErrorClass(resp), Result: result})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleSurfaceMaximize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _, err := s.authenticateHuman(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req schema.MaximizeSurfaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode maximize request: %v", err), http.StatusBadRequest)
		return
	}
	if req.SurfaceID == "" {
		http.Error(w, "surface_id is required", http.StatusBadRequest)
		return
	}
	body, resp, err := callResponse(s.compositorSocket, schema.MethodMaximizeSurface, req)
	if err != nil {
		value := req.Enabled
		state := schema.SurfaceState{Maximized: &value}
		result := schema.SurfaceActionResponse{Action: "surface.maximize", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied, Reason: err.Error(), Error: err.Error(), Actor: "human-shell", TargetState: &state, Maximized: &value}
		uid := uint32(identity.UID)
		result.ActorUID = &uid
		if resp != nil {
			result.Reason = resp.ErrorMessage
			result.Error = resp.ErrorMessage
			var decoded schema.SurfaceActionResponse
			if decodeErr := json.Unmarshal(resp.Body, &decoded); decodeErr == nil && decoded.Action != "" {
				result = decoded
				if result.Actor == "" {
					result.Actor = "human-shell"
				}
				if result.ActorUID == nil {
					result.ActorUID = &uid
				}
			}
		}
		status := http.StatusBadGateway
		if resp != nil && (resp.ErrorClass == schema.ErrorSurfaceNotFound || resp.ErrorClass == schema.ErrorSurfaceStale || resp.ErrorClass == schema.ErrorBackendUnsupported) {
			status = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(surfaceActionHTTPError{ErrorClass: respErrorClass(resp), Result: result})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleSurfaceDebugRaise(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _, err := s.authenticateHuman(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req schema.DebugRaiseSurfaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode debug raise request: %v", err), http.StatusBadRequest)
		return
	}
	if req.SurfaceID == "" {
		http.Error(w, "surface_id is required", http.StatusBadRequest)
		return
	}
	if req.Mode == "" {
		req.Mode = "no-focus"
	}
	body, resp, err := callResponse(s.compositorSocket, schema.MethodDebugRaiseSurface, req)
	if err != nil {
		result := schema.SurfaceActionResponse{Action: "surface.raise.debug", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied, Reason: err.Error(), Error: err.Error(), Actor: "human-shell"}
		uid := uint32(identity.UID)
		result.ActorUID = &uid
		if resp != nil {
			result.Reason = resp.ErrorMessage
			result.Error = resp.ErrorMessage
			var decoded schema.SurfaceActionResponse
			if decodeErr := json.Unmarshal(resp.Body, &decoded); decodeErr == nil && decoded.Action != "" {
				result = decoded
				if result.Actor == "" {
					result.Actor = "human-shell"
				}
				if result.ActorUID == nil {
					result.ActorUID = &uid
				}
			}
		}
		status := http.StatusBadGateway
		if resp != nil && (resp.ErrorClass == schema.ErrorSurfaceNotFound || resp.ErrorClass == schema.ErrorSurfaceStale || resp.ErrorClass == schema.ErrorBackendUnsupported) {
			status = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(surfaceActionHTTPError{ErrorClass: respErrorClass(resp), Result: result})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleSurfaceClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _, err := s.authenticateHuman(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req schema.CloseSurfaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode close request: %v", err), http.StatusBadRequest)
		return
	}
	if req.SurfaceID == "" {
		http.Error(w, "surface_id is required", http.StatusBadRequest)
		return
	}
	body, resp, err := callResponse(s.compositorSocket, schema.MethodCloseSurface, req)
	if err != nil {
		result := schema.SurfaceActionResponse{
			Action: "surface.close", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied,
			Reason: err.Error(), Error: err.Error(), Actor: "human-shell",
		}
		uid := uint32(identity.UID)
		result.ActorUID = &uid
		if resp != nil {
			result.Reason = resp.ErrorMessage
			result.Error = resp.ErrorMessage
			var decoded schema.SurfaceActionResponse
			if decodeErr := json.Unmarshal(resp.Body, &decoded); decodeErr == nil && decoded.Action != "" {
				result = decoded
				if result.Actor == "" {
					result.Actor = "human-shell"
				}
				if result.ActorUID == nil {
					result.ActorUID = &uid
				}
			}
		}
		status := http.StatusBadGateway
		if resp != nil && (resp.ErrorClass == schema.ErrorSurfaceNotFound || resp.ErrorClass == schema.ErrorSurfaceStale || resp.ErrorClass == schema.ErrorBackendUnsupported) {
			status = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(surfaceActionHTTPError{ErrorClass: respErrorClass(resp), Result: result})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (s *Server) handleEscalationDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	identity, _, err := s.authenticateHuman(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	var req escalationDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode decision: %v", err), http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if req.Decision != schema.DecisionApprove && req.Decision != schema.DecisionDeny {
		http.Error(w, "decision must be approve or deny", http.StatusBadRequest)
		return
	}

	pending, pendingByID, err := s.pendingEscalationsWithIndex()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = pending
	entry, ok := pendingByID[req.ID]
	if !ok {
		http.Error(w, "pending escalation not found", http.StatusNotFound)
		return
	}

	decision := schema.HumanEscalationDecision{
		ID:          req.ID,
		Timestamp:   s.now().UTC(),
		ReviewedBy:  identity.UID,
		Decision:    req.Decision,
		Constraints: append([]string(nil), req.Constraints...),
		Notes:       strings.TrimSpace(req.Notes),
		Request:     entry.Request,
		Response:    entry.Response,
	}
	if err := appendJSONL(s.decisionLogPath, decision); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishBusEvent(schema.TopicAdminEscalationDecided, decision)
	writeJSON(w, decision)
}

func (s *Server) handleThemeCSS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	themePath := filepath.Join(s.shellConfigDir, "theme.css")
	if !strings.HasPrefix(filepath.Clean(themePath), filepath.Clean(s.shellConfigDir)+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	raw, err := os.ReadFile(themePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filtered, warnings := sanitizeThemeCSS(string(raw))
	if len(warnings) > 0 {
		w.Header().Set("X-Agora-CSS-Warnings", strings.Join(warnings, "; "))
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write([]byte(filtered))
}

func (s *Server) handleLayoutJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	layoutPath := filepath.Join(s.shellConfigDir, "layout.json")
	if !strings.HasPrefix(filepath.Clean(layoutPath), filepath.Clean(s.shellConfigDir)+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	raw, err := os.ReadFile(layoutPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !json.Valid(raw) {
		http.Error(w, "layout.json is not valid JSON", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) handleWidgetProxy(w http.ResponseWriter, r *http.Request, proxyPath string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
	parts := strings.SplitN(proxyPath, "/", 2)
	if len(parts) != 2 || !validWidgetName(parts[0]) {
		http.NotFound(w, r)
		return
	}
	filePath := path.Clean("/" + parts[1])
	if filePath == "/" || strings.Contains(filePath, "/.") {
		http.NotFound(w, r)
		return
	}
	widgetRoot := filepath.Join(s.shellConfigDir, "widgets", parts[0])
	localPath := filepath.Join(widgetRoot, filepath.FromSlash(strings.TrimPrefix(filePath, "/")))
	resolvedPath, ok, err := resolvedPathWithinDir(widgetRoot, localPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if filePath == "/manifest.json" {
		raw, err := os.ReadFile(resolvedPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !json.Valid(raw) {
			http.Error(w, "manifest.json is not valid JSON", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
		return
	}
	raw, err := os.ReadFile(resolvedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(resolvedPath)))
	_, _ = w.Write(raw)
}

func (s *Server) handleAuditWS(w http.ResponseWriter, r *http.Request) {
	_, selectedSubprotocol, err := s.authenticateHuman(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	var responseHeader http.Header
	if selectedSubprotocol != "" {
		responseHeader = http.Header{"Sec-WebSocket-Protocol": []string{selectedSubprotocol}}
	}
	ws, err := s.upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		return
	}
	defer ws.Close()

	conn, err := net.Dial("unix", s.auditSocket)
	if err != nil {
		_ = webbus.WriteCloseInternalError(ws, "connect audit stream failed")
		return
	}
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		if err := ws.WriteMessage(websocket.TextMessage, scanner.Bytes()); err != nil {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		_ = webbus.WriteCloseInternalError(ws, "audit stream failed")
	}
}

func (s *Server) listAgents() ([]schema.AgentInfo, error) {
	body, err := call(s.isolationSocket, schema.MethodListAgents, nil)
	if err != nil {
		return nil, err
	}
	var resp schema.ListAgentsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode agents: %w", err)
	}
	slices.SortFunc(resp.Agents, func(a, b schema.AgentInfo) int {
		if a.UID < b.UID {
			return -1
		}
		if a.UID > b.UID {
			return 1
		}
		return 0
	})
	return resp.Agents, nil
}

func (s *Server) listSurfaces() ([]schema.CompositorTrackedSurface, error) {
	body, err := call(s.compositorSocket, schema.MethodListSurfaces, nil)
	if err != nil {
		return nil, err
	}
	var resp schema.ListSurfacesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode surfaces: %w", err)
	}
	slices.SortFunc(resp.Surfaces, func(a, b schema.CompositorTrackedSurface) int {
		if a.UpdatedAt.After(b.UpdatedAt) {
			return -1
		}
		if a.UpdatedAt.Before(b.UpdatedAt) {
			return 1
		}
		return strings.Compare(a.Surface.ID, b.Surface.ID)
	})
	return resp.Surfaces, nil
}

func (s *Server) pendingEscalations() ([]schema.AdminEscalationEvent, error) {
	pending, _, err := s.pendingEscalationsWithIndex()
	return pending, err
}

func (s *Server) pendingEscalationsWithIndex() ([]schema.AdminEscalationEvent, map[string]schema.AdminEscalationEvent, error) {
	events, err := loadAdminEscalations(s.adminLogPath)
	if err != nil {
		return nil, nil, err
	}
	decisions, err := loadHumanDecisions(s.decisionLogPath)
	if err != nil {
		return nil, nil, err
	}

	pending := make([]schema.AdminEscalationEvent, 0, len(events))
	index := make(map[string]schema.AdminEscalationEvent)
	for _, event := range events {
		if event.Response.Decision != schema.DecisionEscalate {
			continue
		}
		if _, ok := decisions[event.ID]; ok {
			continue
		}
		pending = append(pending, event)
		index[event.ID] = event
	}
	slices.SortFunc(pending, func(a, b schema.AdminEscalationEvent) int {
		if a.Timestamp.After(b.Timestamp) {
			return -1
		}
		if a.Timestamp.Before(b.Timestamp) {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return pending, index, nil
}

func loadAdminEscalations(path string) ([]schema.AdminEscalationEvent, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open admin log: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var events []schema.AdminEscalationEvent
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, fmt.Errorf("decode admin log: %w", err)
		}
		if _, hasDecision := raw["decision"]; hasDecision {
			continue
		}
		var entry loggedEscalation
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("decode admin log: %w", err)
		}
		events = append(events, schema.AdminEscalationEvent{
			ID:        lineHashID(line),
			Timestamp: entry.Timestamp,
			Request:   entry.Request,
			Response:  entry.Response,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan admin log: %w", err)
	}
	return events, nil
}

func loadHumanDecisions(path string) (map[string]schema.HumanEscalationDecision, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]schema.HumanEscalationDecision{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open decision log: %w", err)
	}
	defer file.Close()

	decisions := make(map[string]schema.HumanEscalationDecision)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		var decision schema.HumanEscalationDecision
		if err := json.Unmarshal(scanner.Bytes(), &decision); err != nil {
			return nil, fmt.Errorf("decode decision log: %w", err)
		}
		decisions[decision.ID] = decision
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan decision log: %w", err)
	}
	return decisions, nil
}

func (s *Server) authenticateHuman(r *http.Request) (webbus.Identity, string, error) {
	identity, selectedSubprotocol, err := webbus.AuthenticateRequest(s.secret, s.now(), r)
	if err != nil {
		return webbus.Identity{}, "", err
	}
	if identity.Role != webbus.RoleHuman {
		return webbus.Identity{}, "", errors.New("human token required")
	}
	return identity, selectedSubprotocol, nil
}

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) checkOrigin(r *http.Request) bool {
	return webbus.CheckOrigin(s.allowedOrigins, r)
}

func (s *Server) publishBusEvent(topic string, body any) {
	client, err := bus.Dial(s.busSocket)
	if err != nil {
		return
	}
	defer client.Close()
	_ = client.Publish(topic, body)
}

func appendJSONL(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open decision log: %w", err)
	}
	defer file.Close()

	line, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode decision: %w", err)
	}
	line = append(line, '\n')
	if _, err := file.Write(line); err != nil {
		return fmt.Errorf("write decision log: %w", err)
	}
	return nil
}

func call(sock, method string, body any) (json.RawMessage, error) {
	responseBody, _, err := callResponse(sock, method, body)
	return responseBody, err
}

func callResponse(sock, method string, body any) (json.RawMessage, *schema.Response, error) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, nil, fmt.Errorf("connect %s: %w", sock, err)
	}
	defer conn.Close()

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("encode request body: %w", err)
	}
	req := schema.Request{Method: method, Body: payload}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, nil, fmt.Errorf("send request: %w", err)
	}

	var resp schema.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, nil, fmt.Errorf("decode response: %w", err)
	}
	if !resp.OK {
		var msg string
		if err := json.Unmarshal(resp.Body, &msg); err == nil && msg != "" {
			return nil, &resp, errors.New(msg)
		}
		return nil, &resp, fmt.Errorf("request %s failed", method)
	}
	return resp.Body, &resp, nil
}

func respErrorClass(resp *schema.Response) string {
	if resp == nil {
		return ""
	}
	return resp.ErrorClass
}

func lineHashID(line []byte) string {
	sum := sha256.Sum256(bytesTrimSpace(line))
	return hex.EncodeToString(sum[:8])
}

func bytesTrimSpace(line []byte) []byte {
	return []byte(strings.TrimSpace(string(line)))
}

func sanitizeThemeCSS(css string) (string, []string) {
	css = stripCSSComments(css)
	var out strings.Builder
	var warnings []string
	for _, block := range strings.Split(css, "}") {
		selectorPart, declarationPart, ok := strings.Cut(block, "{")
		if !ok {
			continue
		}
		selectors := normalizeSelectors(selectorPart)
		if len(selectors) == 0 || !allowedSelectors(selectors) {
			warnings = append(warnings, "stripped selector "+strings.TrimSpace(selectorPart))
			continue
		}
		declarations, stripped := sanitizeDeclarations(declarationPart)
		warnings = append(warnings, stripped...)
		if len(declarations) == 0 {
			continue
		}
		out.WriteString(strings.Join(selectors, ", "))
		out.WriteString(" {\n")
		for _, declaration := range declarations {
			out.WriteString("  ")
			out.WriteString(declaration)
			out.WriteString(";\n")
		}
		out.WriteString("}\n")
	}
	return out.String(), warnings
}

func stripCSSComments(css string) string {
	for {
		start := strings.Index(css, "/*")
		if start < 0 {
			return css
		}
		end := strings.Index(css[start+2:], "*/")
		if end < 0 {
			return css[:start]
		}
		css = css[:start] + css[start+2+end+2:]
	}
}

func normalizeSelectors(raw string) []string {
	var selectors []string
	for _, selector := range strings.Split(raw, ",") {
		selector = strings.TrimSpace(selector)
		if selector != "" {
			selectors = append(selectors, selector)
		}
	}
	return selectors
}

func allowedSelectors(selectors []string) bool {
	allowed := map[string]struct{}{
		":root":                      {},
		".shell-taskbar":             {},
		".shell-clock":               {},
		".shell-notification-center": {},
		".shell-agent-health":        {},
		".shell-background":          {},
	}
	for _, selector := range selectors {
		if _, ok := allowed[selector]; !ok {
			return false
		}
	}
	return true
}

func sanitizeDeclarations(raw string) ([]string, []string) {
	var declarations []string
	var warnings []string
	for _, declaration := range strings.Split(raw, ";") {
		property, value, ok := strings.Cut(declaration, ":")
		if !ok {
			continue
		}
		property = strings.ToLower(strings.TrimSpace(property))
		value = strings.TrimSpace(value)
		if property == "" || value == "" {
			continue
		}
		if !allowedCSSProperty(property) {
			warnings = append(warnings, "stripped property "+property)
			continue
		}
		if !safeCSSValue(value) {
			warnings = append(warnings, "stripped unsafe value for "+property)
			continue
		}
		declarations = append(declarations, property+": "+value)
	}
	return declarations, warnings
}

func safeCSSValue(value string) bool {
	if strings.Contains(value, "\\") {
		return false
	}
	lower := strings.ToLower(value)
	unsafeFragments := []string{
		"url(",
		"@import",
		"expression(",
		"behavior:",
		"javascript:",
		"data:",
		"-moz-binding",
	}
	for _, fragment := range unsafeFragments {
		if strings.Contains(lower, fragment) {
			return false
		}
	}
	return true
}

func allowedCSSProperty(property string) bool {
	switch property {
	case "color", "background", "opacity", "margin", "padding", "box-shadow", "backdrop-filter", "filter":
		return true
	}
	return strings.HasPrefix(property, "border-") || strings.HasPrefix(property, "font-")
}

func validWidgetName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (i > 0 && (r == '-' || r == '_')) {
			continue
		}
		return false
	}
	return true
}

func resolvedPathWithinDir(root, candidate string) (string, bool, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false, err
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false, err
	}
	resolvedRoot = filepath.Clean(resolvedRoot)
	resolvedCandidate = filepath.Clean(resolvedCandidate)
	return resolvedCandidate, resolvedCandidate == resolvedRoot || strings.HasPrefix(resolvedCandidate, resolvedRoot+string(os.PathSeparator)), nil
}

func defaultShellConfigDir() string {
	return DefaultShellConfigDir
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
