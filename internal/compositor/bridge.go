package compositor

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	process       schema.CompositorLaunchProcess
	cmd           *exec.Cmd
	expectedAppID string
	expectedTitle string
}

type Bridge struct {
	bus              publisher
	allowedPluginUID uint32
	grantStore       *grantStore

	mu             sync.RWMutex
	plugin         *pluginSession
	surfaces       map[string]schema.CompositorTrackedSurface
	policies       map[string]schema.CompositorSurfacePolicy
	grants         map[string]map[uint32]schema.SurfaceAccessGrant
	actorUID       *uint32
	captureSeq     uint64
	captureWaiters map[string]chan schema.CompositorCapturePluginResponse
	inputSeq       uint64
	inputWaiters   map[string]chan schema.CompositorInputPluginResponse
	sessionSeq     uint64
	launchSeq      uint64
	sessions       map[string]schema.CompositorSession
	launches       map[string]*launchRecord
	surfaceLaunch  map[string]string
}

func New(bus publisher, cfg Config) (*Bridge, error) {
	store, err := newGrantStore(cfg.GrantLogPath)
	if err != nil {
		return nil, err
	}
	return &Bridge{
		bus:              bus,
		allowedPluginUID: cfg.AllowedPluginUID,
		grantStore:       store,
		surfaces:         make(map[string]schema.CompositorTrackedSurface),
		policies:         make(map[string]schema.CompositorSurfacePolicy),
		grants:           make(map[string]map[uint32]schema.SurfaceAccessGrant),
		captureWaiters:   make(map[string]chan schema.CompositorCapturePluginResponse),
		inputWaiters:     make(map[string]chan schema.CompositorInputPluginResponse),
		sessions:         make(map[string]schema.CompositorSession),
		launches:         make(map[string]*launchRecord),
		surfaceLaunch:    make(map[string]string),
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
		writeError(conn, fmt.Sprintf("peer credentials: %v", err))
		return
	}

	var req schema.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeError(conn, fmt.Sprintf("decode: %v", err))
		return
	}

	resp, err := b.dispatch(peerUID, req)
	if err != nil {
		writeError(conn, err.Error())
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
		surfaces = append(surfaces, surface)
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
		return schema.CompositorSession{}, fmt.Errorf("session %s not found", sessionID)
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
		return fmt.Errorf("session %s not found", sessionID)
	}
	launchIDs := make([]string, 0)
	for id, launch := range b.launches {
		if launch.process.SessionID == sessionID &&
			(launch.process.Status == "running" || len(b.surfacesForLaunchLocked(launch)) > 0) {
			launchIDs = append(launchIDs, id)
		}
	}
	b.mu.RUnlock()
	for _, id := range launchIDs {
		_, _ = b.TerminateLaunch(id)
	}
	b.mu.Lock()
	session := b.sessions[sessionID]
	session.LastUsedAt = time.Now()
	b.sessions[sessionID] = session
	b.mu.Unlock()
	return nil
}

func (b *Bridge) LaunchApp(req schema.LaunchAppRequest) (schema.LaunchAppResponse, error) {
	if strings.TrimSpace(req.Command) == "" {
		return schema.LaunchAppResponse{}, fmt.Errorf("command is required")
	}
	if req.SessionID != "" {
		b.mu.RLock()
		_, ok := b.sessions[req.SessionID]
		b.mu.RUnlock()
		if !ok {
			return schema.LaunchAppResponse{}, fmt.Errorf("session %s not found", req.SessionID)
		}
	}

	cmd := exec.Command("sh", "-lc", req.Command)
	sys := &syscall.SysProcAttr{Setpgid: true}
	if cred := launchCredential(req); cred != nil {
		sys.Credential = cred
	}
	cmd.SysProcAttr = sys
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
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
		Command:   req.Command,
		Cwd:       req.Cwd,
		Status:    "running",
		StartedAt: now,
	}
	b.launches[launchID] = &launchRecord{process: process, cmd: cmd, expectedAppID: req.ExpectedAppID, expectedTitle: req.ExpectedTitle}
	if req.SessionID != "" {
		session := b.sessions[req.SessionID]
		session.LastUsedAt = now
		b.sessions[req.SessionID] = session
	}
	b.mu.Unlock()

	go b.waitLaunch(launchID, cmd)
	resp := schema.LaunchAppResponse{LaunchID: launchID, SessionID: req.SessionID, PID: cmd.Process.Pid}
	if req.WaitSurface {
		timeout := time.Duration(req.WaitTimeoutMs) * time.Millisecond
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		if surface, ok := b.waitForLaunchSurface(launchID, timeout); ok {
			resp.Surface = &surface
		}
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
	}
	for _, surfaceID := range surfaces {
		_ = b.CloseSurface(surfaceID)
	}
	return schema.TerminateLaunchResponse{LaunchID: launchID, SignalSent: signalSent, ClosedSurfaces: surfaces}, nil
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
	}
}

