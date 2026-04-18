package shellui

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
	"github.com/patch/agora-os/internal/webbus"
)

const (
	defaultAdminLogPath     = "/var/log/agent-os/admin-agent.log"
	defaultDecisionLogPath  = "/var/log/agent-os/admin-human-decisions.jsonl"
	defaultShellAuditWSPath = "/api/shell/audit/ws"
)

type Config struct {
	Secret           []byte
	Now              func() time.Time
	AllowedOrigins   map[string]struct{}
	Assets           fs.FS
	BusSocket        string
	IsolationSocket  string
	CompositorSocket string
	AuditSocket      string
	AdminLogPath     string
	DecisionLogPath  string
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
	upgrader         websocket.Upgrader
}

type State struct {
	Agents             []schema.AgentInfo                `json:"agents,omitempty"`
	Surfaces           []schema.CompositorTrackedSurface `json:"surfaces,omitempty"`
	PendingEscalations []schema.AdminEscalationEvent     `json:"pending_escalations,omitempty"`
}

type grantRequest struct {
	SurfaceID string                          `json:"surface_id"`
	AgentUID  uint32                          `json:"agent_uid"`
	Actions   []schema.CompositorAccessAction `json:"actions,omitempty"`
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
	if cfg.Assets != nil {
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
	}
	s.upgrader = websocket.Upgrader{CheckOrigin: s.checkOrigin}
	return s
}

func (s *Server) StaticHandler() http.Handler {
	return s.assets
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch path.Clean(strings.TrimPrefix(r.URL.Path, "/api/shell")) {
	case "/state":
		s.handleState(w, r)
	case "/grants":
		s.handleGrant(w, r)
	case "/escalations/decide":
		s.handleEscalationDecision(w, r)
	case "/audit/ws":
		s.handleAuditWS(w, r)
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
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", sock, err)
	}
	defer conn.Close()

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request body: %w", err)
	}
	req := schema.Request{Method: method, Body: payload}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	var resp schema.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if !resp.OK {
		var msg string
		if err := json.Unmarshal(resp.Body, &msg); err == nil && msg != "" {
			return nil, errors.New(msg)
		}
		return nil, fmt.Errorf("request %s failed", method)
	}
	return resp.Body, nil
}

func lineHashID(line []byte) string {
	sum := sha256.Sum256(bytesTrimSpace(line))
	return hex.EncodeToString(sum[:8])
}

func bytesTrimSpace(line []byte) []byte {
	return []byte(strings.TrimSpace(string(line)))
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
