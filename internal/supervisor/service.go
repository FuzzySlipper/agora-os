// Package supervisor implements the request dispatch and authorization layer for
// the agent supervisor service. It manages worker lease state, enforces profile
// grants and budgets, and coordinates with the isolation service and event bus.
package supervisor

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync/atomic"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/peercred"
	"github.com/patch/agora-os/internal/schema"
)

// --- Supervisor method constants ---
// These are supervisor-specific and kept unexported; they are dispatched by
// HandleConn and should not appear in the shared schema package.

const (
	methodEnsureWorker     = "ensure_worker"
	methodReleaseWorker    = "release_worker"
	methodTerminateWorker  = "terminate_worker"
	methodListWorkers      = "list_workers"
	methodDescribeProfiles = "describe_profiles"
)

// --- Dependency interfaces ---
//
// Service depends on these interfaces instead of concrete types so that
// state.go and policy.go can be built in parallel without circular imports.

// LeaseStore manages worker lease records.
type LeaseStore interface {
	Add(lease schema.WorkerLease) error
	Get(workerID string) (schema.WorkerLease, bool)
	GetByUID(agentUID uint32) (schema.WorkerLease, bool)
	ListBySession(sessionID string) []schema.WorkerLease
	UpdateState(workerID string, state schema.WorkerLeaseState) error
	Release(workerID string) error
	List() []schema.WorkerLease
	CountBySession(sessionID string) int
}

// ProfileProvider resolves worker profile names to profile descriptors.
type ProfileProvider interface {
	Get(name string) (schema.WorkerProfile, bool)
	ValidateProfile(name string) error
}

// GrantProvider resolves which profiles a requester is allowed to request.
type GrantProvider interface {
	Get(requesterUID uint32) (schema.ProfileGrant, bool)
	AllowedToRequest(requesterUID uint32, profileName string) bool
	AllowedProfiles(requesterUID uint32) []string
}

// BudgetChecker enforces per-session and per-requester budget limits.
type BudgetChecker interface {
	CheckBudget(store LeaseStore, grant schema.ProfileGrant, sessionID string, requesterUID uint32) error
}

// ReuseChecker determines whether an existing lease can be reused for a new request.
type ReuseChecker interface {
	CanReuse(lease schema.WorkerLease, req schema.EnsureWorkerRequest, profile schema.WorkerProfile) (bool, string)
}

// --- IsolationClient ---

// IsolationClient connects to the isolation service over its Unix socket and
// dispatches agent lifecycle operations via the shared schema request/response
// envelope.
type IsolationClient struct {
	socketPath string
}

// NewIsolationClient creates an IsolationClient targeting the given Unix socket.
func NewIsolationClient(socketPath string) *IsolationClient {
	return &IsolationClient{socketPath: socketPath}
}

// Spawn sends a spawn request to the isolation service and returns the new
// agent info.
func (c *IsolationClient) Spawn(req schema.SpawnAgentRequest) (*schema.AgentInfo, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("isolation dial: %w", err)
	}
	defer conn.Close()

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal spawn request: %w", err)
	}

	if err := json.NewEncoder(conn).Encode(schema.Request{
		Method: schema.MethodSpawnAgent,
		Body:   body,
	}); err != nil {
		return nil, fmt.Errorf("send spawn request: %w", err)
	}

	var resp schema.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode spawn response: %w", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("isolation: %s", string(resp.Body))
	}

	var spawnResp schema.SpawnAgentResponse
	if err := json.Unmarshal(resp.Body, &spawnResp); err != nil {
		return nil, fmt.Errorf("unmarshal spawn response body: %w", err)
	}
	if spawnResp.Error != "" {
		return nil, fmt.Errorf("isolation: %s", spawnResp.Error)
	}
	return &spawnResp.Agent, nil
}

// Terminate sends a terminate request to the isolation service by agent UID.
func (c *IsolationClient) Terminate(uid uint32) error {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("isolation dial: %w", err)
	}
	defer conn.Close()

	body, err := json.Marshal(schema.TerminateAgentRequest{UID: uid})
	if err != nil {
		return fmt.Errorf("marshal terminate request: %w", err)
	}

	if err := json.NewEncoder(conn).Encode(schema.Request{
		Method: schema.MethodTerminateAgent,
		Body:   body,
	}); err != nil {
		return fmt.Errorf("send terminate request: %w", err)
	}

	var resp schema.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("decode terminate response: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("isolation: %s", string(resp.Body))
	}
	return nil
}

// List retrieves the list of agents from the isolation service.
func (c *IsolationClient) List() ([]schema.AgentInfo, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("isolation dial: %w", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(schema.Request{
		Method: schema.MethodListAgents,
	}); err != nil {
		return nil, fmt.Errorf("send list request: %w", err)
	}

	var resp schema.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode list response: %w", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("isolation: %s", string(resp.Body))
	}

	var listResp schema.ListAgentsResponse
	if err := json.Unmarshal(resp.Body, &listResp); err != nil {
		return nil, fmt.Errorf("unmarshal list response body: %w", err)
	}
	return listResp.Agents, nil
}