func (b *Bridge) waitForLaunchSurface(launchID string, timeout time.Duration) (schema.CompositorTrackedSurface, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b.mu.RLock()
		launch := b.launches[launchID]
		if launch != nil {
			for surfaceID, boundLaunchID := range b.surfaceLaunch {
				if boundLaunchID != launchID {
					continue
				}
				if surface, ok := b.surfaces[surfaceID]; ok {
					b.mu.RUnlock()
					return surface, true
				}
			}
		}
		b.mu.RUnlock()
		time.Sleep(50 * time.Millisecond)
	}
	return schema.CompositorTrackedSurface{}, false
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
			int(surface.Client.PID) == launch.process.PID {
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
	b.mu.Lock()
	if _, ok := b.surfaces[req.SurfaceID]; !ok {
		b.mu.Unlock()
		return schema.CaptureSurfaceResponse{}, fmt.Errorf("surface %s not found", req.SurfaceID)
	}
	if b.plugin == nil {
		b.mu.Unlock()
		return schema.CaptureSurfaceResponse{}, fmt.Errorf("no plugin connected")
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
		if pluginResp.Error == "" {
			pluginResp.Error = "capture failed"
		}
		return schema.CaptureSurfaceResponse{}, fmt.Errorf("%s", pluginResp.Error)
	}
	if pluginResp.Format != "png" {
		return schema.CaptureSurfaceResponse{}, fmt.Errorf("unsupported capture response format %q", pluginResp.Format)
	}

	png, err := base64.StdEncoding.DecodeString(pluginResp.DataBase64)
	if err != nil {
		return schema.CaptureSurfaceResponse{}, fmt.Errorf("decode capture png: %w", err)
	}
	if err := os.MkdirAll("/run/agent-os/captures", 0755); err != nil {
		return schema.CaptureSurfaceResponse{}, err
	}
	path := filepath.Join("/run/agent-os/captures", requestID+".png")
	if err := os.WriteFile(path, png, 0644); err != nil {
		return schema.CaptureSurfaceResponse{}, err
	}
	sum := sha256.Sum256(png)
	return schema.CaptureSurfaceResponse{
		SurfaceID: pluginResp.SurfaceID,
		Path:      path,
		Width:     pluginResp.Width,
		Height:    pluginResp.Height,
		Format:    pluginResp.Format,
		SHA256:    hex.EncodeToString(sum[:]),
	}, nil
}

func (b *Bridge) InjectInput(req schema.InjectInputRequest) (schema.InjectInputResponse, error) {
	b.mu.Lock()
	if _, ok := b.surfaces[req.SurfaceID]; !ok {
		b.mu.Unlock()
		return schema.InjectInputResponse{}, fmt.Errorf("surface %s not found", req.SurfaceID)
	}
	if b.plugin == nil {
		b.mu.Unlock()
		return schema.InjectInputResponse{}, fmt.Errorf("no plugin connected")
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
		return schema.InjectInputResponse{}, fmt.Errorf("%s", pluginResp.Error)
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

func (b *Bridge) CloseSurface(surfaceID string) error {
	return b.sendToPlugin(schema.CompositorCloseSurface{
		Type:      schema.PluginMessageCloseSurface,
		SurfaceID: surfaceID,
	})
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

func (b *Bridge) dispatch(peerUID uint32, req schema.Request) (schema.Response, error) {
	_ = peerUID // peer identity is reserved for future governance; local agents may use this API in Phase D.
	switch req.Method {
	case schema.MethodListSurfaces:
		return okResponse(schema.ListSurfacesResponse{Surfaces: b.ListSurfaces()}), nil
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
		resp, err := b.LaunchApp(body)
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
	case schema.MethodTerminateLaunch:
		var body schema.TerminateLaunchRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.LaunchID == "" {
			return schema.Response{}, fmt.Errorf("launch_id is required")
		}
		resp, err := b.TerminateLaunch(body.LaunchID)
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
	case schema.MethodCloseSurface:
		var body schema.CloseSurfaceRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if body.SurfaceID == "" {
			return schema.Response{}, fmt.Errorf("surface_id is required")
		}
		if err := b.CloseSurface(body.SurfaceID); err != nil {
			return schema.Response{}, err
		}
		return okResponse(schema.CloseSurfacesResponse{Queued: 1}), nil
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

func (b *Bridge) handleSurfaceEvent(msg schema.CompositorPluginEvent) {
	tracked := schema.CompositorTrackedSurface{
		Surface:   msg.Surface,
		Client:    msg.Client,
		LastEvent: msg.Event,
		Device:    msg.Device,
		UpdatedAt: time.Now(),
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
		b.surfaces[msg.Surface.ID] = tracked
		b.associateSurfaceLocked(tracked)
		if err := b.syncDerivedPolicyLocked(msg.Surface.ID); err != nil {
			log.Printf("sync compositor policy for %s: %v", msg.Surface.ID, err)
		}
	case schema.SurfaceEventFocused, schema.SurfaceEventInputDenied:
		b.surfaces[msg.Surface.ID] = tracked
		b.associateSurfaceLocked(tracked)
		if _, ok := b.policies[msg.Surface.ID]; !ok {
			if err := b.syncDerivedPolicyLocked(msg.Surface.ID); err != nil {
				log.Printf("sync compositor policy for %s: %v", msg.Surface.ID, err)
			}
		}
	case schema.SurfaceEventUnmapped:
		delete(b.surfaces, msg.Surface.ID)
		delete(b.surfaceLaunch, msg.Surface.ID)
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
	b.mu.Unlock()

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

func launchCredential(req schema.LaunchAppRequest) *syscall.Credential {
	var uid, gid *uint32
	uid = req.RunAsUID
	gid = req.RunAsGID
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
		return nil
	}
	cred := &syscall.Credential{}
	if uid != nil {
		cred.Uid = *uid
	}
	if gid != nil {
		cred.Gid = *gid
	}
	return cred
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

func writeError(conn net.Conn, msg string) {
	b, _ := json.Marshal(msg)
	resp := schema.Response{OK: false, Body: b}
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("write compositor error response: %v", err)
	}
}
