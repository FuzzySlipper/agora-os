package compositor

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
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

type Bridge struct {
	bus              publisher
	allowedPluginUID uint32
	grantStore       *grantStore

	mu       sync.RWMutex
	plugin   *pluginSession
	surfaces map[string]schema.CompositorTrackedSurface
	policies map[string]schema.CompositorSurfacePolicy
	grants   map[string]map[uint32]schema.SurfaceAccessGrant
	actorUID *uint32
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
		if msg.Type != schema.PluginMessageSurfaceEvent {
			continue
		}
		b.handleSurfaceEvent(msg)
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
	switch req.Method {
	case schema.MethodListSurfaces:
		if peerUID != 0 {
			return schema.Response{}, fmt.Errorf("list_surfaces requires root")
		}
		return okResponse(schema.ListSurfacesResponse{Surfaces: b.ListSurfaces()}), nil
	case schema.MethodUpsertSurfacePolicy:
		if peerUID != 0 {
			return schema.Response{}, fmt.Errorf("upsert_surface_policy requires root")
		}
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
		if peerUID != 0 {
			return schema.Response{}, fmt.Errorf("remove_surface_policy requires root")
		}
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
		if peerUID != 0 {
			return schema.Response{}, fmt.Errorf("set_input_context requires root")
		}
		var body schema.SetInputContextRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
		if err := b.SetInputContext(body.ActorUID); err != nil {
			return schema.Response{}, err
		}
		return okResponse("updated"), nil
	case schema.MethodCloseSurface:
		if peerUID != 0 {
			return schema.Response{}, fmt.Errorf("close_surface requires root")
		}
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
		if peerUID != 0 {
			return schema.Response{}, fmt.Errorf("close_surfaces_by_uid requires root")
		}
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
		if peerUID != 0 {
			return schema.Response{}, fmt.Errorf("grant_viewport requires root")
		}
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
		if peerUID != 0 {
			return schema.Response{}, fmt.Errorf("revoke_viewport requires root")
		}
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

func (b *Bridge) handleSurfaceEvent(msg schema.CompositorPluginEvent) {
	tracked := schema.CompositorTrackedSurface{
		Surface:   msg.Surface,
		Client:    msg.Client,
		LastEvent: msg.Event,
		Device:    msg.Device,
		UpdatedAt: time.Now(),
	}

	var (
		topic   string
		busBody schema.CompositorBusEvent
		sendMsg any
		session *pluginSession
	)

	b.mu.Lock()
	switch msg.Event {
	case schema.SurfaceEventMapped, schema.SurfaceEventFocused, schema.SurfaceEventInputDenied:
		b.surfaces[msg.Surface.ID] = tracked
		policy := b.rebuildSurfacePolicyLocked(msg.Surface.ID)
		sendMsg = schema.CompositorPolicyUpsert{Type: schema.PluginMessagePolicyUpsert, Surface: policy}
		session = b.plugin
	case schema.SurfaceEventUnmapped:
		delete(b.surfaces, msg.Surface.ID)
		delete(b.policies, msg.Surface.ID)
		delete(b.grants, msg.Surface.ID)
		sendMsg = schema.CompositorPolicyRemove{Type: schema.PluginMessagePolicyRemove, SurfaceID: msg.Surface.ID}
		session = b.plugin
	default:
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	topic = topicForSurfaceEvent(msg.Event)
	if topic != "" {
		busBody = schema.CompositorBusEvent{
			Surface: msg.Surface,
			Client:  msg.Client,
			Event:   msg.Event,
			Device:  msg.Device,
		}
		if err := b.bus.Publish(topic, busBody); err != nil {
			log.Printf("publish compositor event %s: %v", topic, err)
		}
	}

	if session != nil {
		if err := session.Send(sendMsg); err != nil {
			log.Printf("sync compositor policy for %s: %v", msg.Surface.ID, err)
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