// --- BusClient ---

// BusClient publishes lifecycle events to the event bus.
type BusClient struct {
	socketPath string
}

// NewBusClient creates a BusClient targeting the given bus Unix socket.
func NewBusClient(socketPath string) *BusClient {
	return &BusClient{socketPath: socketPath}
}

// Publish sends an event on the given topic. Since the supervisor runs as root,
// its Unix socket connection is peer-credentialed as uid 0, so the bus broker
// accepts the publication as privileged without any SenderUID override.
func (c *BusClient) Publish(topic string, body any) {
	client, err := bus.Dial(c.socketPath)
	if err != nil {
		log.Printf("bus dial: %v", err)
		return
	}
	defer client.Close()
	if err := client.Publish(topic, body); err != nil {
		log.Printf("bus publish %s: %v", topic, err)
	}
}

// --- PeerCredProvider ---

// PeerCredProvider resolves the kernel-verified UID of a Unix socket peer.
type PeerCredProvider interface {
	PeerUID(conn net.Conn) (uint32, error)
}

type peerCredProvider struct{}

func (peerCredProvider) PeerUID(conn net.Conn) (uint32, error) {
	return peercred.PeerUID(conn)
}

// NewPeerCredProvider returns the default PeerCredProvider backed by
// SO_PEERCRED.
func NewPeerCredProvider() PeerCredProvider {
	return peerCredProvider{}
}

// --- Service ---

// Service handles supervisor requests. It wraps lease state, profile/grant
// registries, isolation and bus clients, and provides request dispatch.
type Service struct {
	store        LeaseStore
	profiles     ProfileProvider
	grants       GrantProvider
	budgeter     BudgetChecker
	reuser       ReuseChecker
	isoClient    *IsolationClient
	busClient    *BusClient
	peerCreds    PeerCredProvider
	nextWorkerID atomic.Int64
}

// New creates a Service with the given dependencies.
func New(
	store LeaseStore,
	profiles ProfileProvider,
	grants GrantProvider,
	budgeter BudgetChecker,
	reuser ReuseChecker,
	isoClient *IsolationClient,
	busClient *BusClient,
	peerCreds PeerCredProvider,
) *Service {
	return &Service{
		store:     store,
		profiles:  profiles,
		grants:    grants,
		budgeter:  budgeter,
		reuser:    reuser,
		isoClient: isoClient,
		busClient: busClient,
		peerCreds: peerCreds,
	}
}

// HandleConn reads a single request from conn, authorizes it against the peer's
// kernel-verified UID, dispatches it, and writes the response. This follows the
// same pattern as isolation.Service.HandleConn.
func (s *Service) HandleConn(conn net.Conn) {
	defer conn.Close()

	peerUID, err := s.peerCreds.PeerUID(conn)
	if err != nil {
		writeError(conn, fmt.Sprintf("peer credentials: %v", err))
		return
	}

	var req schema.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeError(conn, fmt.Sprintf("decode: %v", err))
		return
	}

	var resp schema.Response
	switch req.Method {
	case methodEnsureWorker:
		resp, err = s.HandleEnsureWorker(peerUID, req.Body)
	case methodReleaseWorker:
		resp, err = s.HandleReleaseWorker(peerUID, req.Body)
	case methodTerminateWorker:
		resp, err = s.HandleTerminateWorker(peerUID, req.Body)
	case methodListWorkers:
		resp, err = s.HandleListWorkers(peerUID, req.Body)
	case methodDescribeProfiles:
		resp, err = s.HandleDescribeProfiles(peerUID, req.Body)
	default:
		writeError(conn, fmt.Sprintf("unknown method: %s", req.Method))
		return
	}

	if err != nil {
		writeError(conn, err.Error())
		return
	}
	json.NewEncoder(conn).Encode(resp)
}

// --- Request Handlers ---

