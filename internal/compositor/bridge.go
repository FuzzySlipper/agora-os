package compositor

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/patch/agora-os/internal/peercred"
	"github.com/patch/agora-os/internal/schema"
)

type publisher interface {
	Publish(topic string, body any) error
}

type Config struct {
	AllowedPluginUID uint32
	GrantLogPath     string
}

const launchSurfaceSettleDelay = 2 * time.Second

type classifiedError struct {
	class   string
	message string
}

func (e classifiedError) Error() string { return e.message }

func compositorError(class, format string, args ...any) error {
	return classifiedError{class: class, message: fmt.Sprintf(format, args...)}
}

func classifyError(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	var ce classifiedError
	if ok := errors.As(err, &ce); ok {
		return ce.class, ce.message
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not found") && strings.Contains(msg, "surface"):
		return schema.ErrorSurfaceNotFound, msg
	case strings.Contains(msg, "session") && strings.Contains(msg, "not found"):
		return schema.ErrorSessionNotFound, msg
	case strings.Contains(msg, "no plugin connected"):
		return schema.ErrorCompositorUnavailable, msg
	case strings.Contains(msg, "capture timed out") || strings.Contains(msg, "frame") && strings.Contains(msg, "timed out"):
		return schema.ErrorFrameTimeout, msg
	case strings.Contains(msg, "invalid coordinate") || strings.Contains(msg, "outside surface") || strings.Contains(msg, "outside bounds"):
		return schema.ErrorInvalidCoordinates, msg
	case strings.Contains(msg, "input") && (strings.Contains(msg, "denied") || strings.Contains(msg, "rejected") || strings.Contains(msg, "failed")):
		return schema.ErrorInputDenied, msg
	case strings.Contains(msg, "unsupported"):
		return schema.ErrorBackendUnsupported, msg
	default:
		return schema.ErrorProtocolError, msg
	}
}

type pluginSession struct {
	conn net.Conn
	enc  *json.Encoder
	mu   sync.Mutex
}

func (s *pluginSession) Send(msg any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode(msg)
}

func (s *pluginSession) Close() error {
	return s.conn.Close()
}

type launchRecord struct {
	process        schema.CompositorLaunchProcess
	cmd            *exec.Cmd
	done           chan struct{}
	expectedAppID  string
	expectedTitle  string
	expectedOutput string
}

type launchLifecycleEvent struct {
	Event         string   `json:"event"`
	SurfaceID     string   `json:"surface_id"`
	SurfaceKind   string   `json:"surface_kind"`
	AppID         string   `json:"app_id"`
	Title         string   `json:"title,omitempty"`
	PID           int32    `json:"pid"`
	UID           uint32   `json:"uid"`
	GID           uint32   `json:"gid"`
	Role          string   `json:"role"`
	Width         int      `json:"width,omitempty"`
	Height        int      `json:"height,omitempty"`
	Namespace     string   `json:"namespace,omitempty"`
	Layer         string   `json:"layer,omitempty"`
	Anchors       []string `json:"anchors,omitempty"`
	ExclusiveZone *bool    `json:"exclusive_zone,omitempty"`
}

type Bridge struct {
	bus              publisher
	allowedPluginUID uint32
	grantStore       *grantStore

	mu                sync.RWMutex
	plugin            *pluginSession
	surfaces          map[string]schema.CompositorTrackedSurface
	staleSurfaces     map[string]time.Time
	policies          map[string]schema.CompositorSurfacePolicy
	grants            map[string]map[uint32]schema.SurfaceAccessGrant
	actorUID          *uint32
	captureSeq        uint64
	captureWaiters    map[string]chan schema.CompositorCapturePluginResponse
	inputSeq          uint64
	inputWaiters      map[string]chan schema.CompositorInputPluginResponse
	placeSeq          uint64
	placeWaiters      map[string]chan schema.CompositorPlacePluginResponse
	focusSeq          uint64
	focusWaiters      map[string]chan schema.CompositorFocusPluginResponse
	raiseSeq          uint64
	raiseWaiters      map[string]chan schema.CompositorRaisePluginResponse
	propertySeq       uint64
	propertyWaiters   map[string]chan schema.CompositorPropertyPluginResponse
	stateSeq          uint64
	stateWaiters      map[string]chan schema.CompositorSurfaceStatePluginResponse
	sessionSeq        uint64
	launchSeq         uint64
	sessions          map[string]schema.CompositorSession
	launches          map[string]*launchRecord
	surfaceLaunch     map[string]string
	artifacts         map[string]schema.ArtifactRecord
	outputs           map[string]schema.LogicalOutput
	surfaceOutput     map[string]string
	appCommandSeq     uint64
	appCommandResults map[string]schema.AppCommandResponse
	layoutDefinitions map[string]schema.LayoutDefinition
	layoutTags        map[string]schema.LayoutTag
	surfacePlacements map[string]schema.SurfacePlacement
}

func New(bus publisher, cfg Config) (*Bridge, error) {
	store, err := newGrantStore(cfg.GrantLogPath)
	if err != nil {
		return nil, err
	}
	return &Bridge{
		bus:               bus,
		allowedPluginUID:  cfg.AllowedPluginUID,
		grantStore:        store,
		surfaces:          make(map[string]schema.CompositorTrackedSurface),
		staleSurfaces:     make(map[string]time.Time),
		policies:          make(map[string]schema.CompositorSurfacePolicy),
		grants:            make(map[string]map[uint32]schema.SurfaceAccessGrant),
		captureWaiters:    make(map[string]chan schema.CompositorCapturePluginResponse),
		inputWaiters:      make(map[string]chan schema.CompositorInputPluginResponse),
		placeWaiters:      make(map[string]chan schema.CompositorPlacePluginResponse),
		focusWaiters:      make(map[string]chan schema.CompositorFocusPluginResponse),
		raiseWaiters:      make(map[string]chan schema.CompositorRaisePluginResponse),
		propertyWaiters:   make(map[string]chan schema.CompositorPropertyPluginResponse),
		stateWaiters:      make(map[string]chan schema.CompositorSurfaceStatePluginResponse),
		sessions:          make(map[string]schema.CompositorSession),
		launches:          make(map[string]*launchRecord),
		surfaceLaunch:     make(map[string]string),
		artifacts:         make(map[string]schema.ArtifactRecord),
		outputs:           make(map[string]schema.LogicalOutput),
		surfaceOutput:     make(map[string]string),
		appCommandResults: make(map[string]schema.AppCommandResponse),
		layoutDefinitions: builtinLayoutDefinitions(),
		layoutTags:        builtinLayoutTags(),
		surfacePlacements: make(map[string]schema.SurfacePlacement),
	}, nil
}

func (b *Bridge) HandlePluginConn(conn net.Conn) {
	defer conn.Close()

	peerUID, err := peercred.PeerUID(conn)
	if err != nil {
		log.Printf("compositor bridge plugin peer credentials: %v", err)
		return
	}
	if !b.isAllowedPluginUID(peerUID) {
		log.Printf("compositor bridge rejected plugin peer uid=%d", peerUID)
		return
	}

	session := &pluginSession{conn: conn, enc: json.NewEncoder(conn)}
	previous := b.installPluginSession(session)
	if previous != nil {
		previous.Close()
	}
	defer b.clearPluginSession(session)

	if err := b.syncPluginSession(session); err != nil {
		log.Printf("compositor bridge sync failed: %v", err)
		return
	}

	dec := json.NewDecoder(conn)
	for {
		var msg schema.CompositorPluginEvent
		if err := dec.Decode(&msg); err != nil {
			return
		}
		switch msg.Type {
		case schema.PluginMessageSurfaceEvent:
			b.handleSurfaceEvent(msg)
		case schema.PluginMessageCaptureResponse:
			b.handleCaptureResponse(schema.CompositorCapturePluginResponse{
				Type:       msg.Type,
				RequestID:  msg.RequestID,
				SurfaceID:  msg.SurfaceID,
				OK:         msg.OK,
				Width:      msg.Width,
				Height:     msg.Height,
				Format:     msg.Format,
				DataBase64: msg.DataBase64,
				Error:      msg.Error,
			})
		case schema.PluginMessagePlaceResponse:
			b.handlePlaceResponse(schema.CompositorPlacePluginResponse{Type: string(msg.Type), RequestID: msg.RequestID, SurfaceID: msg.SurfaceID, OK: msg.OK, Error: msg.Error})
		case schema.PluginMessageFocusResponse:
			b.handleFocusResponse(schema.CompositorFocusPluginResponse{Type: msg.Type, RequestID: msg.RequestID, SurfaceID: msg.SurfaceID, OK: msg.OK, Error: msg.Error})
		case schema.PluginMessageRaiseResponse:
			b.handleRaiseResponse(schema.CompositorRaisePluginResponse{Type: msg.Type, RequestID: msg.RequestID, SurfaceID: msg.SurfaceID, OK: msg.OK, Error: msg.Error})
		case schema.PluginMessagePropertyResponse:
			b.handlePropertyResponse(schema.CompositorPropertyPluginResponse{Type: msg.Type, RequestID: msg.RequestID, SurfaceID: msg.SurfaceID, OK: msg.OK, Error: msg.Error})
		case schema.PluginMessageSurfaceStateResponse:
			b.handleSurfaceStateResponse(schema.CompositorSurfaceStatePluginResponse{Type: msg.Type, RequestID: msg.RequestID, SurfaceID: msg.SurfaceID, OK: msg.OK, Error: msg.Error})
		case schema.PluginMessageInputResponse:
			b.handleInputResponse(schema.CompositorInputPluginResponse{
				Type:      msg.Type,
				RequestID: msg.RequestID,
				SurfaceID: msg.SurfaceID,
				OK:        msg.OK,
				Accepted:  msg.Accepted,
				Rejected:  msg.Rejected,
				Error:     msg.Error,
			})
		}
	}
}

func (b *Bridge) HandleControlConn(conn net.Conn) {
	defer conn.Close()

	peerUID, err := peercred.PeerUID(conn)
	if err != nil {
		writeError(conn, fmt.Errorf("peer credentials: %w", err))
		return
	}

	var req schema.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeError(conn, fmt.Errorf("decode: %w", err))
		return
	}

	resp, err := b.dispatch(peerUID, req)
	if err != nil {
		writeError(conn, err)
		return
	}
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("write compositor response: %v", err)
	}
}

func (b *Bridge) ListSurfaces() []schema.CompositorTrackedSurface {
	b.mu.RLock()
	defer b.mu.RUnlock()

	surfaces := make([]schema.CompositorTrackedSurface, 0, len(b.surfaces))
	for _, surface := range b.surfaces {
		surfaces = append(surfaces, b.decorateSurfaceLocked(surface))
	}
	sort.Slice(surfaces, func(i, j int) bool {
		return surfaces[i].Surface.ID < surfaces[j].Surface.ID
	})
	return surfaces
}

func (b *Bridge) CreateSession(req schema.CreateSessionRequest) schema.CompositorSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessionSeq++
	now := time.Now()
	session := schema.CompositorSession{
		SessionID:          fmt.Sprintf("session-%d-%d", now.UnixNano(), b.sessionSeq),
		Label:              req.Label,
		ProjectID:          req.ProjectID,
		TaskID:             req.TaskID,
		AgentIdentity:      req.AgentIdentity,
		SessionToken:       generateSessionToken(),
		ASHAScenarioID:     req.ASHAScenarioID,
		RepoCommit:         req.RepoCommit,
		RepoBranch:         req.RepoBranch,
		ASHARuntimeMode:    req.ASHARuntimeMode,
		ArtifactRoot:       req.ArtifactRoot,
		AuditCorrelationID: req.AuditCorrelationID,
		CreatedAt:          now,
		LastUsedAt:         now,
	}
	b.sessions[session.SessionID] = session
	return b.hydrateSessionLocked(session)
}

func (b *Bridge) ListSessions() []schema.CompositorSession {
	b.mu.RLock()
	defer b.mu.RUnlock()
	sessions := make([]schema.CompositorSession, 0, len(b.sessions))
	for _, session := range b.sessions {
		sessions = append(sessions, b.hydrateSessionLocked(session))
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].SessionID < sessions[j].SessionID })
	return sessions
}

func (b *Bridge) GetSession(sessionID string) (schema.CompositorSession, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	session, ok := b.sessions[sessionID]
	if !ok {
		return schema.CompositorSession{}, compositorError(schema.ErrorSessionNotFound, "session %s not found", sessionID)
	}
	return b.hydrateSessionLocked(session), nil
}

func (b *Bridge) DestroySession(sessionID string) error {
	if err := b.ResetSession(sessionID); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.sessions, sessionID)
	for id, launch := range b.launches {
		if launch.process.SessionID == sessionID {
			delete(b.launches, id)
		}
	}
	for surfaceID, launchID := range b.surfaceLaunch {
		if launch := b.launches[launchID]; launch == nil || launch.process.SessionID == sessionID {
			delete(b.surfaceLaunch, surfaceID)
		}
	}
	b.mu.Unlock()
	return nil
}

func (b *Bridge) ResetSession(sessionID string) error {
	b.mu.RLock()
	if _, ok := b.sessions[sessionID]; !ok {
		b.mu.RUnlock()
		return compositorError(schema.ErrorSessionNotFound, "session %s not found", sessionID)
	}
	launchIDs := make([]string, 0)
	for id, launch := range b.launches {
		if launch.process.SessionID == sessionID &&
			(launch.process.Status == "running" || len(b.surfacesForLaunchLocked(launch)) > 0) {
			launchIDs = append(launchIDs, id)
		}
	}
	b.mu.RUnlock()
	var errs []string
	for _, id := range launchIDs {
		if _, err := b.TerminateLaunch(id); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("reset session %s cleanup failed: %s", sessionID, strings.Join(errs, "; "))
	}
	b.mu.Lock()
	session := b.sessions[sessionID]
	session.LastUsedAt = time.Now()
	b.sessions[sessionID] = session
	b.mu.Unlock()
	return nil
}

func (b *Bridge) LaunchApp(req schema.LaunchAppRequest) (schema.LaunchAppResponse, error) {
	return b.launchAppAsPeer(0, req)
}

func launchCommand(req schema.LaunchAppRequest) string {
	command := req.Command
	role := strings.TrimSpace(req.Role)
	if role != "" && role != "toplevel" && !strings.Contains(command, "--role") {
		command += " --role " + role
	}
	return command
}

func (b *Bridge) launchAppAsPeer(peerUID uint32, req schema.LaunchAppRequest) (schema.LaunchAppResponse, error) {
	if strings.TrimSpace(req.Command) == "" {
		return schema.LaunchAppResponse{}, fmt.Errorf("command is required")
	}
	if req.SessionID != "" {
		if err := b.requireSessionToken(req.SessionID, req.SessionToken); err != nil {
			return schema.LaunchAppResponse{}, err
		}
	}
	if req.Output != "" {
		b.mu.RLock()
		_, ok := b.outputs[req.Output]
		b.mu.RUnlock()
		if !ok {
			return schema.LaunchAppResponse{}, fmt.Errorf("output %s not found", req.Output)
		}
		req.WaitSurface = true
		if req.WaitTimeoutMs <= 0 {
			req.WaitTimeoutMs = 10000
		}
	}

	command := launchCommand(req)
	cmd := exec.Command("sh", "-lc", command)
	sys := &syscall.SysProcAttr{Setpgid: true}
	cred, err := launchCredential(peerUID, req)
	if err != nil {
		return schema.LaunchAppResponse{}, err
	}
	if cred != nil {
		sys.Credential = cred
	}
	cmd.SysProcAttr = sys
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return schema.LaunchAppResponse{}, fmt.Errorf("capture launch stdout: %w", err)
	}
	env := os.Environ()
	if _, ok := req.Env["XDG_RUNTIME_DIR"]; !ok {
		env = append(env, "XDG_RUNTIME_DIR=/run/user/1001")
	}
	if _, ok := req.Env["WAYLAND_DISPLAY"]; !ok {
		env = append(env, "WAYLAND_DISPLAY="+defaultWaylandDisplay())
	}
	if _, ok := req.Env["DISPLAY"]; !ok {
		env = append(env, "DISPLAY=")
	}
	env = withA11yLaunchEnv(env, req.Env)
	for key, value := range req.Env {
		env = append(env, key+"="+value)
	}
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return schema.LaunchAppResponse{}, err
	}

	b.mu.Lock()
	b.launchSeq++
	now := time.Now()
	launchID := fmt.Sprintf("launch-%d-%d", now.UnixNano(), b.launchSeq)
	process := schema.CompositorLaunchProcess{
		LaunchID:  launchID,
		SessionID: req.SessionID,
		PID:       cmd.Process.Pid,
		Command:   command,
		Cwd:       req.Cwd,
		Status:    "running",
		StartedAt: now,
	}
	b.launches[launchID] = &launchRecord{process: process, cmd: cmd, done: make(chan struct{}), expectedAppID: req.ExpectedAppID, expectedTitle: req.ExpectedTitle, expectedOutput: req.Output}
	if req.SessionID != "" {
		if session, ok := b.sessions[req.SessionID]; ok {
			session.LastUsedAt = now
			b.sessions[req.SessionID] = session
		}
	}
	b.mu.Unlock()

	go b.scanLaunchStdout(launchID, stdout)
	go b.waitLaunch(launchID, cmd)
	resp := schema.LaunchAppResponse{LaunchID: launchID, SessionID: req.SessionID, PID: cmd.Process.Pid}
	if req.WaitSurface {
		timeout := time.Duration(req.WaitTimeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		surface, ok := b.waitForLaunchSurface(launchID, timeout)
		if !ok {
			_, _ = b.TerminateLaunch(launchID)
			return schema.LaunchAppResponse{}, compositorError(schema.ErrorAppNotReady, "launch %s did not map a matching surface before timeout", launchID)
		}
		if req.Output != "" {
			moved, err := b.MoveSurfaceToOutput(surface.Surface.ID, req.Output)
			if err != nil {
				_, _ = b.TerminateLaunch(launchID)
				return schema.LaunchAppResponse{}, err
			}
			surface.OutputID = moved.Output
			surface.Geometry = &moved.Geometry
			surface.Surface.Geometry = &moved.Geometry
			surface.Surface.OutputID = moved.Output
		}
		resp.Surface = &surface
	}
	return resp, nil
}