// HandleEnsureWorker validates a worker profile request against grants and
// budgets, attempts to reuse an existing compatible lease, or spawns a new
// agent via the isolation service.
func (s *Service) HandleEnsureWorker(peerUID uint32, body json.RawMessage) (schema.Response, error) {
	var req schema.EnsureWorkerRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return schema.Response{}, fmt.Errorf("bad body: %w", err)
	}

	// --- Validate required fields ---
	if req.RequestID == "" {
		return schema.Response{}, fmt.Errorf("request_id is required")
	}
	if req.SessionID == "" {
		return schema.Response{}, fmt.Errorf("session_id is required")
	}
	if req.WorkerProfile == "" {
		return schema.Response{}, fmt.Errorf("worker_profile is required")
	}

	// --- Resolve profile ---
	if err := s.profiles.ValidateProfile(req.WorkerProfile); err != nil {
		return schema.Response{}, fmt.Errorf("invalid profile: %w", err)
	}
	profile, ok := s.profiles.Get(req.WorkerProfile)
	if !ok {
		return schema.Response{}, fmt.Errorf("unknown profile: %s", req.WorkerProfile)
	}

	// --- Check grant ---
	if !s.grants.AllowedToRequest(peerUID, req.WorkerProfile) {
		return schema.Response{}, fmt.Errorf("uid %d not authorized for profile %q", peerUID, req.WorkerProfile)
	}
	grant, ok := s.grants.Get(peerUID)
	if !ok {
		return schema.Response{}, fmt.Errorf("no grant for uid %d", peerUID)
	}

	// --- Check profile lease seconds vs grant limit ---
	if grant.MaxLeaseSeconds > 0 && profile.MaxLeaseSeconds > grant.MaxLeaseSeconds {
		return schema.Response{}, fmt.Errorf("profile %q requests %d max lease seconds, grant allows %d",
			profile.Profile, profile.MaxLeaseSeconds, grant.MaxLeaseSeconds)
	}

	// --- Check budget ---
	if err := s.budgeter.CheckBudget(s.store, grant, req.SessionID, peerUID); err != nil {
		return schema.Response{}, fmt.Errorf("budget exceeded: %w", err)
	}

	// --- Attempt reuse ---
	for _, lease := range s.store.ListBySession(req.SessionID) {
		reusable, _ := s.reuser.CanReuse(lease, req, profile)
		if reusable {
			if err := s.store.UpdateState(lease.WorkerID, schema.LeaseRunning); err != nil {
				return schema.Response{}, fmt.Errorf("update lease state: %w", err)
			}
			// Re-fetch the lease to get the updated state
			updatedLease, found := s.store.Get(lease.WorkerID)
			if !found {
				return schema.Response{}, fmt.Errorf("lease disappeared: %s", lease.WorkerID)
			}
			s.busClient.Publish(schema.TopicAgentLifecycleReused, schema.WorkerLifecycleEvent{
				SessionID: req.SessionID,
				Lease:     updatedLease,
			})
			return okResponse(schema.EnsureWorkerResponse{
				Assignment: schema.WorkerAssignment{
					WorkerID:        updatedLease.WorkerID,
					Created:         false,
					Profile:         updatedLease.Profile,
					LeaseExpiresAt:  updatedLease.LeaseExpiresAt,
					AssignmentTopic: updatedLease.AssignmentTopic,
				},
			}), nil
		}
	}

	// --- Spawn new agent ---
	workerID := fmt.Sprintf("worker_%d", s.nextWorkerID.Add(1))
	spawnReq := workerProfileToSpawnRequest(req.WorkerProfile, profile)
	agent, err := s.isoClient.Spawn(spawnReq)
	if err != nil {
		return schema.Response{}, fmt.Errorf("spawn agent: %w", err)
	}

	assignmentTopic := fmt.Sprintf("agent.work.assign.%s", workerID)
	lease := schema.WorkerLease{
		WorkerID:        workerID,
		AgentUID:        agent.UID,
		Profile:         req.WorkerProfile,
		OwnerSessionID:  req.SessionID,
		RequesterUID:    peerUID,
		AssignmentTopic: assignmentTopic,
		State:           schema.LeaseRunning,
	}

	if err := s.store.Add(lease); err != nil {
		// Best-effort terminate the spawned agent since we cannot track the lease
		_ = s.isoClient.Terminate(agent.UID)
		return schema.Response{}, fmt.Errorf("store lease: %w", err)
	}

	s.busClient.Publish(schema.TopicAgentLifecycleSpawned, schema.WorkerLifecycleEvent{
		SessionID: req.SessionID,
		Lease:     lease,
	})

	return okResponse(schema.EnsureWorkerResponse{
		Assignment: schema.WorkerAssignment{
			WorkerID:        workerID,
			Created:         true,
			Agent:           *agent,
			Profile:         req.WorkerProfile,
			AssignmentTopic: assignmentTopic,
		},
	}), nil
}