func (b *Bridge) ListProcesses(sessionID string) []schema.CompositorLaunchProcess {
	b.mu.RLock()
	defer b.mu.RUnlock()
	processes := make([]schema.CompositorLaunchProcess, 0, len(b.launches))
	for _, launch := range b.launches {
		if sessionID == "" || launch.process.SessionID == sessionID {
			processes = append(processes, b.hydrateProcessLocked(launch.process))
		}
	}
	sort.Slice(processes, func(i, j int) bool { return processes[i].LaunchID < processes[j].LaunchID })
	return processes
}

func (b *Bridge) TerminateLaunch(launchID string) (schema.TerminateLaunchResponse, error) {
	b.mu.RLock()
	launch, ok := b.launches[launchID]
	if !ok {
		b.mu.RUnlock()
		return schema.TerminateLaunchResponse{}, fmt.Errorf("launch %s not found", launchID)
	}
	pid := launch.process.PID
	status := launch.process.Status
	cmd := launch.cmd
	done := launch.done
	surfaces := b.surfacesForLaunchLocked(launch)
	b.mu.RUnlock()

	signalSent := false
	if cmd != nil && status == "running" {
		if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
			signalSent = true
		} else if cmd.Process != nil {
			if err := cmd.Process.Kill(); err == nil {
				signalSent = true
			}
		}
		if signalSent && !b.waitForLaunchExit(launchID, done, 2*time.Second) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
			if !b.waitForLaunchExit(launchID, done, 2*time.Second) {
				return schema.TerminateLaunchResponse{}, fmt.Errorf("launch %s did not exit after terminate", launchID)
			}
		}
	}
	closedSurfaces := make([]string, 0, len(surfaces))
	var closeErrs []string
	for _, surfaceID := range surfaces {
		if err := b.CloseSurface(surfaceID); err != nil {
			closeErrs = append(closeErrs, fmt.Sprintf("%s: %v", surfaceID, err))
			continue
		}
		closedSurfaces = append(closedSurfaces, surfaceID)
	}
	resp := schema.TerminateLaunchResponse{LaunchID: launchID, SignalSent: signalSent, ClosedSurfaces: closedSurfaces}
	if len(closeErrs) > 0 {
		return resp, fmt.Errorf("close surfaces for launch %s failed: %s", launchID, strings.Join(closeErrs, "; "))
	}
	return resp, nil
}

func (b *Bridge) scanLaunchStdout(launchID string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event launchLifecycleEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if !isLaunchLifecycleEvent(event) {
			continue
		}
		b.handleLaunchLifecycleEvent(launchID, event)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("scan launch stdout for %s: %v", launchID, err)
	}
}

func isLaunchLifecycleEvent(event launchLifecycleEvent) bool {
	if event.Event == "" || event.SurfaceID == "" || event.PID <= 0 {
		return false
	}
	switch event.Event {
	case string(schema.SurfaceEventMapped), string(schema.SurfaceEventFocused), string(schema.SurfaceEventUnmapped):
		return true
	default:
		return false
	}
}

func (b *Bridge) handleLaunchLifecycleEvent(launchID string, event launchLifecycleEvent) {
	// Launcher stdout is intentionally ignored for readiness. Layer-shell launch
	// waits are satisfied only by compositor/plugin-observed surface events.
}

func pluginEventToTracked(msg schema.CompositorPluginEvent) schema.CompositorTrackedSurface {
	return schema.CompositorTrackedSurface{Surface: msg.Surface, Client: msg.Client, UpdatedAt: time.Now()}
}

func launchAllowsLayerLifecycle(launch *launchRecord) bool {
	if launch == nil {
		return false
	}
	command := strings.ToLower(launch.process.Command)
	return strings.Contains(command, "webview-launcher")
}

func launchLifecycleEventName(event string) (schema.CompositorSurfaceEventName, bool) {
	switch event {
	case string(schema.SurfaceEventMapped):
		return schema.SurfaceEventMapped, true
	case string(schema.SurfaceEventFocused):
		return schema.SurfaceEventFocused, true
	case string(schema.SurfaceEventUnmapped):
		return schema.SurfaceEventUnmapped, true
	default:
		return "", false
	}
}

func (b *Bridge) waitLaunch(launchID string, cmd *exec.Cmd) {
	err := cmd.Wait()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
	} else if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if launch, ok := b.launches[launchID]; ok {
		launch.process.Status = "exited"
		launch.process.ExitCode = &exitCode
		launch.process.ExitedAt = &now
		if launch.done != nil {
			close(launch.done)
			launch.done = nil
		}
	}
}