// HandleReleaseWorker releases a worker lease, publishes a termination event,
// and terminates the underlying isolation agent.
func (s *Service) HandleReleaseWorker(peerUID uint32, body json.RawMessage) (schema.Response, error) {
	var req schema.ReleaseWorkerRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return schema.Response{}, fmt.Errorf("bad body: %w", err)
	}

	lease, ok := s.store.Get(req.WorkerID)
	if !ok {
		return schema.Response{}, fmt.Errorf("worker not found: %s", req.WorkerID)
	}

	if lease.OwnerSessionID != req.SessionID {
		return schema.Response{}, fmt.Errorf("session %q does not own lease %q", req.SessionID, req.WorkerID)
	}

	if err := s.store.Release(req.WorkerID); err != nil {
		return schema.Response{}, fmt.Errorf("release lease: %w", err)
	}

	// Re-fetch the lease to get the updated state for the event
	updatedLease, _ := s.store.Get(req.WorkerID)
	s.busClient.Publish(schema.TopicAgentLifecycleTerminated, schema.WorkerLifecycleEvent{
		SessionID: req.SessionID,
		Lease:     updatedLease,
	})

	if err := s.isoClient.Terminate(lease.AgentUID); err != nil {
		log.Printf("terminate isolation agent %d: %v", lease.AgentUID, err)
		// Non-fatal: lease is already released
	}

	return okResponse(schema.ReleaseWorkerResponse{Released: true}), nil
}

// HandleTerminateWorker forcefully terminates a worker. Admin (uid 0) can
// terminate any worker; otherwise the caller must own the session.
func (s *Service) HandleTerminateWorker(peerUID uint32, body json.RawMessage) (schema.Response, error) {
	var req schema.TerminateWorkerSupervisorRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return schema.Response{}, fmt.Errorf("bad body: %w", err)
	}

	lease, ok := s.store.Get(req.WorkerID)
	if !ok {
		return schema.Response{}, fmt.Errorf("worker not found: %s", req.WorkerID)
	}

	// Admin (uid 0) or owning session can terminate
	if peerUID != 0 && lease.OwnerSessionID != req.SessionID {
		return schema.Response{}, fmt.Errorf("not authorized to terminate worker %q", req.WorkerID)
	}

	if err := s.store.UpdateState(req.WorkerID, schema.LeaseTerminated); err != nil {
		return schema.Response{}, fmt.Errorf("update lease state: %w", err)
	}

	updatedLease, _ := s.store.Get(req.WorkerID)
	s.busClient.Publish(schema.TopicAgentLifecycleTerminated, schema.WorkerLifecycleEvent{
		SessionID: req.SessionID,
		Lease:     updatedLease,
	})

	if err := s.isoClient.Terminate(lease.AgentUID); err != nil {
		log.Printf("terminate isolation agent %d: %v", lease.AgentUID, err)
	}

	return okResponse(schema.TerminateWorkerSupervisorResponse{Terminated: true}), nil
}

// HandleListWorkers returns workers visible to the requester. If a session_id
// is provided in the request, the result is filtered to that session.
func (s *Service) HandleListWorkers(peerUID uint32, body json.RawMessage) (schema.Response, error) {
	var req schema.ListWorkersRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return schema.Response{}, fmt.Errorf("bad body: %w", err)
		}
	}

	var workers []schema.WorkerLease
	if req.SessionID != "" {
		workers = s.store.ListBySession(req.SessionID)
	} else {
		// Non-admin callers only see their own workers
		all := s.store.List()
		if peerUID == 0 {
			workers = all
		} else {
			for _, w := range all {
				if w.RequesterUID == peerUID {
					workers = append(workers, w)
				}
			}
		}
	}

	return okResponse(schema.ListWorkersResponse{Workers: workers}), nil
}

// HandleDescribeProfiles returns the profiles that the requester is allowed to
// request, determined by their grant.
func (s *Service) HandleDescribeProfiles(peerUID uint32, body json.RawMessage) (schema.Response, error) {
	allowed := s.grants.AllowedProfiles(peerUID)
	profiles := make([]schema.WorkerProfile, 0, len(allowed))
	for _, name := range allowed {
		if p, ok := s.profiles.Get(name); ok {
			profiles = append(profiles, p)
		}
	}
	return okResponse(schema.DescribeProfilesResponse{Profiles: profiles}), nil
}

// --- Helpers ---

// workerProfileToSpawnRequest translates a supervisor-level WorkerProfile into
// an isolation service SpawnAgentRequest.
func workerProfileToSpawnRequest(name string, profile schema.WorkerProfile) schema.SpawnAgentRequest {
	return schema.SpawnAgentRequest{
		Name:       name + "-" + generateShortSuffix(),
		CPUQuota:   profile.CPUQuota,
		MemoryMax:  profile.MemoryMax,
		NetAccess:  profile.NetAccess,
		WatchPaths: profile.WatchPaths,
	}
}

// generateShortSuffix returns a 4-character hex string for use in agent names.
func generateShortSuffix() string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// okResponse wraps a body value into a successful schema.Response.
func okResponse(body any) schema.Response {
	b, _ := json.Marshal(body)
	return schema.Response{OK: true, Body: b}
}

// writeError writes an error response to the connection.
func writeError(conn net.Conn, msg string) {
	b, _ := json.Marshal(msg)
	resp := schema.Response{OK: false, Body: b}
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("write error response: %v", err)
	}
}