func (b *Bridge) waitForLaunchExit(launchID string, done <-chan struct{}, timeout time.Duration) bool {
	if done != nil {
		select {
		case <-done:
			return true
		case <-time.After(timeout):
			return false
		}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b.mu.RLock()
		launch := b.launches[launchID]
		exited := launch == nil || launch.process.Status != "running"
		b.mu.RUnlock()
		if exited {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

func (b *Bridge) waitForLaunchSurface(launchID string, timeout time.Duration) (schema.CompositorTrackedSurface, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b.mu.RLock()
		launch := b.launches[launchID]
		if launch != nil {
			if surface, ok := b.reconcileLaunchSurfaceLocked(launchID); ok && launchSurfaceSettled(surface) {
				surface = b.decorateSurfaceLocked(surface)
				b.mu.RUnlock()
				return surface, true
			}
		}
		b.mu.RUnlock()
		time.Sleep(50 * time.Millisecond)
	}
	return schema.CompositorTrackedSurface{}, false
}

func launchSurfaceSettled(surface schema.CompositorTrackedSurface) bool {
	settledSince := surface.UpdatedAt
	if surface.LastPresentTimestamp != nil {
		settledSince = *surface.LastPresentTimestamp
	}
	return time.Since(settledSince) >= launchSurfaceSettleDelay
}

func (b *Bridge) hydrateSessionLocked(session schema.CompositorSession) schema.CompositorSession {
	session.Surfaces = nil
	session.Processes = nil
	for _, launch := range b.launches {
		if launch.process.SessionID == session.SessionID {
			proc := b.hydrateProcessLocked(launch.process)
			session.Processes = append(session.Processes, proc)
			for _, surfaceID := range proc.Surfaces {
				if surface, ok := b.surfaces[surfaceID]; ok {
					session.Surfaces = append(session.Surfaces, surface)
				}
			}
		}
	}
	return session
}

func (b *Bridge) hydrateProcessLocked(process schema.CompositorLaunchProcess) schema.CompositorLaunchProcess {
	process.Surfaces = nil
	if launch := b.launches[process.LaunchID]; launch != nil {
		process.Surfaces = b.surfacesForLaunchLocked(launch)
	}
	return process
}

func (b *Bridge) surfacesForLaunchLocked(launch *launchRecord) []string {
	ids := make([]string, 0)
	for id := range b.surfaces {
		if b.surfaceLaunch[id] == launch.process.LaunchID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (b *Bridge) reconcileLaunchSurfaceLocked(launchID string) (schema.CompositorTrackedSurface, bool) {
	launch := b.launches[launchID]
	if launch == nil {
		return schema.CompositorTrackedSurface{}, false
	}
	for surfaceID, boundLaunchID := range b.surfaceLaunch {
		if boundLaunchID != launchID {
			continue
		}
		if surface, ok := b.surfaces[surfaceID]; ok {
			return surface, true
		}
	}

	var hintID string
	var hintSurface schema.CompositorTrackedSurface
	for surfaceID, surface := range b.surfaces {
		if boundLaunchID := b.surfaceLaunch[surfaceID]; boundLaunchID != "" && boundLaunchID != launchID {
			continue
		}
		if surface.UpdatedAt.Before(launch.process.StartedAt) {
			continue
		}
		if b.surfaceBelongsToLaunchLocked(surface, launch) {
			b.surfaceLaunch[surfaceID] = launchID
			return surface, true
		}
		if b.surfaceMatchesLaunchHint(surface, launch) {
			if hintID != "" {
				return schema.CompositorTrackedSurface{}, false
			}
			hintID = surfaceID
			hintSurface = surface
		}
	}
	if hintID != "" {
		b.surfaceLaunch[hintID] = launchID
		return hintSurface, true
	}
	return schema.CompositorTrackedSurface{}, false
}

func (b *Bridge) associateSurfaceLocked(surface schema.CompositorTrackedSurface) {
	if surface.Surface.ID == "" {
		return
	}
	if _, bound := b.surfaceLaunch[surface.Surface.ID]; bound {
		return
	}
	for id, launch := range b.launches {
		if launch.process.Status == "running" &&
			!surface.UpdatedAt.Before(launch.process.StartedAt) &&
			b.surfaceBelongsToLaunchLocked(surface, launch) {
			b.surfaceLaunch[surface.Surface.ID] = id
			return
		}
	}

	var hintMatch string
	for id, launch := range b.launches {
		if launch.process.Status != "running" || surface.UpdatedAt.Before(launch.process.StartedAt) {
			continue
		}
		if b.surfaceMatchesLaunchHint(surface, launch) {
			if hintMatch != "" {
				// Ambiguous hints are useful for readiness polling, but not safe enough
				// to establish durable launch/session ownership.
				return
			}
			hintMatch = id
		}
	}
	if hintMatch != "" {
		b.surfaceLaunch[surface.Surface.ID] = hintMatch
	}
}

func (b *Bridge) surfaceBelongsToLaunchLocked(surface schema.CompositorTrackedSurface, launch *launchRecord) bool {
	if launch == nil || surface.Client.PID <= 0 {
		return false
	}
	if int(surface.Client.PID) == launch.process.PID {
		return true
	}
	return processDescendsFrom(int(surface.Client.PID), launch.process.PID)
}

func processDescendsFrom(pid, ancestor int) bool {
	if pid <= 0 || ancestor <= 0 {
		return false
	}
	seen := map[int]struct{}{}
	for current := pid; current > 1; {
		if current == ancestor {
			return true
		}
		if _, ok := seen[current]; ok {
			return false
		}
		seen[current] = struct{}{}
		ppid, ok := parentPID(current)
		if !ok {
			return false
		}
		current = ppid
	}
	return false
}

func parentPID(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	text := string(data)
	end := strings.LastIndex(text, ")")
	if end < 0 || end+2 >= len(text) {
		return 0, false
	}
	fields := strings.Fields(text[end+2:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

func (b *Bridge) surfaceMatchesLaunchHint(surface schema.CompositorTrackedSurface, launch *launchRecord) bool {
	if launch == nil {
		return false
	}
	if launch.expectedAppID != "" && surface.Surface.AppID == launch.expectedAppID {
		return true
	}
	if launch.expectedTitle != "" && strings.Contains(surface.Surface.Title, launch.expectedTitle) {
		return true
	}
	return false
}

func (b *Bridge) CaptureSurface(req schema.CaptureSurfaceRequest) (schema.CaptureSurfaceResponse, error) {
	authorizedSessionID, err := b.authorizeSurfaceSession(req.SurfaceID, req.SessionID, req.SessionToken)
	if err != nil {
		return schema.CaptureSurfaceResponse{}, err
	}
	if req.SessionID == "" {
		req.SessionID = authorizedSessionID
	}
	req.AuditCorrelationID = b.sessionAuditCorrelation(req.SessionID, req.AuditCorrelationID)
	b.mu.Lock()
	if _, ok := b.surfaces[req.SurfaceID]; !ok {
		b.mu.Unlock()
		return schema.CaptureSurfaceResponse{}, compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
	}
	if b.plugin == nil {
		b.mu.Unlock()
		return schema.CaptureSurfaceResponse{}, compositorError(schema.ErrorCompositorUnavailable, "no plugin connected")
	}
	b.captureSeq++
	requestID := fmt.Sprintf("capture-%d-%d", time.Now().UnixNano(), b.captureSeq)
	ch := make(chan schema.CompositorCapturePluginResponse, 1)
	b.captureWaiters[requestID] = ch
	session := b.plugin
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.captureWaiters, requestID)
		b.mu.Unlock()
	}()

	if err := session.Send(schema.CompositorCaptureSurface{
		Type:      schema.PluginMessageCaptureSurface,
		RequestID: requestID,
		SurfaceID: req.SurfaceID,
	}); err != nil {
		return schema.CaptureSurfaceResponse{}, err
	}

	var pluginResp schema.CompositorCapturePluginResponse
	select {
	case pluginResp = <-ch:
	case <-time.After(5 * time.Second):
		return schema.CaptureSurfaceResponse{}, fmt.Errorf("capture timed out")
	}
	if !pluginResp.OK {
		return schema.CaptureSurfaceResponse{}, classifyCapturePluginError(pluginResp.Error)
	}
	if pluginResp.Format != "png" {
		return schema.CaptureSurfaceResponse{}, fmt.Errorf("unsupported capture response format %q", pluginResp.Format)
	}

	captureBytes, err := base64.StdEncoding.DecodeString(pluginResp.DataBase64)
	if err != nil {
		return schema.CaptureSurfaceResponse{}, fmt.Errorf("decode capture png: %w", err)
	}
	visualInspection, err := inspectCapturePNG(captureBytes)
	if err != nil {
		return schema.CaptureSurfaceResponse{}, fmt.Errorf("inspect capture png: %w", err)
	}
	sessionID := req.SessionID
	if sessionID == "" {
		b.mu.RLock()
		if surface, ok := b.surfaces[req.SurfaceID]; ok {
			sessionID = surface.SessionID
		}
		b.mu.RUnlock()
	}
	dir := "/run/agent-os/captures"
	if req.Export {
		if sessionID == "" {
			sessionID = "unscoped"
		}
		dir = filepath.Join("/run/agent-os/artifacts", sessionID, requestID)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return schema.CaptureSurfaceResponse{}, err
	}
	path := filepath.Join(dir, requestID+".png")
	if err := os.WriteFile(path, captureBytes, 0644); err != nil {
		return schema.CaptureSurfaceResponse{}, err
	}
	sum := sha256.Sum256(captureBytes)
	sha := hex.EncodeToString(sum[:])
	capturedAt := b.recordCaptureReadback(pluginResp.SurfaceID)
	resp := schema.CaptureSurfaceResponse{
		SurfaceID:        pluginResp.SurfaceID,
		RequestID:        requestID,
		Path:             path,
		ImagePath:        path,
		Width:            pluginResp.Width,
		Height:           pluginResp.Height,
		Format:           pluginResp.Format,
		SHA256:           sha,
		CapturedAt:       capturedAt,
		VisualInspection: visualInspection,
	}
	if req.Export {
		evidenceClass := req.EvidenceClass
		if evidenceClass == "" {
			evidenceClass = "surface_screenshot"
		}
		now := time.Now()
		artifact := schema.ArtifactRecord{
			ArtifactID: requestID, SessionID: sessionID, SurfaceID: pluginResp.SurfaceID, RequestID: requestID,
			ImagePath: path, IndexPath: filepath.Join(dir, "index.json"), Width: pluginResp.Width, Height: pluginResp.Height,
			Format: pluginResp.Format, SHA256: sha, CaptureBackend: "plugin_readback", AuditCorrelationID: req.AuditCorrelationID, EvidenceClass: evidenceClass,
			Timestamp: now, ASHACommandSequenceID: req.ASHACommandSequenceID, VisualInspection: visualInspection,
		}
		if visualInspection.Status == "blank" {
			artifact.Warnings = append(artifact.Warnings, "capture payload classified as "+visualInspection.Classification)
		}
		if sessionID == "unscoped" {
			artifact.Warnings = append(artifact.Warnings, "capture was not associated with a compositor session")
		}
		indexBytes, _ := json.MarshalIndent(artifact, "", "  ")
		if err := os.WriteFile(artifact.IndexPath, indexBytes, 0644); err != nil {
			return schema.CaptureSurfaceResponse{}, err
		}
		b.mu.Lock()
		b.artifacts[artifact.ArtifactID] = artifact
		b.mu.Unlock()
		resp.Artifact = &artifact
	}
	return resp, nil
}

func (b *Bridge) InjectInput(req schema.InjectInputRequest) (schema.InjectInputResponse, error) {
	if _, err := b.authorizeSurfaceSession(req.SurfaceID, req.SessionID, req.SessionToken); err != nil {
		return schema.InjectInputResponse{}, err
	}
	b.mu.Lock()
	if _, ok := b.surfaces[req.SurfaceID]; !ok {
		b.mu.Unlock()
		return schema.InjectInputResponse{}, compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
	}
	if b.plugin == nil {
		b.mu.Unlock()
		return schema.InjectInputResponse{}, compositorError(schema.ErrorCompositorUnavailable, "no plugin connected")
	}
	if len(req.Events) == 0 {
		b.mu.Unlock()
		return schema.InjectInputResponse{}, fmt.Errorf("at least one input event is required")
	}
	b.inputSeq++
	requestID := fmt.Sprintf("input-%d-%d", time.Now().UnixNano(), b.inputSeq)
	ch := make(chan schema.CompositorInputPluginResponse, 1)
	b.inputWaiters[requestID] = ch
	session := b.plugin
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.inputWaiters, requestID)
		b.mu.Unlock()
	}()

	coordinateSpace := req.CoordinateSpace
	if coordinateSpace == "" {
		coordinateSpace = "surface"
	}
	if err := session.Send(schema.CompositorInjectInput{
		Type:            schema.PluginMessageInjectInput,
		RequestID:       requestID,
		SurfaceID:       req.SurfaceID,
		CoordinateSpace: coordinateSpace,
		Events:          req.Events,
	}); err != nil {
		return schema.InjectInputResponse{}, err
	}

	var pluginResp schema.CompositorInputPluginResponse
	select {
	case pluginResp = <-ch:
	case <-time.After(5 * time.Second):
		return schema.InjectInputResponse{}, fmt.Errorf("input injection timed out")
	}
	if !pluginResp.OK {
		if pluginResp.Error == "" {
			pluginResp.Error = "input injection failed"
		}
		return schema.InjectInputResponse{}, classifyInputPluginError(pluginResp.Error)
	}
	return schema.InjectInputResponse{
		SurfaceID: pluginResp.SurfaceID,
		Accepted:  pluginResp.Accepted,
		Rejected:  pluginResp.Rejected,
	}, nil
}

func (b *Bridge) UpsertSurfacePolicy(policy schema.CompositorSurfacePolicy) error {
	msg := schema.CompositorPolicyUpsert{Type: schema.PluginMessagePolicyUpsert, Surface: policy}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.policies[policy.SurfaceID] = policy
	if b.plugin == nil {
		return nil
	}
	return b.plugin.Send(msg)
}

func (b *Bridge) RemoveSurfacePolicy(surfaceID string) error {
	msg := schema.CompositorPolicyRemove{Type: schema.PluginMessagePolicyRemove, SurfaceID: surfaceID}

	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.policies, surfaceID)
	if b.plugin == nil {
		return nil
	}
	return b.plugin.Send(msg)
}

func (b *Bridge) SetInputContext(actorUID *uint32) error {
	msg := schema.CompositorInputContextUpdate{Type: schema.PluginMessageInputContext}

	b.mu.Lock()
	defer b.mu.Unlock()

	if actorUID == nil {
		b.actorUID = nil
	} else {
		uid := *actorUID
		b.actorUID = &uid
		msg.ActorUID = &uid
	}

	if b.plugin == nil {
		return nil
	}
	return b.plugin.Send(msg)
}

func (b *Bridge) surfaceActionActor(actorUID uint32) (string, *uint32) {
	if actorUID == 0 {
		return "root", nil
	}
	uid := actorUID
	return fmt.Sprintf("uid:%d", actorUID), &uid
}

func (b *Bridge) publishSurfaceActionDenied(actorUID uint32, action, surfaceID string, err error) {
	if b.bus == nil {
		return
	}
	actor, uid := b.surfaceActionActor(actorUID)
	_, message := classifyError(err)
	result := schema.SurfaceActionResponse{
		Action: action, SurfaceID: surfaceID, Decision: schema.SurfaceActionDenied,
		Reason: message, Error: message, Actor: actor, ActorUID: uid,
	}
	if err := b.bus.Publish(schema.TopicShellActionDenied, result); err != nil {
		log.Printf("publish shell action denied: %v", err)
	}
}

func (b *Bridge) publishSurfaceMoveDenied(actorUID uint32, req schema.MoveSurfaceRequest, err error, current *schema.SurfaceGeometry) {
	if b.bus == nil {
		return
	}
	actor, uid := b.surfaceActionActor(actorUID)
	_, message := classifyError(err)
	target := schema.SurfaceGeometry{X: req.X, Y: req.Y, Width: req.Width, Height: req.Height}
	if target.Width <= 0 && current != nil {
		target.Width = current.Width
	}
	if target.Height <= 0 && current != nil {
		target.Height = current.Height
	}
	result := schema.SurfaceActionResponse{
		Action: "surface.move", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied,
		Reason: message, Error: message, Actor: actor, ActorUID: uid, TargetGeometry: &target,
	}
	if current != nil {
		copy := *current
		result.ResultGeometry = &copy
	}
	if err := b.bus.Publish(schema.TopicShellActionDenied, result); err != nil {
		log.Printf("publish shell action denied: %v", err)
	}
}

const (
	minSurfaceResizeWidth  = 160
	minSurfaceResizeHeight = 120
	maxSurfaceResizeWidth  = 10000
	maxSurfaceResizeHeight = 10000
)

func (b *Bridge) publishSurfaceResizeDenied(actorUID uint32, req schema.ResizeSurfaceRequest, err error, current *schema.SurfaceGeometry) {
	if b.bus == nil {
		return
	}
	actor, uid := b.surfaceActionActor(actorUID)
	_, message := classifyError(err)
	target := schema.SurfaceGeometry{Width: req.Width, Height: req.Height}
	if current != nil {
		target.X = current.X
		target.Y = current.Y
		copy := *current
		result := schema.SurfaceActionResponse{
			Action: "surface.resize", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied,
			Reason: message, Error: message, Actor: actor, ActorUID: uid, TargetGeometry: &target, ResultGeometry: &copy,
		}
		if err := b.bus.Publish(schema.TopicShellActionDenied, result); err != nil {
			log.Printf("publish shell action denied: %v", err)
		}
		return
	}
	result := schema.SurfaceActionResponse{
		Action: "surface.resize", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied,
		Reason: message, Error: message, Actor: actor, ActorUID: uid, TargetGeometry: &target,
	}
	if err := b.bus.Publish(schema.TopicShellActionDenied, result); err != nil {
		log.Printf("publish shell action denied: %v", err)
	}
}

func validateResizeGeometry(width, height int) error {
	if width < minSurfaceResizeWidth || height < minSurfaceResizeHeight {
		return compositorError(schema.ErrorInvalidCoordinates, "resize target %dx%d is below minimum %dx%d", width, height, minSurfaceResizeWidth, minSurfaceResizeHeight)
	}
	if width > maxSurfaceResizeWidth || height > maxSurfaceResizeHeight {
		return compositorError(schema.ErrorInvalidCoordinates, "resize target %dx%d exceeds maximum %dx%d", width, height, maxSurfaceResizeWidth, maxSurfaceResizeHeight)
	}
	return nil
}

func (b *Bridge) FocusSurface(actorUID uint32, req schema.FocusSurfaceRequest) (schema.SurfaceActionResponse, error) {
	if req.SurfaceID == "" {
		err := fmt.Errorf("surface_id is required")
		b.publishSurfaceActionDenied(actorUID, "surface.focus", req.SurfaceID, err)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	tracked, ok := b.surfaces[req.SurfaceID]
	b.mu.RUnlock()
	if !ok {
		b.mu.RLock()
		_, stale := b.staleSurfaces[req.SurfaceID]
		b.mu.RUnlock()
		if stale {
			err := compositorError(schema.ErrorSurfaceStale, "surface %s is unmapped/stale", req.SurfaceID)
			b.publishSurfaceActionDenied(actorUID, "surface.focus", req.SurfaceID, err)
			return schema.SurfaceActionResponse{}, err
		}
		err := compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
		b.publishSurfaceActionDenied(actorUID, "surface.focus", req.SurfaceID, err)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Surface.SurfaceKind == schema.SurfaceKindLayerShell {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s is a layer-shell surface and cannot be focused as a work surface", req.SurfaceID)
		b.publishSurfaceActionDenied(actorUID, "surface.focus", req.SurfaceID, err)
		return schema.SurfaceActionResponse{}, err
	}
	if !tracked.Visible {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s is not visible", req.SurfaceID)
		b.publishSurfaceActionDenied(actorUID, "surface.focus", req.SurfaceID, err)
		return schema.SurfaceActionResponse{}, err
	}

	b.mu.Lock()
	b.focusSeq++
	requestID := fmt.Sprintf("focus-%d-%d", time.Now().UnixNano(), b.focusSeq)
	waiter := make(chan schema.CompositorFocusPluginResponse, 1)
	b.focusWaiters[requestID] = waiter
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.focusWaiters, requestID)
		b.mu.Unlock()
	}()

	if err := b.sendToPlugin(schema.CompositorFocusSurface{Type: schema.PluginMessageFocusSurface, RequestID: requestID, SurfaceID: req.SurfaceID}); err != nil {
		b.publishSurfaceActionDenied(actorUID, "surface.focus", req.SurfaceID, err)
		return schema.SurfaceActionResponse{}, err
	}

	timeout := time.Duration(req.WaitTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	select {
	case resp := <-waiter:
		if !resp.OK {
			msg := resp.Error
			if msg == "" {
				msg = "focus rejected by compositor plugin"
			}
			err := classifyInputPluginError(msg)
			if strings.Contains(strings.ToLower(msg), "surface not found") {
				err = compositorError(schema.ErrorSurfaceStale, "surface %s is unmapped/stale", req.SurfaceID)
			}
			b.publishSurfaceActionDenied(actorUID, "surface.focus", req.SurfaceID, err)
			return schema.SurfaceActionResponse{}, err
		}
	case <-time.After(timeout):
		err := compositorError(schema.ErrorFrameTimeout, "focus request timed out")
		b.publishSurfaceActionDenied(actorUID, "surface.focus", req.SurfaceID, err)
		return schema.SurfaceActionResponse{}, err
	}

	b.mu.RLock()
	readback, ok := b.surfaces[req.SurfaceID]
	if ok {
		readback = b.decorateSurfaceLocked(readback)
	}
	b.mu.RUnlock()
	if !ok {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s disappeared after focus", req.SurfaceID)
		b.publishSurfaceActionDenied(actorUID, "surface.focus", req.SurfaceID, err)
		return schema.SurfaceActionResponse{}, err
	}

	actor, uid := b.surfaceActionActor(actorUID)
	result := schema.SurfaceActionResponse{
		Action: "surface.focus", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionAccepted,
		Reason: "focused via compositor plugin", FocusedSurfaceID: req.SurfaceID, Actor: actor, ActorUID: uid, Surface: &readback,
	}
	if b.bus != nil {
		if err := b.bus.Publish(schema.TopicShellActionCompleted, result); err != nil {
			log.Printf("publish shell action completed: %v", err)
		}
	}
	return result, nil
}

func (b *Bridge) RaiseSurfaceAction(actorUID uint32, req schema.RaiseSurfaceRequest) (schema.SurfaceActionResponse, error) {
	if req.SurfaceID == "" {
		err := fmt.Errorf("surface_id is required")
		b.publishRaiseDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	if req.Mode != "" && req.Mode != "no-focus" {
		err := fmt.Errorf("surface raise only supports mode no-focus")
		b.publishRaiseDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	tracked, ok := b.surfaces[req.SurfaceID]
	focusedBefore := ""
	for _, s := range b.surfaces {
		if s.Focused {
			focusedBefore = s.Surface.ID
			break
		}
	}
	b.mu.RUnlock()
	if !ok {
		b.mu.RLock()
		_, stale := b.staleSurfaces[req.SurfaceID]
		b.mu.RUnlock()
		var err error
		if stale {
			err = compositorError(schema.ErrorSurfaceStale, "surface %s is unmapped/stale", req.SurfaceID)
		} else {
			err = compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
		}
		b.publishRaiseDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Surface.SurfaceKind == schema.SurfaceKindLayerShell || (tracked.Surface.SurfaceKind != "" && tracked.Surface.SurfaceKind != schema.SurfaceKindXDGView) || (tracked.Surface.Role != "" && tracked.Surface.Role != "toplevel") {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s is not an xdg toplevel and cannot be raised as a work surface", req.SurfaceID)
		b.publishRaiseDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if !tracked.Visible {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s is not visible", req.SurfaceID)
		b.publishRaiseDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if (tracked.Surface.Minimized != nil && *tracked.Surface.Minimized) || tracked.Surface.VisibilityState == "minimized" {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s is minimized and must be restored before it can be raised", req.SurfaceID)
		b.publishRaiseDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Surface.Fullscreen != nil && *tracked.Surface.Fullscreen {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s is fullscreen and has no meaningful scoped stack raise", req.SurfaceID)
		b.publishRaiseDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.OutputID == "" && tracked.Surface.OutputID == "" {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s has no output for scoped stack raise", req.SurfaceID)
		b.publishRaiseDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Surface.Workspace == nil {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s has no workspace for scoped stack raise", req.SurfaceID)
		b.publishRaiseDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	beforeStack := stackStateFromSurface(tracked.Surface)
	if err := b.raiseSurfaceNoFocus(req.SurfaceID, time.Duration(req.WaitTimeoutMs)*time.Millisecond); err != nil {
		b.publishRaiseDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	updated, ok := b.surfaces[req.SurfaceID]
	focusedAfter := ""
	for _, s := range b.surfaces {
		if s.Focused {
			focusedAfter = s.Surface.ID
			break
		}
	}
	if ok {
		updated = b.decorateSurfaceLocked(updated)
	}
	b.mu.RUnlock()
	if !ok {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s disappeared after raise", req.SurfaceID)
		b.publishRaiseDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if focusedBefore != focusedAfter {
		err := compositorError(schema.ErrorProtocolError, "surface raise changed focus from %s to %s", focusedBefore, focusedAfter)
		b.publishRaiseDenied(actorUID, req, err, &updated)
		return schema.SurfaceActionResponse{}, err
	}
	if updated.Surface.IsTopInStack == nil || !*updated.Surface.IsTopInStack {
		err := compositorError(schema.ErrorFrameTimeout, "surface %s raise readback did not become top in scoped stack", req.SurfaceID)
		b.publishRaiseDenied(actorUID, req, err, &updated)
		return schema.SurfaceActionResponse{}, err
	}
	actor, uid := b.surfaceActionActor(actorUID)
	resultStack := stackStateFromSurface(updated.Surface)
	result := schema.SurfaceActionResponse{Action: "surface.raise", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionAccepted, Reason: "no-focus raise accepted via Wayfire view_bring_to_front", Actor: actor, ActorUID: uid, FocusedSurfaceID: focusedAfter, TargetState: &schema.SurfaceState{Stack: beforeStack}, ResultState: &schema.SurfaceState{Stack: resultStack}, Surface: &updated}
	if b.bus != nil {
		if err := b.bus.Publish(schema.TopicShellActionCompleted, result); err != nil {
			log.Printf("publish shell action completed: %v", err)
		}
	}
	return result, nil
}

func stackStateFromSurface(surface schema.CompositorSurface) *schema.CompositorStackState {
	return &schema.CompositorStackState{OutputID: surface.OutputID, Workspace: surface.Workspace, StackLayer: surface.StackLayer, StackIndex: surface.StackIndex, StackCount: surface.StackCount, IsTopInStack: surface.IsTopInStack, ZOrderGeneration: surface.ZOrderGeneration}
}

func (b *Bridge) publishRaiseDenied(actorUID uint32, req schema.RaiseSurfaceRequest, err error, surface *schema.CompositorTrackedSurface) {
	actor, uid := b.surfaceActionActor(actorUID)
	result := schema.SurfaceActionResponse{Action: "surface.raise", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied, Reason: err.Error(), Error: err.Error(), Actor: actor, ActorUID: uid, Surface: surface}
	if b.bus != nil {
		if publishErr := b.bus.Publish(schema.TopicShellActionDenied, result); publishErr != nil {
			log.Printf("publish shell action denied: %v", publishErr)
		}
	}
}

func (b *Bridge) raiseSurfaceNoFocus(surfaceID string, timeout time.Duration) error {
	b.mu.Lock()
	if b.plugin == nil {
		b.mu.Unlock()
		return compositorError(schema.ErrorCompositorUnavailable, "no plugin connected")
	}
	b.raiseSeq++
	requestID := fmt.Sprintf("raise-%d-%d", time.Now().UnixNano(), b.raiseSeq)
	ch := make(chan schema.CompositorRaisePluginResponse, 1)
	b.raiseWaiters[requestID] = ch
	session := b.plugin
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.raiseWaiters, requestID)
		b.mu.Unlock()
	}()
	if err := session.Send(schema.CompositorRaiseSurface{Type: schema.PluginMessageRaiseSurface, RequestID: requestID, SurfaceID: surfaceID, Mode: "no-focus"}); err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	deadline := time.After(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	pluginAcked := false
	for {
		select {
		case resp := <-ch:
			if !resp.OK {
				if resp.Error == "" {
					resp.Error = "raise failed"
				}
				return compositorError(schema.ErrorProtocolError, "surface raise failed: %s", resp.Error)
			}
			pluginAcked = true
			if b.surfaceIsTopInScopedStack(surfaceID) {
				return nil
			}
		case <-ticker.C:
			if b.surfaceIsTopInScopedStack(surfaceID) {
				return nil
			}
		case <-deadline:
			if pluginAcked {
				return compositorError(schema.ErrorFrameTimeout, "surface raise stack readback timed out after plugin ack")
			}
			return compositorError(schema.ErrorFrameTimeout, "surface raise plugin acknowledgement timed out")
		}
	}
}

func (b *Bridge) surfaceIsTopInScopedStack(surfaceID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	tracked, ok := b.surfaces[surfaceID]
	return ok && tracked.Surface.IsTopInStack != nil && *tracked.Surface.IsTopInStack
}

func (b *Bridge) MoveSurfaceAction(actorUID uint32, req schema.MoveSurfaceRequest) (schema.SurfaceActionResponse, error) {
	if req.SurfaceID == "" {
		err := fmt.Errorf("surface_id is required")
		b.publishSurfaceMoveDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	tracked, ok := b.surfaces[req.SurfaceID]
	b.mu.RUnlock()
	if !ok {
		b.mu.RLock()
		_, stale := b.staleSurfaces[req.SurfaceID]
		b.mu.RUnlock()
		if stale {
			err := compositorError(schema.ErrorSurfaceStale, "surface %s is unmapped/stale", req.SurfaceID)
			b.publishSurfaceMoveDenied(actorUID, req, err, nil)
			return schema.SurfaceActionResponse{}, err
		}
		err := compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
		b.publishSurfaceMoveDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Surface.SurfaceKind == schema.SurfaceKindLayerShell {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s is a layer-shell surface and cannot be moved as a work surface", req.SurfaceID)
		b.publishSurfaceMoveDenied(actorUID, req, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	if !tracked.Visible {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s is not visible", req.SurfaceID)
		b.publishSurfaceMoveDenied(actorUID, req, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	geom := schema.SurfaceGeometry{X: req.X, Y: req.Y, Width: req.Width, Height: req.Height}
	if geom.Width <= 0 || geom.Height <= 0 {
		if tracked.Geometry != nil {
			if geom.Width <= 0 {
				geom.Width = tracked.Geometry.Width
			}
			if geom.Height <= 0 {
				geom.Height = tracked.Geometry.Height
			}
		}
		if geom.Width <= 0 && tracked.Surface.Geometry != nil {
			geom.Width = tracked.Surface.Geometry.Width
		}
		if geom.Height <= 0 && tracked.Surface.Geometry != nil {
			geom.Height = tracked.Surface.Geometry.Height
		}
	}
	if geom.Width <= 0 || geom.Height <= 0 {
		err := compositorError(schema.ErrorInvalidCoordinates, "surface %s has no known size for move", req.SurfaceID)
		b.publishSurfaceMoveDenied(actorUID, req, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	if err := b.placeSurface(req.SurfaceID, geom, time.Duration(req.WaitTimeoutMs)*time.Millisecond, true); err != nil {
		b.publishSurfaceMoveDenied(actorUID, req, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	updated, ok := b.surfaces[req.SurfaceID]
	if ok {
		updated = b.decorateSurfaceLocked(updated)
	}
	b.mu.RUnlock()
	if !ok {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s disappeared after move", req.SurfaceID)
		b.publishSurfaceMoveDenied(actorUID, req, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	if updated.Geometry == nil {
		err := compositorError(schema.ErrorFrameTimeout, "surface %s move produced no compositor geometry readback", req.SurfaceID)
		b.publishSurfaceMoveDenied(actorUID, req, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	resultGeom := *updated.Geometry
	actor, uid := b.surfaceActionActor(actorUID)
	result := schema.SurfaceActionResponse{
		Action: "surface.move", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionAccepted,
		Reason: "moved via compositor plugin", Actor: actor, ActorUID: uid,
		TargetGeometry: &geom, ResultGeometry: &resultGeom, Surface: &updated,
	}
	if b.bus != nil {
		if err := b.bus.Publish(schema.TopicShellActionCompleted, result); err != nil {
			log.Printf("publish shell action completed: %v", err)
		}
	}
	return result, nil
}

func (b *Bridge) ResizeSurfaceAction(actorUID uint32, req schema.ResizeSurfaceRequest) (schema.SurfaceActionResponse, error) {
	if req.SurfaceID == "" {
		err := fmt.Errorf("surface_id is required")
		b.publishSurfaceResizeDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	tracked, ok := b.surfaces[req.SurfaceID]
	b.mu.RUnlock()
	if !ok {
		b.mu.RLock()
		_, stale := b.staleSurfaces[req.SurfaceID]
		b.mu.RUnlock()
		if stale {
			err := compositorError(schema.ErrorSurfaceStale, "surface %s is unmapped/stale", req.SurfaceID)
			b.publishSurfaceResizeDenied(actorUID, req, err, nil)
			return schema.SurfaceActionResponse{}, err
		}
		err := compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
		b.publishSurfaceResizeDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Surface.SurfaceKind == schema.SurfaceKindLayerShell {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s is a layer-shell surface and cannot be resized as a work surface", req.SurfaceID)
		b.publishSurfaceResizeDenied(actorUID, req, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	if !tracked.Visible {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s is not visible", req.SurfaceID)
		b.publishSurfaceResizeDenied(actorUID, req, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	if err := validateResizeGeometry(req.Width, req.Height); err != nil {
		b.publishSurfaceResizeDenied(actorUID, req, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Geometry == nil {
		err := compositorError(schema.ErrorInvalidCoordinates, "surface %s has no known geometry for resize", req.SurfaceID)
		b.publishSurfaceResizeDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	geom := schema.SurfaceGeometry{X: tracked.Geometry.X, Y: tracked.Geometry.Y, Width: req.Width, Height: req.Height}

	if err := b.placeSurface(req.SurfaceID, geom, time.Duration(req.WaitTimeoutMs)*time.Millisecond, true); err != nil {
		b.publishSurfaceResizeDenied(actorUID, req, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	updated, ok := b.surfaces[req.SurfaceID]
	if ok {
		updated = b.decorateSurfaceLocked(updated)
	}
	b.mu.RUnlock()
	if !ok {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s disappeared after resize", req.SurfaceID)
		b.publishSurfaceResizeDenied(actorUID, req, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	if updated.Geometry == nil {
		err := compositorError(schema.ErrorFrameTimeout, "surface %s resize produced no compositor geometry readback", req.SurfaceID)
		b.publishSurfaceResizeDenied(actorUID, req, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	resultGeom := *updated.Geometry
	actor, uid := b.surfaceActionActor(actorUID)
	result := schema.SurfaceActionResponse{
		Action: "surface.resize", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionAccepted,
		Reason: "resized via compositor plugin", Actor: actor, ActorUID: uid,
		TargetGeometry: &geom, ResultGeometry: &resultGeom, Surface: &updated,
	}
	if b.bus != nil {
		if err := b.bus.Publish(schema.TopicShellActionCompleted, result); err != nil {
			log.Printf("publish shell action completed: %v", err)
		}
	}
	return result, nil
}

func normalizeTileRequest(req schema.TileSurfaceRequest) (schema.TileSurfaceRequest, error) {
	if req.SurfaceID == "" {
		return req, fmt.Errorf("surface_id is required")
	}
	if req.Rows == 0 {
		req.Rows = 2
	}
	if req.Cols == 0 {
		req.Cols = 2
	}
	if req.RowSpan == 0 {
		req.RowSpan = 1
	}
	if req.ColSpan == 0 {
		req.ColSpan = 1
	}
	if req.Rows != 2 || req.Cols != 2 {
		return req, compositorError(schema.ErrorBackendUnsupported, "surface.tile currently supports the 2x2 grid only")
	}
	if req.Row < 0 || req.Col < 0 || req.RowSpan <= 0 || req.ColSpan <= 0 || req.Row+req.RowSpan > req.Rows || req.Col+req.ColSpan > req.Cols {
		return req, compositorError(schema.ErrorInvalidCoordinates, "invalid tile region row=%d col=%d row_span=%d col_span=%d for %dx%d grid", req.Row, req.Col, req.RowSpan, req.ColSpan, req.Rows, req.Cols)
	}
	return req, nil
}

func (b *Bridge) tileCellGeometry(req schema.TileSurfaceRequest) schema.SurfaceGeometry {
	b.mu.RLock()
	physicalW, physicalH := b.physicalBoundsLocked()
	b.mu.RUnlock()
	cellW, cellH := physicalW/req.Cols, physicalH/req.Rows
	if cellW <= 0 {
		cellW = 960
	}
	if cellH <= 0 {
		cellH = 540
	}
	return schema.SurfaceGeometry{X: req.Col * cellW, Y: req.Row * cellH, Width: req.ColSpan * cellW, Height: req.RowSpan * cellH}
}

func centeredTileGeometry(cell schema.SurfaceGeometry, current *schema.SurfaceGeometry) schema.SurfaceGeometry {
	if current == nil || current.Width <= 0 || current.Height <= 0 {
		return cell
	}
	width, height := current.Width, current.Height
	if width > cell.Width {
		width = cell.Width
	}
	if height > cell.Height {
		height = cell.Height
	}
	return schema.SurfaceGeometry{X: cell.X + (cell.Width-width)/2, Y: cell.Y + (cell.Height-height)/2, Width: width, Height: height}
}

func (b *Bridge) publishSurfaceTileDenied(actorUID uint32, req schema.TileSurfaceRequest, target schema.SurfaceGeometry, err error, current *schema.SurfaceGeometry) {
	if b.bus == nil {
		return
	}
	actor, uid := b.surfaceActionActor(actorUID)
	_, message := classifyError(err)
	result := schema.SurfaceActionResponse{Action: "surface.tile", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied, Reason: message, Error: message, Actor: actor, ActorUID: uid, TargetGeometry: &target}
	if current != nil {
		copy := *current
		result.ResultGeometry = &copy
	}
	if err := b.bus.Publish(schema.TopicShellActionDenied, result); err != nil {
		log.Printf("publish shell action denied: %v", err)
	}
}

func (b *Bridge) TileSurfaceAction(actorUID uint32, req schema.TileSurfaceRequest) (schema.SurfaceActionResponse, error) {
	normalized, err := normalizeTileRequest(req)
	if err != nil {
		target := schema.SurfaceGeometry{}
		if normalized.Rows > 0 && normalized.Cols > 0 {
			target = b.tileCellGeometry(normalized)
		}
		b.publishSurfaceTileDenied(actorUID, normalized, target, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	req = normalized
	cell := b.tileCellGeometry(req)
	b.mu.RLock()
	tracked, ok := b.surfaces[req.SurfaceID]
	b.mu.RUnlock()
	target := centeredTileGeometry(cell, tracked.Geometry)
	if !ok {
		b.mu.RLock()
		_, stale := b.staleSurfaces[req.SurfaceID]
		b.mu.RUnlock()
		if stale {
			err := compositorError(schema.ErrorSurfaceStale, "surface %s is unmapped/stale", req.SurfaceID)
			b.publishSurfaceTileDenied(actorUID, req, target, err, nil)
			return schema.SurfaceActionResponse{}, err
		}
		err := compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
		b.publishSurfaceTileDenied(actorUID, req, target, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Surface.SurfaceKind == schema.SurfaceKindLayerShell {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s is a layer-shell surface and cannot be tiled as a work surface", req.SurfaceID)
		b.publishSurfaceTileDenied(actorUID, req, target, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	if !tracked.Visible {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s is not visible", req.SurfaceID)
		b.publishSurfaceTileDenied(actorUID, req, target, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	if err := b.tileSurface(req.SurfaceID, target, time.Duration(req.WaitTimeoutMs)*time.Millisecond); err != nil {
		b.publishSurfaceTileDenied(actorUID, req, target, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	updated, ok := b.surfaces[req.SurfaceID]
	if ok {
		updated = b.decorateSurfaceLocked(updated)
	}
	b.mu.RUnlock()
	if !ok {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s disappeared after tile", req.SurfaceID)
		b.publishSurfaceTileDenied(actorUID, req, target, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	if updated.Geometry == nil {
		err := compositorError(schema.ErrorFrameTimeout, "surface %s tile produced no compositor geometry readback", req.SurfaceID)
		b.publishSurfaceTileDenied(actorUID, req, target, err, tracked.Geometry)
		return schema.SurfaceActionResponse{}, err
	}
	resultGeom := *updated.Geometry
	actor, uid := b.surfaceActionActor(actorUID)
	result := schema.SurfaceActionResponse{Action: "surface.tile", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionAccepted, Reason: "tiled via compositor window manager", Actor: actor, ActorUID: uid, TargetGeometry: &target, ResultGeometry: &resultGeom, Surface: &updated}
	if b.bus != nil {
		if err := b.bus.Publish(schema.TopicShellActionCompleted, result); err != nil {
			log.Printf("publish shell action completed: %v", err)
		}
	}
	return result, nil
}

func (b *Bridge) tileSurface(surfaceID string, target schema.SurfaceGeometry, timeout time.Duration) error {
	return b.placeSurface(surfaceID, target, timeout, true)
}

func (b *Bridge) SetViewProperty(req schema.SetViewPropertyRequest) error {
	if req.SurfaceID == "" {
		return fmt.Errorf("surface_id is required")
	}
	if len(req.Properties) == 0 {
		return fmt.Errorf("at least one property is required")
	}
	if _, ok := req.Properties["always_on_top"]; !ok {
		return fmt.Errorf("unsupported view properties: only always_on_top is currently supported")
	}
	if len(req.Properties) != 1 {
		return fmt.Errorf("unsupported view properties: only always_on_top is currently supported")
	}
	if _, ok := req.Properties["always_on_top"].(bool); !ok {
		return fmt.Errorf("always_on_top must be a boolean")
	}

	b.mu.RLock()
	_, tracked := b.surfaces[req.SurfaceID]
	b.mu.RUnlock()
	if !tracked {
		return compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
	}

	return b.sendToPlugin(schema.CompositorSetViewProperty{
		Type:       schema.PluginMessageSetViewProperty,
		SurfaceID:  req.SurfaceID,
		Properties: req.Properties,
	})
}

func (b *Bridge) AlwaysOnTopAction(actorUID uint32, req schema.AlwaysOnTopRequest) (schema.SurfaceActionResponse, error) {
	if req.SurfaceID == "" {
		err := fmt.Errorf("surface_id is required")
		b.publishAlwaysOnTopDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	tracked, ok := b.surfaces[req.SurfaceID]
	b.mu.RUnlock()
	if !ok {
		b.mu.RLock()
		_, stale := b.staleSurfaces[req.SurfaceID]
		b.mu.RUnlock()
		var err error
		if stale {
			err = compositorError(schema.ErrorSurfaceStale, "surface %s is unmapped/stale", req.SurfaceID)
		} else {
			err = compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
		}
		b.publishAlwaysOnTopDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Surface.SurfaceKind == schema.SurfaceKindLayerShell {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s is a layer-shell surface and cannot be raised as a work surface", req.SurfaceID)
		b.publishAlwaysOnTopDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if !tracked.Visible {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s is not visible", req.SurfaceID)
		b.publishAlwaysOnTopDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if err := b.setViewAlwaysOnTop(req.SurfaceID, req.Enabled, time.Duration(req.WaitTimeoutMs)*time.Millisecond); err != nil {
		b.publishAlwaysOnTopDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	updated, ok := b.surfaces[req.SurfaceID]
	if ok {
		updated = b.decorateSurfaceLocked(updated)
	}
	b.mu.RUnlock()
	if !ok {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s disappeared after always_on_top", req.SurfaceID)
		b.publishAlwaysOnTopDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if updated.Surface.AlwaysOnTop == nil || *updated.Surface.AlwaysOnTop != req.Enabled {
		err := compositorError(schema.ErrorFrameTimeout, "surface %s always_on_top readback did not converge", req.SurfaceID)
		b.publishAlwaysOnTopDenied(actorUID, req, err, &updated)
		return schema.SurfaceActionResponse{}, err
	}
	actor, uid := b.surfaceActionActor(actorUID)
	value := req.Enabled
	state := schema.SurfaceState{AlwaysOnTop: &value}
	result := schema.SurfaceActionResponse{Action: "surface.always_on_top", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionAccepted, Reason: "always_on_top updated via compositor plugin", Actor: actor, ActorUID: uid, TargetState: &state, ResultState: &state, AlwaysOnTop: &value, Surface: &updated}
	if b.bus != nil {
		if err := b.bus.Publish(schema.TopicShellActionCompleted, result); err != nil {
			log.Printf("publish shell action completed: %v", err)
		}
	}
	return result, nil
}

func (b *Bridge) publishAlwaysOnTopDenied(actorUID uint32, req schema.AlwaysOnTopRequest, err error, surface *schema.CompositorTrackedSurface) {
	actor, uid := b.surfaceActionActor(actorUID)
	value := req.Enabled
	state := schema.SurfaceState{AlwaysOnTop: &value}
	result := schema.SurfaceActionResponse{Action: "surface.always_on_top", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied, Reason: err.Error(), Error: err.Error(), Actor: actor, ActorUID: uid, TargetState: &state, AlwaysOnTop: &value, Surface: surface}
	if b.bus != nil {
		if publishErr := b.bus.Publish(schema.TopicShellActionDenied, result); publishErr != nil {
			log.Printf("publish shell action denied: %v", publishErr)
		}
	}
}

func (b *Bridge) setViewAlwaysOnTop(surfaceID string, enabled bool, timeout time.Duration) error {
	b.mu.Lock()
	if b.plugin == nil {
		b.mu.Unlock()
		return compositorError(schema.ErrorCompositorUnavailable, "no plugin connected")
	}
	b.propertySeq++
	requestID := fmt.Sprintf("property-%d-%d", time.Now().UnixNano(), b.propertySeq)
	ch := make(chan schema.CompositorPropertyPluginResponse, 1)
	b.propertyWaiters[requestID] = ch
	session := b.plugin
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.propertyWaiters, requestID)
		b.mu.Unlock()
	}()
	if err := session.Send(schema.CompositorSetViewProperty{Type: schema.PluginMessageSetViewProperty, RequestID: requestID, SurfaceID: surfaceID, Properties: map[string]any{"always_on_top": enabled}}); err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	deadline := time.After(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	pluginAcked := false
	for {
		select {
		case resp := <-ch:
			if !resp.OK {
				if resp.Error == "" {
					resp.Error = "property update failed"
				}
				return compositorError(schema.ErrorProtocolError, "set always_on_top failed: %s", resp.Error)
			}
			pluginAcked = true
			if b.surfaceAlwaysOnTopMatches(surfaceID, enabled) {
				return nil
			}
		case <-ticker.C:
			if b.surfaceAlwaysOnTopMatches(surfaceID, enabled) {
				return nil
			}
		case <-deadline:
			if pluginAcked {
				return compositorError(schema.ErrorFrameTimeout, "always_on_top readback timed out after plugin ack")
			}
			return compositorError(schema.ErrorFrameTimeout, "always_on_top plugin acknowledgement timed out")
		}
	}
}

func (b *Bridge) surfaceAlwaysOnTopMatches(surfaceID string, want bool) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	tracked, ok := b.surfaces[surfaceID]
	return ok && tracked.Surface.AlwaysOnTop != nil && *tracked.Surface.AlwaysOnTop == want
}

func (b *Bridge) FullscreenSurfaceAction(actorUID uint32, req schema.FullscreenSurfaceRequest) (schema.SurfaceActionResponse, error) {
	if req.SurfaceID == "" {
		err := fmt.Errorf("surface_id is required")
		b.publishFullscreenDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	tracked, ok := b.surfaces[req.SurfaceID]
	b.mu.RUnlock()
	if !ok {
		b.mu.RLock()
		_, stale := b.staleSurfaces[req.SurfaceID]
		b.mu.RUnlock()
		var err error
		if stale {
			err = compositorError(schema.ErrorSurfaceStale, "surface %s is unmapped/stale", req.SurfaceID)
		} else {
			err = compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
		}
		b.publishFullscreenDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Surface.SurfaceKind == schema.SurfaceKindLayerShell || (tracked.Surface.Role != "" && tracked.Surface.Role != "toplevel") {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s is not an xdg toplevel and cannot be fullscreened", req.SurfaceID)
		b.publishFullscreenDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if !tracked.Visible {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s is not visible", req.SurfaceID)
		b.publishFullscreenDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.OutputID == "" && tracked.Surface.OutputID == "" {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s has no output for fullscreen", req.SurfaceID)
		b.publishFullscreenDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if err := b.setSurfaceFullscreen(req.SurfaceID, req.Enabled, time.Duration(req.WaitTimeoutMs)*time.Millisecond); err != nil {
		b.publishFullscreenDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	updated, ok := b.surfaces[req.SurfaceID]
	if ok {
		updated = b.decorateSurfaceLocked(updated)
	}
	b.mu.RUnlock()
	if !ok {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s disappeared after fullscreen", req.SurfaceID)
		b.publishFullscreenDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if updated.Surface.Fullscreen == nil || *updated.Surface.Fullscreen != req.Enabled {
		err := compositorError(schema.ErrorFrameTimeout, "surface %s fullscreen readback did not converge", req.SurfaceID)
		b.publishFullscreenDenied(actorUID, req, err, &updated)
		return schema.SurfaceActionResponse{}, err
	}
	actor, uid := b.surfaceActionActor(actorUID)
	value := req.Enabled
	state := schema.SurfaceState{Fullscreen: &value}
	result := schema.SurfaceActionResponse{Action: "surface.fullscreen", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionAccepted, Reason: "fullscreen updated via compositor plugin", Actor: actor, ActorUID: uid, TargetState: &state, ResultState: &state, Fullscreen: &value, Surface: &updated}
	if b.bus != nil {
		if err := b.bus.Publish(schema.TopicShellActionCompleted, result); err != nil {
			log.Printf("publish shell action completed: %v", err)
		}
	}
	return result, nil
}

func (b *Bridge) publishFullscreenDenied(actorUID uint32, req schema.FullscreenSurfaceRequest, err error, surface *schema.CompositorTrackedSurface) {
	actor, uid := b.surfaceActionActor(actorUID)
	value := req.Enabled
	state := schema.SurfaceState{Fullscreen: &value}
	result := schema.SurfaceActionResponse{Action: "surface.fullscreen", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied, Reason: err.Error(), Error: err.Error(), Actor: actor, ActorUID: uid, TargetState: &state, Fullscreen: &value, Surface: surface}
	if b.bus != nil {
		if publishErr := b.bus.Publish(schema.TopicShellActionDenied, result); publishErr != nil {
			log.Printf("publish shell action denied: %v", publishErr)
		}
	}
}

func (b *Bridge) setSurfaceFullscreen(surfaceID string, enabled bool, timeout time.Duration) error {
	if b.surfaceFullscreenMatches(surfaceID, enabled) {
		return nil
	}
	b.mu.Lock()
	if b.plugin == nil {
		b.mu.Unlock()
		return compositorError(schema.ErrorCompositorUnavailable, "no plugin connected")
	}
	b.stateSeq++
	requestID := fmt.Sprintf("state-%d-%d", time.Now().UnixNano(), b.stateSeq)
	ch := make(chan schema.CompositorSurfaceStatePluginResponse, 1)
	b.stateWaiters[requestID] = ch
	session := b.plugin
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.stateWaiters, requestID)
		b.mu.Unlock()
	}()
	if err := session.Send(schema.CompositorSetSurfaceState{Type: schema.PluginMessageSetSurfaceState, RequestID: requestID, SurfaceID: surfaceID, Fullscreen: &enabled}); err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	deadline := time.After(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	pluginAcked := false
	for {
		select {
		case resp := <-ch:
			if !resp.OK {
				if resp.Error == "" {
					resp.Error = "surface state update failed"
				}
				return compositorError(schema.ErrorProtocolError, "set fullscreen failed: %s", resp.Error)
			}
			pluginAcked = true
			if b.surfaceFullscreenMatches(surfaceID, enabled) {
				return nil
			}
		case <-ticker.C:
			if b.surfaceFullscreenMatches(surfaceID, enabled) {
				return nil
			}
		case <-deadline:
			if pluginAcked {
				return compositorError(schema.ErrorFrameTimeout, "fullscreen readback timed out after plugin ack")
			}
			return compositorError(schema.ErrorFrameTimeout, "fullscreen plugin acknowledgement timed out")
		}
	}
}

func (b *Bridge) surfaceFullscreenMatches(surfaceID string, want bool) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	tracked, ok := b.surfaces[surfaceID]
	return ok && tracked.Surface.Fullscreen != nil && *tracked.Surface.Fullscreen == want
}

const wayfireTiledEdgesAll uint32 = 15

var wayfireAllTiledEdges = []string{"top", "bottom", "left", "right"}

func maximizeTargetState(enabled bool) schema.SurfaceState {
	maximized := enabled
	return schema.SurfaceState{Maximized: &maximized, TiledEdges: tiledEdgesForMaximized(enabled)}
}

func tiledEdgesForMaximized(enabled bool) *schema.SurfaceTiledEdges {
	if enabled {
		return &schema.SurfaceTiledEdges{Bits: wayfireTiledEdgesAll, Edges: append([]string(nil), wayfireAllTiledEdges...)}
	}
	return &schema.SurfaceTiledEdges{Bits: 0}
}

func (b *Bridge) MaximizeSurfaceAction(actorUID uint32, req schema.MaximizeSurfaceRequest) (schema.SurfaceActionResponse, error) {
	if req.SurfaceID == "" {
		err := fmt.Errorf("surface_id is required")
		b.publishMaximizeDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	tracked, ok := b.surfaces[req.SurfaceID]
	b.mu.RUnlock()
	if !ok {
		b.mu.RLock()
		_, stale := b.staleSurfaces[req.SurfaceID]
		b.mu.RUnlock()
		var err error
		if stale {
			err = compositorError(schema.ErrorSurfaceStale, "surface %s is unmapped/stale", req.SurfaceID)
		} else {
			err = compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
		}
		b.publishMaximizeDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Surface.SurfaceKind == schema.SurfaceKindLayerShell || (tracked.Surface.Role != "" && tracked.Surface.Role != "toplevel") {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s is not an xdg toplevel and cannot be maximized", req.SurfaceID)
		b.publishMaximizeDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if !tracked.Visible {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s is not visible", req.SurfaceID)
		b.publishMaximizeDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if err := b.setSurfaceMaximized(req.SurfaceID, req.Enabled, time.Duration(req.WaitTimeoutMs)*time.Millisecond); err != nil {
		b.publishMaximizeDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	updated, ok := b.surfaces[req.SurfaceID]
	if ok {
		updated = b.decorateSurfaceLocked(updated)
	}
	b.mu.RUnlock()
	if !ok {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s disappeared after maximize", req.SurfaceID)
		b.publishMaximizeDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if updated.Surface.Maximized == nil || *updated.Surface.Maximized != req.Enabled || updated.Surface.TiledEdges == nil || updated.Surface.TiledEdges.Bits != tiledEdgesForMaximized(req.Enabled).Bits {
		err := compositorError(schema.ErrorFrameTimeout, "surface %s maximize readback did not converge", req.SurfaceID)
		b.publishMaximizeDenied(actorUID, req, err, &updated)
		return schema.SurfaceActionResponse{}, err
	}
	actor, uid := b.surfaceActionActor(actorUID)
	value := req.Enabled
	state := maximizeTargetState(value)
	result := schema.SurfaceActionResponse{Action: "surface.maximize", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionAccepted, Reason: "maximize updated via compositor plugin", Actor: actor, ActorUID: uid, TargetState: &state, ResultState: &state, Maximized: &value, TiledEdges: updated.Surface.TiledEdges, Surface: &updated}
	if b.bus != nil {
		if err := b.bus.Publish(schema.TopicShellActionCompleted, result); err != nil {
			log.Printf("publish shell action completed: %v", err)
		}
	}
	return result, nil
}

func (b *Bridge) publishMaximizeDenied(actorUID uint32, req schema.MaximizeSurfaceRequest, err error, surface *schema.CompositorTrackedSurface) {
	actor, uid := b.surfaceActionActor(actorUID)
	value := req.Enabled
	state := maximizeTargetState(value)
	result := schema.SurfaceActionResponse{Action: "surface.maximize", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied, Reason: err.Error(), Error: err.Error(), Actor: actor, ActorUID: uid, TargetState: &state, Maximized: &value, TiledEdges: state.TiledEdges, Surface: surface}
	if b.bus != nil {
		if publishErr := b.bus.Publish(schema.TopicShellActionDenied, result); publishErr != nil {
			log.Printf("publish shell action denied: %v", publishErr)
		}
	}
}

func (b *Bridge) setSurfaceMaximized(surfaceID string, enabled bool, timeout time.Duration) error {
	if b.surfaceMaximizedMatches(surfaceID, enabled) {
		return nil
	}
	b.mu.Lock()
	if b.plugin == nil {
		b.mu.Unlock()
		return compositorError(schema.ErrorCompositorUnavailable, "no plugin connected")
	}
	b.stateSeq++
	requestID := fmt.Sprintf("state-%d-%d", time.Now().UnixNano(), b.stateSeq)
	ch := make(chan schema.CompositorSurfaceStatePluginResponse, 1)
	b.stateWaiters[requestID] = ch
	session := b.plugin
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.stateWaiters, requestID)
		b.mu.Unlock()
	}()
	if err := session.Send(schema.CompositorSetSurfaceState{Type: schema.PluginMessageSetSurfaceState, RequestID: requestID, SurfaceID: surfaceID, Maximized: &enabled}); err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	deadline := time.After(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	pluginAcked := false
	for {
		select {
		case resp := <-ch:
			if !resp.OK {
				if resp.Error == "" {
					resp.Error = "surface state update failed"
				}
				return compositorError(schema.ErrorProtocolError, "set maximize failed: %s", resp.Error)
			}
			pluginAcked = true
			if b.surfaceMaximizedMatches(surfaceID, enabled) {
				return nil
			}
		case <-ticker.C:
			if b.surfaceMaximizedMatches(surfaceID, enabled) {
				return nil
			}
		case <-deadline:
			if pluginAcked {
				return compositorError(schema.ErrorFrameTimeout, "maximize readback timed out after plugin ack")
			}
			return compositorError(schema.ErrorFrameTimeout, "maximize plugin acknowledgement timed out")
		}
	}
}

func (b *Bridge) surfaceMaximizedMatches(surfaceID string, want bool) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	tracked, ok := b.surfaces[surfaceID]
	if !ok || tracked.Surface.Maximized == nil || *tracked.Surface.Maximized != want || tracked.Surface.TiledEdges == nil {
		return false
	}
	return tracked.Surface.TiledEdges.Bits == tiledEdgesForMaximized(want).Bits
}

func (b *Bridge) MinimizeSurfaceAction(actorUID uint32, req schema.MinimizeSurfaceRequest) (schema.SurfaceActionResponse, error) {
	if req.SurfaceID == "" {
		err := fmt.Errorf("surface_id is required")
		b.publishMinimizeDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	tracked, ok := b.surfaces[req.SurfaceID]
	b.mu.RUnlock()
	if !ok {
		b.mu.RLock()
		_, stale := b.staleSurfaces[req.SurfaceID]
		b.mu.RUnlock()
		var err error
		if stale {
			err = compositorError(schema.ErrorSurfaceStale, "surface %s is unmapped/stale", req.SurfaceID)
		} else {
			err = compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
		}
		b.publishMinimizeDenied(actorUID, req, err, nil)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Surface.SurfaceKind == schema.SurfaceKindLayerShell || (tracked.Surface.Role != "" && tracked.Surface.Role != "toplevel") {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s is not an xdg toplevel and cannot be minimized", req.SurfaceID)
		b.publishMinimizeDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if req.Enabled && !tracked.Visible {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s is not visible", req.SurfaceID)
		b.publishMinimizeDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if err := b.setSurfaceMinimized(req.SurfaceID, req.Enabled, time.Duration(req.WaitTimeoutMs)*time.Millisecond); err != nil {
		b.publishMinimizeDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	updated, ok := b.surfaces[req.SurfaceID]
	if ok {
		updated = b.decorateSurfaceLocked(updated)
	}
	b.mu.RUnlock()
	if !ok {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s disappeared after minimize", req.SurfaceID)
		b.publishMinimizeDenied(actorUID, req, err, &tracked)
		return schema.SurfaceActionResponse{}, err
	}
	if updated.Surface.Minimized == nil || *updated.Surface.Minimized != req.Enabled {
		err := compositorError(schema.ErrorFrameTimeout, "surface %s minimize readback did not converge", req.SurfaceID)
		b.publishMinimizeDenied(actorUID, req, err, &updated)
		return schema.SurfaceActionResponse{}, err
	}
	actor, uid := b.surfaceActionActor(actorUID)
	value := req.Enabled
	state := schema.SurfaceState{Minimized: &value}
	result := schema.SurfaceActionResponse{Action: "surface.minimize", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionAccepted, Reason: "minimize updated via compositor plugin", Actor: actor, ActorUID: uid, TargetState: &state, ResultState: &state, Minimized: &value, Surface: &updated}
	if b.bus != nil {
		if err := b.bus.Publish(schema.TopicShellActionCompleted, result); err != nil {
			log.Printf("publish shell action completed: %v", err)
		}
	}
	return result, nil
}

func (b *Bridge) publishMinimizeDenied(actorUID uint32, req schema.MinimizeSurfaceRequest, err error, surface *schema.CompositorTrackedSurface) {
	actor, uid := b.surfaceActionActor(actorUID)
	value := req.Enabled
	state := schema.SurfaceState{Minimized: &value}
	result := schema.SurfaceActionResponse{Action: "surface.minimize", SurfaceID: req.SurfaceID, Decision: schema.SurfaceActionDenied, Reason: err.Error(), Error: err.Error(), Actor: actor, ActorUID: uid, TargetState: &state, Minimized: &value, Surface: surface}
	if b.bus != nil {
		if publishErr := b.bus.Publish(schema.TopicShellActionDenied, result); publishErr != nil {
			log.Printf("publish shell action denied: %v", publishErr)
		}
	}
}

func (b *Bridge) setSurfaceMinimized(surfaceID string, enabled bool, timeout time.Duration) error {
	if b.surfaceMinimizedMatches(surfaceID, enabled) {
		return nil
	}
	b.mu.Lock()
	if b.plugin == nil {
		b.mu.Unlock()
		return compositorError(schema.ErrorCompositorUnavailable, "no plugin connected")
	}
	b.stateSeq++
	requestID := fmt.Sprintf("state-%d-%d", time.Now().UnixNano(), b.stateSeq)
	ch := make(chan schema.CompositorSurfaceStatePluginResponse, 1)
	b.stateWaiters[requestID] = ch
	session := b.plugin
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.stateWaiters, requestID)
		b.mu.Unlock()
	}()
	if err := session.Send(schema.CompositorSetSurfaceState{Type: schema.PluginMessageSetSurfaceState, RequestID: requestID, SurfaceID: surfaceID, Minimized: &enabled}); err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	deadline := time.After(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	pluginAcked := false
	for {
		select {
		case resp := <-ch:
			if !resp.OK {
				if resp.Error == "" {
					resp.Error = "surface state update failed"
				}
				return compositorError(schema.ErrorProtocolError, "set minimize failed: %s", resp.Error)
			}
			pluginAcked = true
			if b.surfaceMinimizedMatches(surfaceID, enabled) {
				return nil
			}
		case <-ticker.C:
			if b.surfaceMinimizedMatches(surfaceID, enabled) {
				return nil
			}
		case <-deadline:
			if pluginAcked {
				return compositorError(schema.ErrorFrameTimeout, "minimize readback timed out after plugin ack")
			}
			return compositorError(schema.ErrorFrameTimeout, "minimize plugin acknowledgement timed out")
		}
	}
}

func (b *Bridge) surfaceMinimizedMatches(surfaceID string, want bool) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	tracked, ok := b.surfaces[surfaceID]
	return ok && tracked.Surface.Minimized != nil && *tracked.Surface.Minimized == want
}

func (b *Bridge) CloseSurface(surfaceID string) error {
	return b.sendToPlugin(schema.CompositorCloseSurface{
		Type:      schema.PluginMessageCloseSurface,
		SurfaceID: surfaceID,
	})
}

func (b *Bridge) CloseSurfaceAction(actorUID uint32, req schema.CloseSurfaceRequest) (schema.SurfaceActionResponse, error) {
	if req.SurfaceID == "" {
		err := fmt.Errorf("surface_id is required")
		b.publishSurfaceActionDenied(actorUID, "surface.close", req.SurfaceID, err)
		return schema.SurfaceActionResponse{}, err
	}
	b.mu.RLock()
	tracked, ok := b.surfaces[req.SurfaceID]
	b.mu.RUnlock()
	if !ok {
		b.mu.RLock()
		_, stale := b.staleSurfaces[req.SurfaceID]
		b.mu.RUnlock()
		if stale {
			err := compositorError(schema.ErrorSurfaceStale, "surface %s is unmapped/stale", req.SurfaceID)
			b.publishSurfaceActionDenied(actorUID, "surface.close", req.SurfaceID, err)
			return schema.SurfaceActionResponse{}, err
		}
		err := compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
		b.publishSurfaceActionDenied(actorUID, "surface.close", req.SurfaceID, err)
		return schema.SurfaceActionResponse{}, err
	}
	if tracked.Surface.SurfaceKind == schema.SurfaceKindLayerShell {
		err := compositorError(schema.ErrorBackendUnsupported, "surface %s is a layer-shell surface and cannot be closed as a work surface", req.SurfaceID)
		b.publishSurfaceActionDenied(actorUID, "surface.close", req.SurfaceID, err)
		return schema.SurfaceActionResponse{}, err
	}
	if !tracked.Visible {
		err := compositorError(schema.ErrorSurfaceStale, "surface %s is not visible", req.SurfaceID)
		b.publishSurfaceActionDenied(actorUID, "surface.close", req.SurfaceID, err)
		return schema.SurfaceActionResponse{}, err
	}

	if err := b.CloseSurface(req.SurfaceID); err != nil {
		b.publishSurfaceActionDenied(actorUID, "surface.close", req.SurfaceID, err)
		return schema.SurfaceActionResponse{}, err
	}
	actor, uid := b.surfaceActionActor(actorUID)
	readback := b.decorateSurfaceLockedCopy(req.SurfaceID)
	result := schema.SurfaceActionResponse{
		Action: "surface.close", SurfaceID: req.SurfaceID, ClosedSurfaceID: req.SurfaceID, Decision: schema.SurfaceActionAccepted,
		Reason: "close queued via compositor plugin", Queued: true, Actor: actor, ActorUID: uid, Surface: readback,
	}
	if b.bus != nil {
		if err := b.bus.Publish(schema.TopicShellActionCompleted, result); err != nil {
			log.Printf("publish shell action completed: %v", err)
		}
	}
	return result, nil
}

func (b *Bridge) decorateSurfaceLockedCopy(surfaceID string) *schema.CompositorTrackedSurface {
	b.mu.RLock()
	defer b.mu.RUnlock()
	tracked, ok := b.surfaces[surfaceID]
	if !ok {
		return nil
	}
	decorated := b.decorateSurfaceLocked(tracked)
	return &decorated
}

func (b *Bridge) CloseSurfacesByUID(ownerUID uint32) (int, error) {
	b.mu.RLock()
	queued := 0
	for _, surface := range b.surfaces {
		if surface.Client.UID == ownerUID {
			queued++
		}
	}
	b.mu.RUnlock()

	if queued == 0 {
		return 0, nil
	}

	if err := b.sendToPlugin(schema.CompositorCloseSurfacesByUID{
		Type:     schema.PluginMessageCloseSurfacesByUID,
		OwnerUID: ownerUID,
	}); err != nil {
		return 0, err
	}
	return queued, nil
}

// GrantViewport records the operator approval immediately; if syncing the derived policy to the plugin fails, the grant remains durable in memory and the append-only log and will be re-sent on the next plugin reconnect.
func (b *Bridge) GrantViewport(grantedByUID uint32, req schema.ViewportGrantRequest) (schema.SurfaceAccessGrant, error) {
	actions := normalizeViewportActions(req.Actions)
	if len(actions) == 0 {
		return schema.SurfaceAccessGrant{}, fmt.Errorf("at least one valid viewport action is required")
	}

	record := newGrantRecord(schema.GrantRecordGrant, req.SurfaceID, req.AgentUID, grantedByUID, actions)
	grant := schema.SurfaceAccessGrant{
		SurfaceID:    req.SurfaceID,
		AgentUID:     req.AgentUID,
		Actions:      record.Actions,
		GrantedByUID: record.GrantedByUID,
		GrantedAt:    record.RecordedAt,
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.surfaces[req.SurfaceID]; !ok {
		return schema.SurfaceAccessGrant{}, fmt.Errorf("surface %s not found", req.SurfaceID)
	}
	if err := b.grantStore.Append(record); err != nil {
		return schema.SurfaceAccessGrant{}, err
	}
	byAgent, ok := b.grants[req.SurfaceID]
	if !ok {
		byAgent = make(map[uint32]schema.SurfaceAccessGrant)
		b.grants[req.SurfaceID] = byAgent
	}
	byAgent[req.AgentUID] = grant

	if err := b.syncDerivedPolicyLocked(req.SurfaceID); err != nil {
		return schema.SurfaceAccessGrant{}, err
	}
	return grant, nil
}

func (b *Bridge) RevokeViewport(grantedByUID uint32, req schema.RevokeViewportGrantRequest) error {
	record := newGrantRecord(schema.GrantRecordRevoke, req.SurfaceID, req.AgentUID, grantedByUID, nil)

	b.mu.Lock()
	defer b.mu.Unlock()

	byAgent, ok := b.grants[req.SurfaceID]
	if !ok {
		return fmt.Errorf("no viewport grant for surface %s", req.SurfaceID)
	}
	if _, ok := byAgent[req.AgentUID]; !ok {
		return fmt.Errorf("no viewport grant for surface %s and agent uid %d", req.SurfaceID, req.AgentUID)
	}
	if err := b.grantStore.Append(record); err != nil {
		return err
	}
	delete(byAgent, req.AgentUID)
	if len(byAgent) == 0 {
		delete(b.grants, req.SurfaceID)
	}

	if _, ok := b.surfaces[req.SurfaceID]; ok {
		return b.syncDerivedPolicyLocked(req.SurfaceID)
	}
	return nil
}

func (b *Bridge) CheckSurfaceAccess(surfaceID string, agentUID uint32, action schema.CompositorAccessAction) schema.SurfaceAccessCheckResponse {
	b.mu.RLock()
	defer b.mu.RUnlock()

	allowed, reason := b.checkSurfaceAccessLocked(surfaceID, agentUID, action)
	return schema.SurfaceAccessCheckResponse{Allowed: allowed, Reason: reason}
}

func (b *Bridge) CreateOutput(req schema.CreateOutputRequest) (schema.LogicalOutput, error) {
	if req.Name == "" {
		return schema.LogicalOutput{}, fmt.Errorf("name is required")
	}
	if req.Width <= 0 {
		req.Width = 1280
	}
	if req.Height <= 0 {
		req.Height = 720
	}
	if req.Scale <= 0 {
		req.Scale = 1
	}
	now := time.Now()
	b.mu.Lock()
	out := b.outputs[req.Name]
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	out.Name, out.Width, out.Height, out.Scale, out.Mode = req.Name, req.Width, req.Height, req.Scale, "logical_physical_tile"
	out.UpdatedAt = now
	b.outputs[req.Name] = out
	b.layoutOutputsLocked()
	out = b.outputs[req.Name]
	b.mu.Unlock()
	if err := b.applyAllOutputAssignments(); err != nil {
		return schema.LogicalOutput{}, err
	}
	return out, nil
}

func (b *Bridge) DestroyOutput(name string) error {
	b.mu.Lock()
	if _, ok := b.outputs[name]; !ok {
		b.mu.Unlock()
		return fmt.Errorf("output %s not found", name)
	}
	delete(b.outputs, name)
	for surfaceID, output := range b.surfaceOutput {
		if output == name {
			delete(b.surfaceOutput, surfaceID)
		}
	}
	b.layoutOutputsLocked()
	b.mu.Unlock()
	return b.applyAllOutputAssignments()
}

func (b *Bridge) ResizeOutput(req schema.ResizeOutputRequest) (schema.LogicalOutput, error) {
	b.mu.Lock()
	out, ok := b.outputs[req.Name]
	if !ok {
		b.mu.Unlock()
		return schema.LogicalOutput{}, fmt.Errorf("output %s not found", req.Name)
	}
	if req.Width <= 0 || req.Height <= 0 {
		b.mu.Unlock()
		return schema.LogicalOutput{}, fmt.Errorf("width and height must be positive")
	}
	out.Width, out.Height, out.UpdatedAt = req.Width, req.Height, time.Now()
	b.outputs[req.Name] = out
	b.layoutOutputsLocked()
	out = b.outputs[req.Name]
	b.mu.Unlock()
	if err := b.applyOutputAssignments(req.Name); err != nil {
		return schema.LogicalOutput{}, err
	}
	return out, nil
}

func (b *Bridge) SetOutputScale(req schema.SetOutputScaleRequest) (schema.LogicalOutput, error) {
	b.mu.Lock()
	out, ok := b.outputs[req.Name]
	if !ok {
		b.mu.Unlock()
		return schema.LogicalOutput{}, fmt.Errorf("output %s not found", req.Name)
	}
	if req.Scale <= 0 {
		b.mu.Unlock()
		return schema.LogicalOutput{}, fmt.Errorf("scale must be positive")
	}
	out.Scale, out.UpdatedAt = req.Scale, time.Now()
	b.outputs[req.Name] = out
	b.mu.Unlock()
	return out, nil
}

func (b *Bridge) ListOutputs() []schema.LogicalOutput {
	b.mu.RLock()
	defer b.mu.RUnlock()
	outs := make([]schema.LogicalOutput, 0, len(b.outputs))
	for _, out := range b.outputs {
		outs = append(outs, b.decorateOutputLocked(out))
	}
	sort.Slice(outs, func(i, j int) bool { return outs[i].Name < outs[j].Name })
	return outs
}

func (b *Bridge) MoveSurfaceToOutput(surfaceID, outputName string) (schema.MoveSurfaceToOutputResponse, error) {
	b.mu.RLock()
	surface, ok := b.surfaces[surfaceID]
	if !ok {
		b.mu.RUnlock()
		return schema.MoveSurfaceToOutputResponse{}, compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", surfaceID)
	}
	out, ok := b.outputs[outputName]
	if !ok {
		b.mu.RUnlock()
		return schema.MoveSurfaceToOutputResponse{}, fmt.Errorf("output %s not found", outputName)
	}
	_ = surface
	geom := schema.SurfaceGeometry{X: out.PhysicalX, Y: out.PhysicalY, Width: out.PhysicalWidth, Height: out.PhysicalHeight}
	if geom.Width <= 0 {
		geom.Width = out.Width
	}
	if geom.Height <= 0 {
		geom.Height = out.Height
	}
	b.mu.RUnlock()

	if err := b.placeSurface(surfaceID, geom, 0, true); err != nil {
		return schema.MoveSurfaceToOutputResponse{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	surface = b.surfaces[surfaceID]
	surface.OutputID = outputName
	surface.Geometry = &geom
	surface.Surface.Geometry = &geom
	surface.Surface.OutputID = outputName
	surface.UpdatedAt = time.Now()
	b.surfaces[surfaceID] = surface
	b.surfaceOutput[surfaceID] = outputName
	return schema.MoveSurfaceToOutputResponse{SurfaceID: surfaceID, Output: outputName, Geometry: geom}, nil
}

func (b *Bridge) placeSurface(surfaceID string, geom schema.SurfaceGeometry, timeout time.Duration, waitReadback bool) error {
	b.mu.Lock()
	if b.plugin == nil {
		b.mu.Unlock()
		return compositorError(schema.ErrorCompositorUnavailable, "no plugin connected")
	}
	b.placeSeq++
	requestID := fmt.Sprintf("place-%d-%d", time.Now().UnixNano(), b.placeSeq)
	ch := make(chan schema.CompositorPlacePluginResponse, 1)
	b.placeWaiters[requestID] = ch
	session := b.plugin
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.placeWaiters, requestID)
		b.mu.Unlock()
	}()

	if err := session.Send(schema.CompositorPlaceSurface{Type: string(schema.PluginMessagePlaceSurface), RequestID: requestID, SurfaceID: surfaceID, Geometry: geom}); err != nil {
		return err
	}

	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.After(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	pluginAcked := false
	for {
		select {
		case resp := <-ch:
			if !resp.OK {
				if resp.Error == "" {
					resp.Error = "placement failed"
				}
				return compositorError(schema.ErrorProtocolError, "place surface failed: %s", resp.Error)
			}
			pluginAcked = true
			if !waitReadback || b.surfaceGeometryMatches(surfaceID, geom) {
				return nil
			}
		case <-ticker.C:
			if b.surfaceGeometryMatches(surfaceID, geom) {
				return nil
			}
		case <-deadline:
			if pluginAcked {
				return compositorError(schema.ErrorFrameTimeout, "place surface readback timed out")
			}
			return compositorError(schema.ErrorFrameTimeout, "place surface timed out")
		}
	}
}

func (b *Bridge) surfaceGeometryMatches(surfaceID string, geom schema.SurfaceGeometry) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	surface, ok := b.surfaces[surfaceID]
	if !ok || surface.Geometry == nil {
		return false
	}
	current := *surface.Geometry
	return absInt(current.X-geom.X) <= 2 && absInt(current.Y-geom.Y) <= 2 &&
		absInt(current.Width-geom.Width) <= 8 && absInt(current.Height-geom.Height) <= 16
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (b *Bridge) ListOutputSurfaces(name string) (schema.ListOutputSurfacesResponse, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out, ok := b.outputs[name]
	if !ok {
		return schema.ListOutputSurfacesResponse{}, fmt.Errorf("output %s not found", name)
	}
	var surfaces []schema.CompositorTrackedSurface
	for _, surface := range b.surfaces {
		if b.surfaceOutput[surface.Surface.ID] == name {
			surfaces = append(surfaces, b.decorateSurfaceLocked(surface))
		}
	}
	sort.Slice(surfaces, func(i, j int) bool { return surfaces[i].Surface.ID < surfaces[j].Surface.ID })
	return schema.ListOutputSurfacesResponse{Output: b.decorateOutputLocked(out), Surfaces: surfaces}, nil
}

func (b *Bridge) CaptureOutput(req schema.CaptureOutputRequest) (schema.CaptureOutputResponse, error) {
	listed, err := b.ListOutputSurfaces(req.Name)
	if err != nil {
		return schema.CaptureOutputResponse{}, err
	}
	resp := schema.CaptureOutputResponse{Output: req.Name}
	if len(listed.Surfaces) == 0 {
		resp.Warnings = append(resp.Warnings, "output has no surfaces to capture")
		return resp, compositorError(schema.ErrorCaptureDenied, "output %s has no surfaces to capture", req.Name)
	}
	for _, surface := range listed.Surfaces {
		cap, err := b.CaptureSurface(schema.CaptureSurfaceRequest{SurfaceID: surface.Surface.ID, Export: req.Export, SessionID: req.SessionID, SessionToken: req.SessionToken, AuditCorrelationID: req.AuditCorrelationID, EvidenceClass: req.EvidenceClass, ASHACommandSequenceID: req.ASHACommandSequenceID})
		if err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("capture %s: %v", surface.Surface.ID, err))
			continue
		}
		resp.Captures = append(resp.Captures, cap)
	}
	if len(resp.Captures) == 0 {
		return resp, compositorError(schema.ErrorCaptureDenied, "output %s capture failed for all %d surface(s): %s", req.Name, len(listed.Surfaces), strings.Join(resp.Warnings, "; "))
	}
	return resp, nil
}

func (b *Bridge) applyOutputAssignments(name string) error {
	b.mu.RLock()
	var surfaceIDs []string
	for surfaceID, output := range b.surfaceOutput {
		if output == name {
			surfaceIDs = append(surfaceIDs, surfaceID)
		}
	}
	b.mu.RUnlock()
	for _, surfaceID := range surfaceIDs {
		b.mu.RLock()
		_, exists := b.surfaces[surfaceID]
		b.mu.RUnlock()
		if !exists {
			b.mu.Lock()
			delete(b.surfaceOutput, surfaceID)
			b.mu.Unlock()
			continue
		}
		if _, err := b.MoveSurfaceToOutput(surfaceID, name); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bridge) applyAllOutputAssignments() error {
	b.mu.RLock()
	names := make(map[string]struct{}, len(b.surfaceOutput))
	for _, output := range b.surfaceOutput {
		if output != "" {
			names[output] = struct{}{}
		}
	}
	b.mu.RUnlock()
	for name := range names {
		if err := b.applyOutputAssignments(name); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bridge) layoutOutputsLocked() {
	names := make([]string, 0, len(b.outputs))
	for name := range b.outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return
	}
	cols := 1
	for cols*cols < len(names) {
		cols++
	}
	rows := (len(names) + cols - 1) / cols
	physicalW, physicalH := b.physicalBoundsLocked()
	slotW, slotH := physicalW/cols, physicalH/rows
	if slotW <= 0 {
		slotW = 640
	}
	if slotH <= 0 {
		slotH = 480
	}
	for i, name := range names {
		out := b.outputs[name]
		out.PhysicalX = (i % cols) * slotW
		out.PhysicalY = (i / cols) * slotH
		out.PhysicalWidth = slotW
		out.PhysicalHeight = slotH
		out.UpdatedAt = time.Now()
		b.outputs[name] = out
	}
}

func (b *Bridge) physicalBoundsLocked() (int, int) {
	maxX, maxY := 1920, 1080
	for _, surface := range b.surfaces {
		if surface.Geometry != nil {
			if surface.Geometry.X+surface.Geometry.Width > maxX {
				maxX = surface.Geometry.X + surface.Geometry.Width
			}
			if surface.Geometry.Y+surface.Geometry.Height > maxY {
				maxY = surface.Geometry.Y + surface.Geometry.Height
			}
		}
	}
	return maxX, maxY
}

func (b *Bridge) decorateOutputLocked(out schema.LogicalOutput) schema.LogicalOutput {
	out.Surfaces = nil
	for surfaceID, output := range b.surfaceOutput {
		if output == out.Name {
			out.Surfaces = append(out.Surfaces, surfaceID)
		}
	}
	sort.Strings(out.Surfaces)
	return out
}

func (b *Bridge) outputForSurfaceLocked(surface schema.CompositorTrackedSurface) string {
	if out := b.surfaceOutput[surface.Surface.ID]; out != "" {
		return out
	}
	if surface.OutputID != "" {
		return surface.OutputID
	}
	if surface.Geometry == nil {
		return ""
	}
	best, area := "", 0
	for name, out := range b.outputs {
		overlap := rectOverlap(*surface.Geometry, schema.SurfaceGeometry{X: out.PhysicalX, Y: out.PhysicalY, Width: out.PhysicalWidth, Height: out.PhysicalHeight})
		if overlap > area {
			best, area = name, overlap
		}
	}
	return best
}

func rectOverlap(a, b schema.SurfaceGeometry) int {
	x1, y1 := maxInt(a.X, b.X), maxInt(a.Y, b.Y)
	x2, y2 := minInt(a.X+a.Width, b.X+b.Width), minInt(a.Y+a.Height, b.Y+b.Height)
	if x2 <= x1 || y2 <= y1 {
		return 0
	}
	return (x2 - x1) * (y2 - y1)
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (b *Bridge) GetSurface(surfaceID string) (schema.CompositorTrackedSurface, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	surface, ok := b.surfaces[surfaceID]
	if !ok {
		return schema.CompositorTrackedSurface{}, compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", surfaceID)
	}
	return b.decorateSurfaceLocked(surface), nil
}

func (b *Bridge) WaitForSurface(req schema.WaitForSurfaceRequest) (schema.WaitForSurfaceResponse, error) {
	deadline := timeoutDeadline(req.TimeoutMs, 5*time.Second)
	for time.Now().Before(deadline) {
		b.mu.RLock()
		for _, surface := range b.surfaces {
			surface = b.decorateSurfaceLocked(surface)
			if req.SessionID != "" && surface.SessionID != req.SessionID {
				continue
			}
			if req.AppID != "" && !strings.Contains(surface.Surface.AppID, req.AppID) {
				continue
			}
			if req.Title != "" && !strings.Contains(surface.Surface.Title, req.Title) {
				continue
			}
			b.mu.RUnlock()
			return schema.WaitForSurfaceResponse{Surface: surface}, nil
		}
		b.mu.RUnlock()
		time.Sleep(50 * time.Millisecond)
	}
	return schema.WaitForSurfaceResponse{}, compositorError(schema.ErrorAppNotReady, "surface wait timed out")
}

func classifyCapturePluginError(message string) error {
	if message == "" {
		message = "capture failed"
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "surface") && strings.Contains(lower, "not found"):
		return compositorError(schema.ErrorSurfaceNotFound, "capture failed: %s", message)
	case strings.Contains(lower, "timed out"):
		return compositorError(schema.ErrorFrameTimeout, "capture failed: %s", message)
	case strings.Contains(lower, "denied") || strings.Contains(lower, "access"):
		return compositorError(schema.ErrorCaptureDenied, "capture failed: %s", message)
	case strings.Contains(lower, "empty dimensions") || strings.Contains(lower, "black"):
		return compositorError(schema.ErrorCaptureBlackFrame, "capture failed: %s", message)
	default:
		return compositorError(schema.ErrorProtocolError, "capture failed: %s", message)
	}
}

func classifyInputPluginError(message string) error {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "surface") && strings.Contains(lower, "not found"):
		return compositorError(schema.ErrorSurfaceNotFound, "%s", message)
	case strings.Contains(lower, "coordinate") && strings.Contains(lower, "unsupported"):
		return compositorError(schema.ErrorInvalidCoordinates, "%s", message)
	case strings.Contains(lower, "outside") || strings.Contains(lower, "invalid coordinate"):
		return compositorError(schema.ErrorInvalidCoordinates, "%s", message)
	case strings.Contains(lower, "denied") || strings.Contains(lower, "rejected") || strings.Contains(lower, "input injection failed"):
		return compositorError(schema.ErrorInputDenied, "%s", message)
	case strings.Contains(lower, "seat not available"):
		return compositorError(schema.ErrorInputDenied, "%s", message)
	case strings.Contains(lower, "empty dimensions"):
		return compositorError(schema.ErrorInputDenied, "%s", message)
	default:
		return compositorError(schema.ErrorProtocolError, "%s", message)
	}
}

func (b *Bridge) recordFramePresented(surfaceID string) {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if surface, ok := b.surfaces[surfaceID]; ok {
		surface.FrameCount++
		surface.LastPresentTimestamp = &now
		surface.UpdatedAt = now
		b.surfaces[surfaceID] = surface
	}
}

func (b *Bridge) recordCaptureReadback(surfaceID string) time.Time {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if surface, ok := b.surfaces[surfaceID]; ok {
		surface.CaptureCount++
		surface.LastCaptureTimestamp = &now
		surface.UpdatedAt = now
		b.surfaces[surfaceID] = surface
	}
	return now
}

func (b *Bridge) WaitForFrame(req schema.WaitForFrameRequest) (schema.WaitForFrameResponse, error) {
	deadline := timeoutDeadline(req.TimeoutMs, 5*time.Second)
	for time.Now().Before(deadline) {
		b.mu.RLock()
		surface, ok := b.surfaces[req.SurfaceID]
		if ok && surface.FrameCount > req.AfterFrame {
			timestamp := surface.UpdatedAt
			if surface.LastPresentTimestamp != nil {
				timestamp = *surface.LastPresentTimestamp
			}
			b.mu.RUnlock()
			return schema.WaitForFrameResponse{SurfaceID: req.SurfaceID, FrameCount: surface.FrameCount, Timestamp: timestamp}, nil
		}
		b.mu.RUnlock()
		if !ok {
			return schema.WaitForFrameResponse{}, compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return schema.WaitForFrameResponse{}, compositorError(schema.ErrorFrameTimeout, "frame wait timed out")
}

func (b *Bridge) WaitForAppReady(req schema.WaitForAppReadyRequest) (schema.WaitForSurfaceResponse, error) {
	deadline := timeoutDeadline(req.TimeoutMs, 5*time.Second)
	for time.Now().Before(deadline) {
		b.mu.RLock()
		launch := b.launches[req.LaunchID]
		if launch == nil {
			b.mu.RUnlock()
			return schema.WaitForSurfaceResponse{}, compositorError(schema.ErrorAppNotReady, "launch %s not found", req.LaunchID)
		}
		surfaceIDs := b.surfacesForLaunchLocked(launch)
		b.mu.RUnlock()
		for _, surfaceID := range surfaceIDs {
			b.mu.RLock()
			if surface, ok := b.surfaces[surfaceID]; ok && surface.FrameCount > 0 {
				surface = b.decorateSurfaceLocked(surface)
				b.mu.RUnlock()
				return schema.WaitForSurfaceResponse{Surface: surface}, nil
			}
			b.mu.RUnlock()
		}
		time.Sleep(50 * time.Millisecond)
	}
	return schema.WaitForSurfaceResponse{}, compositorError(schema.ErrorAppNotReady, "app readiness timed out")
}

func (b *Bridge) WaitForRenderIdle(req schema.WaitForRenderIdleRequest) (schema.WaitGenericResponse, error) {
	idle := time.Duration(req.IdleMs) * time.Millisecond
	if idle <= 0 {
		idle = 250 * time.Millisecond
	}
	deadline := timeoutDeadline(req.TimeoutMs, 5*time.Second)
	for time.Now().Before(deadline) {
		b.mu.RLock()
		surface, ok := b.surfaces[req.SurfaceID]
		frameCount := surface.FrameCount
		presentedAt := surface.LastPresentTimestamp
		b.mu.RUnlock()
		if !ok {
			return schema.WaitGenericResponse{}, compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID)
		}
		if frameCount > 0 && presentedAt != nil && time.Since(*presentedAt) >= idle {
			return schema.WaitGenericResponse{OK: true, SurfaceID: req.SurfaceID, Timestamp: time.Now()}, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return schema.WaitGenericResponse{}, compositorError(schema.ErrorFrameTimeout, "render idle wait timed out")
}

func (b *Bridge) WaitForNoPending(req schema.WaitForNoPendingRequest) (schema.WaitGenericResponse, error) {
	idleReq := schema.WaitForRenderIdleRequest{SurfaceID: req.SurfaceID, IdleMs: 100, TimeoutMs: req.TimeoutMs}
	return b.WaitForRenderIdle(idleReq)
}

func timeoutDeadline(ms int, def time.Duration) time.Time {
	if ms <= 0 {
		return time.Now().Add(def)
	}
	return time.Now().Add(time.Duration(ms) * time.Millisecond)
}

func (b *Bridge) ListArtifacts(sessionID string) []schema.ArtifactRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.loadArtifactsFromDiskLocked(sessionID)
	out := make([]schema.ArtifactRecord, 0)
	for _, a := range b.artifacts {
		if sessionID == "" || a.SessionID == sessionID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ArtifactID < out[j].ArtifactID })
	return out
}

func (b *Bridge) GetArtifact(artifactID string) (schema.ArtifactRecord, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.loadArtifactsFromDiskLocked("")
	a, ok := b.artifacts[artifactID]
	if !ok {
		return schema.ArtifactRecord{}, fmt.Errorf("artifact %s not found", artifactID)
	}
	return a, nil
}

func (b *Bridge) loadArtifactsFromDiskLocked(sessionID string) {
	roots := []string{"/run/agent-os/artifacts/*/*/index.json"}
	if sessionID != "" {
		roots = []string{filepath.Join("/run/agent-os/artifacts", sessionID, "*", "index.json")}
	}
	for _, pattern := range roots {
		matches, _ := filepath.Glob(pattern)
		for _, path := range matches {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var artifact schema.ArtifactRecord
			if err := json.Unmarshal(data, &artifact); err != nil || artifact.ArtifactID == "" {
				continue
			}
			b.artifacts[artifact.ArtifactID] = artifact
		}
	}
}

func (b *Bridge) ExportArtifacts(req schema.ExportArtifactsRequest) (schema.ExportArtifactsResponse, error) {
	if req.SessionID == "" || req.To == "" {
		return schema.ExportArtifactsResponse{}, fmt.Errorf("session_id and to are required")
	}
	arts := b.ListArtifacts(req.SessionID)
	if err := os.MkdirAll(req.To, 0755); err != nil {
		return schema.ExportArtifactsResponse{}, err
	}
	copied := make([]string, 0)
	for _, a := range arts {
		for _, src := range []string{a.ImagePath, a.IndexPath} {
			data, err := os.ReadFile(src)
			if err != nil {
				return schema.ExportArtifactsResponse{}, err
			}
			dst := filepath.Join(req.To, a.ArtifactID+"-"+filepath.Base(src))
			if err := os.WriteFile(dst, data, 0644); err != nil {
				return schema.ExportArtifactsResponse{}, err
			}
			copied = append(copied, dst)
		}
	}
	index, _ := json.MarshalIndent(arts, "", "  ")
	idxPath := filepath.Join(req.To, "artifacts-index.json")
	if err := os.WriteFile(idxPath, index, 0644); err != nil {
		return schema.ExportArtifactsResponse{}, err
	}
	copied = append(copied, idxPath)
	return schema.ExportArtifactsResponse{SessionID: req.SessionID, To: req.To, Copied: copied}, nil
}

func (b *Bridge) decorateSurfaceLocked(surface schema.CompositorTrackedSurface) schema.CompositorTrackedSurface {
	if launchID := b.surfaceLaunch[surface.Surface.ID]; launchID != "" {
		if launch := b.launches[launchID]; launch != nil {
			surface.SessionID = launch.process.SessionID
		}
	}
	if surface.Geometry == nil {
		surface.Geometry = surface.Surface.Geometry
	}
	if surface.PixelSize == nil {
		surface.PixelSize = surface.Surface.PixelSize
	}
	if surface.ScaleFactor == 0 {
		surface.ScaleFactor = surface.Surface.ScaleFactor
	}
	if surface.ScaleFactor == 0 {
		surface.ScaleFactor = 1
	}
	if surface.OutputID == "" {
		surface.OutputID = surface.Surface.OutputID
	}
	if surface.OutputID == "" {
		surface.OutputID = b.outputForSurfaceLocked(surface)
	}
	if surface.Surface.Visible != nil {
		surface.Visible = *surface.Surface.Visible
	}
	if surface.Surface.Minimized != nil && *surface.Surface.Minimized {
		surface.Visible = false
	}
	if surface.LastEvent == schema.SurfaceEventFocused {
		surface.Focused = true
	}
	surface.Capturable = surface.Visible
	surface.InputInjectable = surface.Visible
	if surface.Surface.Minimized != nil && *surface.Surface.Minimized {
		surface.Capturable = false
		surface.InputInjectable = false
	}
	if surface.Surface.SurfaceKind == schema.SurfaceKindLayerShell {
		surface.Capturable = false
		surface.InputInjectable = false
	}
	policy := b.policies[surface.Surface.ID]
	grantState := &schema.SurfaceGrantState{OwnerUID: policy.OwnerUID}
	seen := map[uint32]struct{}{}
	for _, uid := range policy.AllowPointerUIDs {
		seen[uid] = struct{}{}
		grantState.GrantActions = append(grantState.GrantActions, "pointer")
	}
	for _, uid := range policy.AllowKeyboardUIDs {
		seen[uid] = struct{}{}
		grantState.GrantActions = append(grantState.GrantActions, "keyboard")
	}
	for uid := range seen {
		grantState.GrantedUIDs = append(grantState.GrantedUIDs, uid)
	}
	sort.Slice(grantState.GrantedUIDs, func(i, j int) bool { return grantState.GrantedUIDs[i] < grantState.GrantedUIDs[j] })
	surface.GrantState = grantState
	if placement, ok := b.surfacePlacements[surface.Surface.ID]; ok {
		copyPlacement := placement
		surface.Surface.ManagementState = placement.ManagementState
		surface.Surface.Placement = &copyPlacement
	} else if surface.Surface.Placement == nil {
		placement := unmanagedPlacement(surface)
		surface.Surface.ManagementState = placement.ManagementState
		surface.Surface.Placement = &placement
	} else if surface.Surface.ManagementState == "" {
		surface.Surface.ManagementState = surface.Surface.Placement.ManagementState
	}
	return surface
}

func (b *Bridge) dispatch(peerUID uint32, req schema.Request) (schema.Response, error) {
	switch req.Method {
	case schema.MethodListSurfaces:
		return okResponse(schema.ListSurfacesResponse{Surfaces: b.ListSurfaces()}), nil
	case schema.MethodGetSurface:
		var body schema.GetSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		surface, err := b.GetSurface(body.SurfaceID)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(surface), nil
	case schema.MethodCaptureSurface:
		var body schema.CaptureSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.SurfaceID == "" {
			return schema.Response{}, fmt.Errorf("surface_id is required")
		}
		capture, err := b.CaptureSurface(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(capture), nil
	case schema.MethodInjectInput:
		var body schema.InjectInputRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.SurfaceID == "" {
			return schema.Response{}, fmt.Errorf("surface_id is required")
		}
		resp, err := b.InjectInput(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodCreateSession:
		var body schema.CreateSessionRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		return okResponse(b.CreateSession(body)), nil
	case schema.MethodListSessions:
		return okResponse(schema.ListSessionsResponse{Sessions: b.ListSessions()}), nil
	case schema.MethodGetSession:
		var body schema.SessionRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.SessionID == "" {
			return schema.Response{}, fmt.Errorf("session_id is required")
		}
		session, err := b.GetSession(body.SessionID)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(session), nil
	case schema.MethodDestroySession:
		var body schema.SessionRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.SessionID == "" {
			return schema.Response{}, fmt.Errorf("session_id is required")
		}
		if err := b.DestroySession(body.SessionID); err != nil {
			return schema.Response{}, err
		}
		return okResponse("destroyed"), nil
	case schema.MethodResetSession:
		var body schema.SessionRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.SessionID == "" {
			return schema.Response{}, fmt.Errorf("session_id is required")
		}
		if err := b.ResetSession(body.SessionID); err != nil {
			return schema.Response{}, err
		}
		return okResponse("reset"), nil
	case schema.MethodLaunchApp:
		var body schema.LaunchAppRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.launchAppAsPeer(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodListProcesses:
		var body schema.ListProcessesRequest
		if len(req.Body) > 0 {
			if err := json.Unmarshal(req.Body, &body); err != nil {
				return schema.Response{}, fmt.Errorf("bad body: %w", err)
			}
		}
		return okResponse(schema.ListProcessesResponse{Processes: b.ListProcesses(body.SessionID)}), nil
	case schema.MethodWaitForSurface:
		var body schema.WaitForSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.WaitForSurface(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodWaitForFrame:
		var body schema.WaitForFrameRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.WaitForFrame(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodWaitForAppReady:
		var body schema.WaitForAppReadyRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.WaitForAppReady(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodWaitForRenderIdle:
		var body schema.WaitForRenderIdleRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.WaitForRenderIdle(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodWaitForNoPending:
		var body schema.WaitForNoPendingRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.WaitForNoPending(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodListArtifacts:
		var body schema.ListArtifactsRequest
		if len(req.Body) > 0 {
			if err := json.Unmarshal(req.Body, &body); err != nil {
				return schema.Response{}, fmt.Errorf("bad body: %w", err)
			}
		}
		return okResponse(schema.ListArtifactsResponse{Artifacts: b.ListArtifacts(body.SessionID)}), nil
	case schema.MethodGetArtifact:
		var body schema.GetArtifactRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		artifact, err := b.GetArtifact(body.ArtifactID)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(artifact), nil
	case schema.MethodExportArtifacts:
		var body schema.ExportArtifactsRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.ExportArtifacts(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodTerminateLaunch:
		var body schema.TerminateLaunchRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.LaunchID == "" {
			return schema.Response{}, fmt.Errorf("launch_id is required")
		}
		if err := b.requireLaunchSessionToken(body.LaunchID, body.SessionToken); err != nil {
			return schema.Response{}, err
		}
		resp, err := b.TerminateLaunch(body.LaunchID)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodCreateOutput:
		var body schema.CreateOutputRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		out, err := b.CreateOutput(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(out), nil
	case schema.MethodDestroyOutput:
		var body schema.OutputRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if err := b.DestroyOutput(body.Name); err != nil {
			return schema.Response{}, err
		}
		return okResponse("destroyed"), nil
	case schema.MethodResizeOutput:
		var body schema.ResizeOutputRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		out, err := b.ResizeOutput(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(out), nil
	case schema.MethodSetOutputScale:
		var body schema.SetOutputScaleRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		out, err := b.SetOutputScale(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(out), nil
	case schema.MethodListOutputs:
		return okResponse(schema.ListOutputsResponse{Outputs: b.ListOutputs()}), nil
	case schema.MethodListLayoutZones:
		var body schema.ListLayoutZonesRequest
		if len(req.Body) > 0 {
			if err := json.Unmarshal(req.Body, &body); err != nil {
				return schema.Response{}, fmt.Errorf("bad body: %w", err)
			}
		}
		resp, err := b.ListLayoutZones(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodAssignSurfaceTag:
		var body schema.AssignSurfaceTagRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.AssignSurfaceTag(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodGetArrangement:
		var body schema.GetArrangementRequest
		if len(req.Body) > 0 {
			if err := json.Unmarshal(req.Body, &body); err != nil {
				return schema.Response{}, fmt.Errorf("bad body: %w", err)
			}
		}
		resp, err := b.GetArrangement(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodMoveSurfaceToOutput:
		var body schema.MoveSurfaceToOutputRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.MoveSurfaceToOutput(body.SurfaceID, body.Output)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodListOutputSurfaces:
		var body schema.OutputRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.ListOutputSurfaces(body.Name)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodCaptureOutput:
		var body schema.CaptureOutputRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.CaptureOutput(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodA11yTree:
		var body schema.A11yTreeRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.A11yTree(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodA11ySemantic:
		var body schema.A11yTreeRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.A11ySemantic(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodA11yFind:
		var body schema.A11yFindRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.A11yFind(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodA11yClick:
		var body schema.A11yClickRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.A11yClick(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodAppCommand:
		var body schema.AppCommandRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.AppCommand(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodAppResult:
		var body schema.AppResultRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.AppResult(body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodUpsertSurfacePolicy:
		var body schema.UpsertSurfacePolicyRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.Surface.SurfaceID == "" {
			return schema.Response{}, fmt.Errorf("surface.surface_id is required")
		}
		if err := b.UpsertSurfacePolicy(body.Surface); err != nil {
			return schema.Response{}, err
		}
		return okResponse("updated"), nil
	case schema.MethodRemoveSurfacePolicy:
		var body schema.RemoveSurfacePolicyRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.SurfaceID == "" {
			return schema.Response{}, fmt.Errorf("surface_id is required")
		}
		if err := b.RemoveSurfacePolicy(body.SurfaceID); err != nil {
			return schema.Response{}, err
		}
		return okResponse("removed"), nil
	case schema.MethodSetInputContext:
		var body schema.SetInputContextRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if err := b.SetInputContext(body.ActorUID); err != nil {
			return schema.Response{}, err
		}
		return okResponse("updated"), nil
	case schema.MethodFocusSurface:
		var body schema.FocusSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.FocusSurface(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodRaiseSurface, schema.MethodDebugRaiseSurface:
		var body schema.RaiseSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.RaiseSurfaceAction(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodMoveSurface:
		var body schema.MoveSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.MoveSurfaceAction(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodResizeSurface:
		var body schema.ResizeSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.ResizeSurfaceAction(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodTileSurface:
		var body schema.TileSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.TileSurfaceAction(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodSetViewProperty:
		var body schema.SetViewPropertyRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if err := b.SetViewProperty(body); err != nil {
			return schema.Response{}, err
		}
		return okResponse("updated"), nil
	case schema.MethodAlwaysOnTop:
		var body schema.AlwaysOnTopRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.AlwaysOnTopAction(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodFullscreenSurface:
		var body schema.FullscreenSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.FullscreenSurfaceAction(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodMaximizeSurface:
		var body schema.MaximizeSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.MaximizeSurfaceAction(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodMinimizeSurface:
		var body schema.MinimizeSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.MinimizeSurfaceAction(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodCloseSurface:
		var body schema.CloseSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		resp, err := b.CloseSurfaceAction(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(resp), nil
	case schema.MethodCloseSurfacesByUID:
		var body schema.CloseSurfacesByUIDRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		queued, err := b.CloseSurfacesByUID(body.OwnerUID)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(schema.CloseSurfacesResponse{Queued: queued}), nil
	case schema.MethodGrantViewport:
		var body schema.ViewportGrantRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.SurfaceID == "" || body.AgentUID == 0 {
			return schema.Response{}, fmt.Errorf("surface_id and agent_uid are required")
		}
		grant, err := b.GrantViewport(peerUID, body)
		if err != nil {
			return schema.Response{}, err
		}
		return okResponse(schema.GrantViewportResponse{Grant: grant}), nil
	case schema.MethodRevokeViewport:
		var body schema.RevokeViewportGrantRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.SurfaceID == "" || body.AgentUID == 0 {
			return schema.Response{}, fmt.Errorf("surface_id and agent_uid are required")
		}
		if err := b.RevokeViewport(peerUID, body); err != nil {
			return schema.Response{}, err
		}
		return okResponse("revoked"), nil
	case schema.MethodCheckSurfaceAccess:
		if peerUID != 0 {
			return schema.Response{}, fmt.Errorf("check_surface_access requires root")
		}
		var body schema.SurfaceAccessCheckRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.SurfaceID == "" || body.AgentUID == 0 || body.Action == "" {
			return schema.Response{}, fmt.Errorf("surface_id, agent_uid, and action are required")
		}
		return okResponse(b.CheckSurfaceAccess(body.SurfaceID, body.AgentUID, body.Action)), nil
	default:
		return schema.Response{}, fmt.Errorf("unknown method: %s", req.Method)
	}
}

func (b *Bridge) handleCaptureResponse(resp schema.CompositorCapturePluginResponse) {
	b.mu.RLock()
	waiter := b.captureWaiters[resp.RequestID]
	b.mu.RUnlock()
	if waiter == nil {
		return
	}
	select {
	case waiter <- resp:
	default:
	}
}

func (b *Bridge) handleInputResponse(resp schema.CompositorInputPluginResponse) {
	b.mu.RLock()
	waiter := b.inputWaiters[resp.RequestID]
	b.mu.RUnlock()
	if waiter == nil {
		return
	}
	select {
	case waiter <- resp:
	default:
	}
}

func (b *Bridge) handlePropertyResponse(resp schema.CompositorPropertyPluginResponse) {
	b.mu.RLock()
	waiter := b.propertyWaiters[resp.RequestID]
	b.mu.RUnlock()
	if waiter == nil {
		return
	}
	select {
	case waiter <- resp:
	default:
	}
}

func (b *Bridge) handleSurfaceStateResponse(resp schema.CompositorSurfaceStatePluginResponse) {
	b.mu.RLock()
	waiter := b.stateWaiters[resp.RequestID]
	b.mu.RUnlock()
	if waiter == nil {
		return
	}
	select {
	case waiter <- resp:
	default:
	}
}

func (b *Bridge) handlePlaceResponse(resp schema.CompositorPlacePluginResponse) {
	b.mu.RLock()
	waiter := b.placeWaiters[resp.RequestID]
	b.mu.RUnlock()
	if waiter == nil {
		return
	}
	select {
	case waiter <- resp:
	default:
	}
}

func (b *Bridge) handleFocusResponse(resp schema.CompositorFocusPluginResponse) {
	b.mu.RLock()
	waiter := b.focusWaiters[resp.RequestID]
	b.mu.RUnlock()
	if waiter == nil {
		return
	}
	select {
	case waiter <- resp:
	default:
	}
}

func (b *Bridge) handleRaiseResponse(resp schema.CompositorRaisePluginResponse) {
	b.mu.RLock()
	waiter := b.raiseWaiters[resp.RequestID]
	b.mu.RUnlock()
	if waiter == nil {
		return
	}
	select {
	case waiter <- resp:
	default:
	}
}

func (b *Bridge) handleSurfaceEvent(msg schema.CompositorPluginEvent) {
	now := time.Now()
	visible := true
	if msg.Surface.Visible != nil {
		visible = *msg.Surface.Visible
	}
	if msg.Surface.SurfaceKind == "" {
		msg.Surface.SurfaceKind = schema.SurfaceKindXDGView
	}
	tracked := schema.CompositorTrackedSurface{
		Surface:         msg.Surface,
		Client:          msg.Client,
		LastEvent:       msg.Event,
		Device:          msg.Device,
		UpdatedAt:       now,
		Geometry:        msg.Surface.Geometry,
		PixelSize:       msg.Surface.PixelSize,
		ScaleFactor:     msg.Surface.ScaleFactor,
		Capturable:      msg.Surface.SurfaceKind != schema.SurfaceKindLayerShell,
		InputInjectable: msg.Surface.SurfaceKind != schema.SurfaceKindLayerShell,
		Visible:         visible,
		OutputID:        msg.Surface.OutputID,
	}
	if tracked.ScaleFactor == 0 {
		tracked.ScaleFactor = 1
	}
	if msg.Event == schema.SurfaceEventFocused {
		tracked.Focused = true
	}
	if msg.Event == schema.SurfaceEventFrameDone {
		tracked.FrameCount = 1
		tracked.LastPresentTimestamp = &now
	}

	topic := topicForSurfaceEvent(msg.Event)
	busBody := schema.CompositorBusEvent{
		Surface: msg.Surface,
		Client:  msg.Client,
		Event:   msg.Event,
		Device:  msg.Device,
	}

	b.mu.Lock()
	switch msg.Event {
	case schema.SurfaceEventMapped:
		delete(b.staleSurfaces, msg.Surface.ID)
		if previous, ok := b.surfaces[msg.Surface.ID]; ok {
			tracked.FrameCount = previous.FrameCount
			tracked.LastPresentTimestamp = previous.LastPresentTimestamp
			tracked.CaptureCount = previous.CaptureCount
			tracked.LastCaptureTimestamp = previous.LastCaptureTimestamp
			tracked.Focused = previous.Focused
		}
		b.surfaces[msg.Surface.ID] = tracked
		b.associateSurfaceLocked(tracked)
		if msg.Surface.SurfaceKind != schema.SurfaceKindLayerShell {
			if err := b.syncDerivedPolicyLocked(msg.Surface.ID); err != nil {
				log.Printf("sync compositor policy for %s: %v", msg.Surface.ID, err)
			}
		}
	case schema.SurfaceEventFocused, schema.SurfaceEventRestored, schema.SurfaceEventMinimized, schema.SurfaceEventStacked, schema.SurfaceEventInputDenied, schema.SurfaceEventFrameDone:
		if msg.Event == schema.SurfaceEventFocused || msg.Event == schema.SurfaceEventRestored {
			for id, other := range b.surfaces {
				if id != msg.Surface.ID {
					other.Focused = false
					b.surfaces[id] = other
				}
			}
		}
		if previous, ok := b.surfaces[msg.Surface.ID]; ok {
			if tracked.Geometry == nil {
				tracked.Geometry = previous.Geometry
			}
			if tracked.PixelSize == nil {
				tracked.PixelSize = previous.PixelSize
			}
			if tracked.OutputID == "" {
				tracked.OutputID = previous.OutputID
			}
			if tracked.Surface.Workspace == nil {
				tracked.Surface.Workspace = previous.Surface.Workspace
			}
			if tracked.Surface.StackLayer == "" {
				tracked.Surface.StackLayer = previous.Surface.StackLayer
			}
			if tracked.Surface.StackIndex == nil {
				tracked.Surface.StackIndex = previous.Surface.StackIndex
			}
			if tracked.Surface.StackCount == nil {
				tracked.Surface.StackCount = previous.Surface.StackCount
			}
			if tracked.Surface.IsTopInStack == nil {
				tracked.Surface.IsTopInStack = previous.Surface.IsTopInStack
			}
			if tracked.Surface.ZOrderGeneration == 0 {
				tracked.Surface.ZOrderGeneration = previous.Surface.ZOrderGeneration
			}
			tracked.FrameCount = previous.FrameCount
			tracked.LastPresentTimestamp = previous.LastPresentTimestamp
			tracked.CaptureCount = previous.CaptureCount
			tracked.LastCaptureTimestamp = previous.LastCaptureTimestamp
			if msg.Event == schema.SurfaceEventFrameDone {
				tracked.FrameCount++
				tracked.LastPresentTimestamp = &now
			}
			if msg.Event != schema.SurfaceEventFocused && msg.Event != schema.SurfaceEventRestored {
				tracked.Focused = previous.Focused
			}
			if msg.Event == schema.SurfaceEventMinimized {
				tracked.Focused = false
			}
		}
		b.surfaces[msg.Surface.ID] = tracked
		b.associateSurfaceLocked(tracked)
		if msg.Surface.SurfaceKind != schema.SurfaceKindLayerShell {
			if _, ok := b.policies[msg.Surface.ID]; !ok {
				if err := b.syncDerivedPolicyLocked(msg.Surface.ID); err != nil {
					log.Printf("sync compositor policy for %s: %v", msg.Surface.ID, err)
				}
			}
		}
	case schema.SurfaceEventUnmapped:
		b.staleSurfaces[msg.Surface.ID] = now
		delete(b.surfaces, msg.Surface.ID)
		delete(b.surfaceLaunch, msg.Surface.ID)
		delete(b.surfaceOutput, msg.Surface.ID)
		delete(b.policies, msg.Surface.ID)
		delete(b.grants, msg.Surface.ID)
		if b.plugin != nil {
			if err := b.plugin.Send(schema.CompositorPolicyRemove{Type: schema.PluginMessagePolicyRemove, SurfaceID: msg.Surface.ID}); err != nil {
				log.Printf("sync compositor policy for %s: %v", msg.Surface.ID, err)
			}
		}
	default:
		b.mu.Unlock()
		return
	}
	var desiredOutput string
	if launchID := b.surfaceLaunch[msg.Surface.ID]; launchID != "" {
		if launch := b.launches[launchID]; launch != nil {
			desiredOutput = launch.expectedOutput
		}
	}
	b.mu.Unlock()

	if desiredOutput != "" {
		surfaceID := msg.Surface.ID
		go func() {
			b.mu.RLock()
			alreadyAssigned := b.surfaceOutput[surfaceID] != ""
			b.mu.RUnlock()
			if alreadyAssigned {
				return
			}
			if _, err := b.MoveSurfaceToOutput(surfaceID, desiredOutput); err != nil {
				log.Printf("place surface %s on output %s: %v", surfaceID, desiredOutput, err)
			}
		}()
	}

	if topic != "" {
		if err := b.bus.Publish(topic, busBody); err != nil {
			log.Printf("publish compositor event %s: %v", topic, err)
		}
	}
}

func topicForSurfaceEvent(event schema.CompositorSurfaceEventName) string {
	switch event {
	case schema.SurfaceEventMapped:
		return schema.TopicCompositorSurfaceCreated
	case schema.SurfaceEventUnmapped:
		return schema.TopicCompositorSurfaceDestroyed
	case schema.SurfaceEventFocused:
		return schema.TopicCompositorSurfaceFocused
	case schema.SurfaceEventInputDenied:
		return schema.TopicCompositorSurfaceInput
	default:
		return ""
	}
}

func (b *Bridge) installPluginSession(session *pluginSession) *pluginSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	previous := b.plugin
	b.plugin = session
	return previous
}

func (b *Bridge) clearPluginSession(session *pluginSession) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.plugin == session {
		b.plugin = nil
	}
}

func (b *Bridge) syncPluginSession(session *pluginSession) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	policies := b.snapshotPoliciesLocked()
	var actorUID *uint32
	if b.actorUID != nil {
		uid := *b.actorUID
		actorUID = &uid
	}

	if err := session.Send(schema.CompositorPolicyReplace{
		Type:     schema.PluginMessagePolicyReplace,
		Surfaces: policies,
	}); err != nil {
		return err
	}
	return session.Send(schema.CompositorInputContextUpdate{
		Type:     schema.PluginMessageInputContext,
		ActorUID: actorUID,
	})
}

func (b *Bridge) snapshotPoliciesLocked() []schema.CompositorSurfacePolicy {
	policies := make([]schema.CompositorSurfacePolicy, 0, len(b.policies))
	for _, policy := range b.policies {
		policies = append(policies, policy)
	}
	sort.Slice(policies, func(i, j int) bool {
		return policies[i].SurfaceID < policies[j].SurfaceID
	})
	return policies
}

func (b *Bridge) isAllowedPluginUID(peerUID uint32) bool {
	return peerUID == 0 || peerUID == b.allowedPluginUID
}

func (b *Bridge) sendToPlugin(msg any) error {
	b.mu.RLock()
	session := b.plugin
	b.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("no plugin connected")
	}
	return session.Send(msg)
}

func (b *Bridge) syncDerivedPolicyLocked(surfaceID string) error {
	policy := b.rebuildSurfacePolicyLocked(surfaceID)
	if b.plugin == nil {
		return nil
	}
	return b.plugin.Send(schema.CompositorPolicyUpsert{Type: schema.PluginMessagePolicyUpsert, Surface: policy})
}

func (b *Bridge) rebuildSurfacePolicyLocked(surfaceID string) schema.CompositorSurfacePolicy {
	tracked := b.surfaces[surfaceID]
	policy := schema.CompositorSurfacePolicy{
		SurfaceID: surfaceID,
		OwnerUID:  tracked.Client.UID,
	}

	pointer := make(map[uint32]struct{})
	keyboard := make(map[uint32]struct{})
	for uid, grant := range b.grants[surfaceID] {
		if grantAllows(grant, schema.AccessPointer) {
			pointer[uid] = struct{}{}
		}
		if grantAllows(grant, schema.AccessKeyboard) {
			keyboard[uid] = struct{}{}
		}
	}
	policy.AllowPointerUIDs = sortedUIDs(pointer)
	policy.AllowKeyboardUIDs = sortedUIDs(keyboard)
	b.policies[surfaceID] = policy
	return policy
}

func (b *Bridge) checkSurfaceAccessLocked(surfaceID string, agentUID uint32, action schema.CompositorAccessAction) (bool, string) {
	tracked, ok := b.surfaces[surfaceID]
	if !ok {
		return false, "surface not found"
	}
	if action != schema.AccessPointer && action != schema.AccessKeyboard && action != schema.AccessReadPixels {
		return false, "unknown access action"
	}
	if tracked.Client.UID == agentUID {
		return true, "surface owner"
	}
	grantsForSurface, ok := b.grants[surfaceID]
	if !ok {
		return false, "no viewport grant"
	}
	grant, ok := grantsForSurface[agentUID]
	if !ok {
		return false, "no viewport grant"
	}
	if !grantAllows(grant, action) {
		return false, fmt.Sprintf("viewport grant does not include %s", action)
	}
	return true, "viewport grant"
}

func sortedUIDs(values map[uint32]struct{}) []uint32 {
	uids := make([]uint32, 0, len(values))
	for uid := range values {
		uids = append(uids, uid)
	}
	sort.Slice(uids, func(i, j int) bool {
		return uids[i] < uids[j]
	})
	return uids
}

func launchCredential(peerUID uint32, req schema.LaunchAppRequest) (*syscall.Credential, error) {
	var uid, gid *uint32
	uid = req.RunAsUID
	gid = req.RunAsGID
	if peerUID != 0 && (uid != nil || gid != nil) {
		return nil, fmt.Errorf("run_as_uid/run_as_gid overrides require root peer credentials")
	}
	if uid == nil && os.Getuid() == 0 {
		if u, err := user.Lookup("agent"); err == nil {
			if parsedUID, err := strconv.ParseUint(u.Uid, 10, 32); err == nil {
				v := uint32(parsedUID)
				uid = &v
			}
			if parsedGID, err := strconv.ParseUint(u.Gid, 10, 32); err == nil {
				v := uint32(parsedGID)
				gid = &v
			}
		}
	}
	if uid == nil && gid == nil {
		return nil, nil
	}
	cred := &syscall.Credential{}
	if uid != nil {
		cred.Uid = *uid
	}
	if gid != nil {
		cred.Gid = *gid
	}
	return cred, nil
}

func defaultWaylandDisplay() string {
	matches, err := filepath.Glob("/run/user/1001/wayland-*")
	if err != nil {
		return "wayland-1"
	}
	type candidate struct {
		name    string
		modTime time.Time
	}
	candidates := make([]candidate, 0, len(matches))
	for _, match := range matches {
		if strings.HasSuffix(match, ".lock") {
			continue
		}
		info, err := os.Stat(match)
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{name: filepath.Base(match), modTime: info.ModTime()})
	}
	if len(candidates) == 0 {
		return "wayland-1"
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modTime.After(candidates[j].modTime) })
	return candidates[0].name
}

func okResponse(body any) schema.Response {
	b, _ := json.Marshal(body)
	return schema.Response{OK: true, Body: b}
}

func writeError(conn net.Conn, err error) {
	class, message := classifyError(err)
	b, _ := json.Marshal(message)
	resp := schema.Response{OK: false, Body: b, ErrorClass: class, ErrorMessage: message}
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("write compositor error response: %v", err)
	}
}
