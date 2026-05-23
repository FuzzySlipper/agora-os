package supervisor

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
)

// --- Fake Dependencies ---

type fakeLeaseStore struct {
	leases map[string]schema.WorkerLease
}

func newFakeLeaseStore() *fakeLeaseStore {
	return &fakeLeaseStore{leases: make(map[string]schema.WorkerLease)}
}

func (f *fakeLeaseStore) Add(lease schema.WorkerLease) error {
	if _, exists := f.leases[lease.WorkerID]; exists {
		return fmt.Errorf("duplicate worker id: %s", lease.WorkerID)
	}
	f.leases[lease.WorkerID] = lease
	return nil
}

func (f *fakeLeaseStore) Get(workerID string) (schema.WorkerLease, bool) {
	l, ok := f.leases[workerID]
	return l, ok
}

func (f *fakeLeaseStore) GetByUID(agentUID uint32) (schema.WorkerLease, bool) {
	for _, l := range f.leases {
		if l.AgentUID == agentUID {
			return l, true
		}
	}
	return schema.WorkerLease{}, false
}

func (f *fakeLeaseStore) ListBySession(sessionID string) []schema.WorkerLease {
	var out []schema.WorkerLease
	for _, l := range f.leases {
		if l.OwnerSessionID == sessionID {
			out = append(out, l)
		}
	}
	return out
}

func (f *fakeLeaseStore) UpdateState(workerID string, state schema.WorkerLeaseState) error {
	l, ok := f.leases[workerID]
	if !ok {
		return fmt.Errorf("worker not found: %s", workerID)
	}
	l.State = state
	f.leases[workerID] = l
	return nil
}

func (f *fakeLeaseStore) Release(workerID string) error {
	l, ok := f.leases[workerID]
	if !ok {
		return fmt.Errorf("worker not found: %s", workerID)
	}
	l.State = schema.LeaseReleased
	f.leases[workerID] = l
	return nil
}

func (f *fakeLeaseStore) List() []schema.WorkerLease {
	var out []schema.WorkerLease
	for _, l := range f.leases {
		out = append(out, l)
	}
	return out
}

func (f *fakeLeaseStore) CountBySession(sessionID string) int {
	count := 0
	for _, l := range f.leases {
		if l.OwnerSessionID == sessionID {
			count++
		}
	}
	return count
}

type fakeProfileProvider struct {
	profiles map[string]schema.WorkerProfile
}

func newFakeProfileProvider() *fakeProfileProvider {
	return &fakeProfileProvider{profiles: map[string]schema.WorkerProfile{
		"coder": {
			Profile:   "coder",
			Runtime:   schema.RuntimeLocalLLM,
			CPUQuota:  "50%",
			MemoryMax: "512M",
			NetAccess: schema.NetDeny,
		},
	}}
}

func (f *fakeProfileProvider) Get(name string) (schema.WorkerProfile, bool) {
	p, ok := f.profiles[name]
	return p, ok
}

func (f *fakeProfileProvider) ValidateProfile(name string) error {
	if _, ok := f.profiles[name]; !ok {
		return fmt.Errorf("unknown profile: %s", name)
	}
	return nil
}

type fakeGrantProvider struct {
	grants map[uint32]schema.ProfileGrant
}

func newFakeGrantProvider() *fakeGrantProvider {
	return &fakeGrantProvider{grants: map[uint32]schema.ProfileGrant{
		1001: {
			RequesterUID:         1001,
			AllowedProfiles:      []string{"coder"},
			MaxConcurrentWorkers: 5,
		},
	}}
}

func (f *fakeGrantProvider) Get(requesterUID uint32) (schema.ProfileGrant, bool) {
	g, ok := f.grants[requesterUID]
	return g, ok
}

func (f *fakeGrantProvider) AllowedToRequest(requesterUID uint32, profileName string) bool {
	g, ok := f.grants[requesterUID]
	if !ok {
		return false
	}
	for _, p := range g.AllowedProfiles {
		if p == profileName {
			return true
		}
	}
	return false
}

func (f *fakeGrantProvider) AllowedProfiles(requesterUID uint32) []string {
	g, ok := f.grants[requesterUID]
	if !ok {
		return nil
	}
	return g.AllowedProfiles
}

type fakeBudgetChecker struct {
	shouldFail bool
}

func (f *fakeBudgetChecker) CheckBudget(_ LeaseStore, _ schema.ProfileGrant, _ string, _ uint32) error {
	if f.shouldFail {
		return fmt.Errorf("budget exceeded")
	}
	return nil
}

type fakeReuseChecker struct {
	canReuse bool
}

func (f *fakeReuseChecker) CanReuse(_ schema.WorkerLease, _ schema.EnsureWorkerRequest, _ schema.WorkerProfile) (bool, string) {
	if f.canReuse {
		return true, "compatible profile"
	}
	return false, "no compatible lease"
}

type fakePeerCredProvider struct {
	uid uint32
}

func (f *fakePeerCredProvider) PeerUID(_ net.Conn) (uint32, error) {
	return f.uid, nil
}

// --- Test Setup ---

type testHarness struct {
	t            *testing.T
	service      *Service
	store        *fakeLeaseStore
	profiles     *fakeProfileProvider
	grants       *fakeGrantProvider
	budgeter     *fakeBudgetChecker
	reuser       *fakeReuseChecker
	isoClient    *IsolationClient
	busClient    *BusClient
	isoSock      string
	busSock      string
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()

	busSock := startTestBus(t)
	isoSock := startFakeIsolationService(t)

	store := newFakeLeaseStore()
	profiles := newFakeProfileProvider()
	grants := newFakeGrantProvider()
	budgeter := &fakeBudgetChecker{}
	reuser := &fakeReuseChecker{}
	isoClient := NewIsolationClient(isoSock)
	busClient := NewBusClient(busSock)
	peerCreds := &fakePeerCredProvider{uid: 1001}

	svc := New(store, profiles, grants, budgeter, reuser, isoClient, busClient, peerCreds)

	return &testHarness{
		t:         t,
		service:   svc,
		store:     store,
		profiles:  profiles,
		grants:    grants,
		budgeter:  budgeter,
		reuser:    reuser,
		isoClient: isoClient,
		busClient: busClient,
		isoSock:   isoSock,
		busSock:   busSock,
	}
}

func startTestBus(t *testing.T) string {
	t.Helper()

	sock := filepath.Join(t.TempDir(), "bus.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	broker := bus.NewBrokerWithOptions(false)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _ = bus.ServeConn(c, broker) }(conn)
		}
	}()

	return sock
}

func startFakeIsolationService(t *testing.T) string {
	t.Helper()

	sock := filepath.Join(t.TempDir(), "isolation.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var req schema.Request
				if err := json.NewDecoder(c).Decode(&req); err != nil {
					return
				}
				switch req.Method {
				case schema.MethodSpawnAgent:
					_ = req // body not needed for fake
					resp := schema.SpawnAgentResponse{
						Agent: schema.AgentInfo{
							Name:      "coder-test",
							UID:       60001,
							Status:    schema.StatusRunning,
							Slice:     "agent-60001.slice",
							CreatedAt: time.Now(),
						},
					}
					b, _ := json.Marshal(resp)
					json.NewEncoder(c).Encode(schema.Response{OK: true, Body: b})
				case schema.MethodTerminateAgent:
					json.NewEncoder(c).Encode(schema.Response{OK: true, Body: json.RawMessage(`"terminated"`)})
				case schema.MethodListAgents:
					resp := schema.ListAgentsResponse{Agents: []schema.AgentInfo{}}
					b, _ := json.Marshal(resp)
					json.NewEncoder(c).Encode(schema.Response{OK: true, Body: b})
				default:
					json.NewEncoder(c).Encode(schema.Response{OK: false, Body: json.RawMessage(`"unknown method"`)})
				}
			}(conn)
		}
	}()

	return sock
}

// mustDialBus is a test helper that dials the event bus.
func mustDialBus(t *testing.T, sock string) *bus.Client {
	t.Helper()
	client, err := bus.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// --- Tests ---

func TestService_EnsureWorker_Valid(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	req := schema.EnsureWorkerRequest{
		RequestID:     "req-1",
		SessionID:     "session-1",
		WorkerProfile: "coder",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := h.service.HandleEnsureWorker(1001, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got body: %s", string(resp.Body))
	}

	var ensureResp schema.EnsureWorkerResponse
	if err := json.Unmarshal(resp.Body, &ensureResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !ensureResp.Assignment.Created {
		t.Fatal("expected Created=true for new worker")
	}
	if ensureResp.Assignment.WorkerID == "" {
		t.Fatal("expected non-empty WorkerID")
	}
	if ensureResp.Assignment.Profile != "coder" {
		t.Fatalf("expected profile 'coder', got %q", ensureResp.Assignment.Profile)
	}
}

func TestService_EnsureWorker_UnknownProfile(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	req := schema.EnsureWorkerRequest{
		RequestID:     "req-1",
		SessionID:     "session-1",
		WorkerProfile: "nonexistent",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.service.HandleEnsureWorker(1001, body)
	if err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestService_EnsureWorker_NoGrant(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	req := schema.EnsureWorkerRequest{
		RequestID:     "req-1",
		SessionID:     "session-1",
		WorkerProfile: "coder",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	// uid 9999 has no grant
	_, err = h.service.HandleEnsureWorker(9999, body)
	if err == nil {
		t.Fatal("expected error for no grant")
	}
}

func TestService_EnsureWorker_BudgetExceeded(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.budgeter.shouldFail = true

	req := schema.EnsureWorkerRequest{
		RequestID:     "req-1",
		SessionID:     "session-1",
		WorkerProfile: "coder",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.service.HandleEnsureWorker(1001, body)
	if err == nil {
		t.Fatal("expected error for budget exceeded")
	}
}

func TestService_EnsureWorker_ReusesLease(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.reuser.canReuse = true

	// Pre-create a lease for the session
	existingLease := schema.WorkerLease{
		WorkerID:        "worker_1",
		AgentUID:        60001,
		Profile:         "coder",
		OwnerSessionID:  "session-1",
		RequesterUID:    1001,
		AssignmentTopic: "agent.work.assign.worker_1",
		State:           schema.LeaseRunning,
	}
	if err := h.store.Add(existingLease); err != nil {
		t.Fatal(err)
	}

	req := schema.EnsureWorkerRequest{
		RequestID:     "req-1",
		SessionID:     "session-1",
		WorkerProfile: "coder",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := h.service.HandleEnsureWorker(1001, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %s", string(resp.Body))
	}

	var ensureResp schema.EnsureWorkerResponse
	if err := json.Unmarshal(resp.Body, &ensureResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ensureResp.Assignment.Created {
		t.Fatal("expected Created=false for reused lease")
	}
	if ensureResp.Assignment.WorkerID != "worker_1" {
		t.Fatalf("expected WorkerID 'worker_1', got %q", ensureResp.Assignment.WorkerID)
	}
}

func TestService_EnsureWorker_MissingRequestID(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	req := schema.EnsureWorkerRequest{
		SessionID:     "session-1",
		WorkerProfile: "coder",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.service.HandleEnsureWorker(1001, body)
	if err == nil {
		t.Fatal("expected error for missing request_id")
	}
}

func TestService_EnsureWorker_MissingSessionID(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	req := schema.EnsureWorkerRequest{
		RequestID:     "req-1",
		WorkerProfile: "coder",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.service.HandleEnsureWorker(1001, body)
	if err == nil {
		t.Fatal("expected error for missing session_id")
	}
}

func TestService_EnsureWorker_MissingProfile(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	req := schema.EnsureWorkerRequest{
		RequestID: "req-1",
		SessionID: "session-1",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.service.HandleEnsureWorker(1001, body)
	if err == nil {
		t.Fatal("expected error for missing worker_profile")
	}
}

func TestService_EnsureWorker_ProfileNotAllowed(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	req := schema.EnsureWorkerRequest{
		RequestID:     "req-1",
		SessionID:     "session-1",
		WorkerProfile: "coder",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	// uid 1002 has a grant but "coder" is not in its allowed profiles
	h.grants.grants[1002] = schema.ProfileGrant{
		RequesterUID:    1002,
		AllowedProfiles: []string{"reviewer"},
	}

	_, err = h.service.HandleEnsureWorker(1002, body)
	if err == nil {
		t.Fatal("expected error for profile not in grant")
	}
}

func TestService_ReleaseWorker_Valid(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	// Pre-create a lease
	lease := schema.WorkerLease{
		WorkerID:       "worker_1",
		AgentUID:       60001,
		Profile:        "coder",
		OwnerSessionID: "session-1",
		RequesterUID:   1001,
		State:          schema.LeaseRunning,
	}
	if err := h.store.Add(lease); err != nil {
		t.Fatal(err)
	}

	req := schema.ReleaseWorkerRequest{
		SessionID: "session-1",
		WorkerID:  "worker_1",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := h.service.HandleReleaseWorker(1001, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %s", string(resp.Body))
	}

	var releaseResp schema.ReleaseWorkerResponse
	if err := json.Unmarshal(resp.Body, &releaseResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !releaseResp.Released {
		t.Fatal("expected Released=true")
	}
}

func TestService_ReleaseWorker_NotFound(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	req := schema.ReleaseWorkerRequest{
		SessionID: "session-1",
		WorkerID:  "nonexistent",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.service.HandleReleaseWorker(1001, body)
	if err == nil {
		t.Fatal("expected error for nonexistent worker")
	}
}

func TestService_ReleaseWorker_WrongSession(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	lease := schema.WorkerLease{
		WorkerID:       "worker_1",
		AgentUID:       60001,
		Profile:        "coder",
		OwnerSessionID: "session-1",
		RequesterUID:   1001,
		State:          schema.LeaseRunning,
	}
	if err := h.store.Add(lease); err != nil {
		t.Fatal(err)
	}

	req := schema.ReleaseWorkerRequest{
		SessionID: "wrong-session",
		WorkerID:  "worker_1",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.service.HandleReleaseWorker(1001, body)
	if err == nil {
		t.Fatal("expected error for wrong session")
	}
}

func TestService_TerminateWorker_Valid(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	lease := schema.WorkerLease{
		WorkerID:       "worker_1",
		AgentUID:       60001,
		Profile:        "coder",
		OwnerSessionID: "session-1",
		RequesterUID:   1001,
		State:          schema.LeaseRunning,
	}
	if err := h.store.Add(lease); err != nil {
		t.Fatal(err)
	}

	req := schema.TerminateWorkerSupervisorRequest{
		SessionID: "session-1",
		WorkerID:  "worker_1",
		Reason:    "test termination",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := h.service.HandleTerminateWorker(1001, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %s", string(resp.Body))
	}

	var termResp schema.TerminateWorkerSupervisorResponse
	if err := json.Unmarshal(resp.Body, &termResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !termResp.Terminated {
		t.Fatal("expected Terminated=true")
	}

	// Verify lease state was updated
	updatedLease, ok := h.store.Get("worker_1")
	if !ok {
		t.Fatal("lease should still exist")
	}
	if updatedLease.State != schema.LeaseTerminated {
		t.Fatalf("expected state 'terminated', got %q", updatedLease.State)
	}
}

func TestService_TerminateWorker_AdminOverride(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	lease := schema.WorkerLease{
		WorkerID:       "worker_1",
		AgentUID:       60001,
		Profile:        "coder",
		OwnerSessionID: "session-1",
		RequesterUID:   1001,
		State:          schema.LeaseRunning,
	}
	if err := h.store.Add(lease); err != nil {
		t.Fatal(err)
	}

	req := schema.TerminateWorkerSupervisorRequest{
		SessionID: "admin-session",
		WorkerID:  "worker_1",
		Reason:    "admin override",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	// uid 0 (admin) can terminate any worker
	resp, err := h.service.HandleTerminateWorker(0, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %s", string(resp.Body))
	}
}

func TestService_TerminateWorker_NotFound(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	req := schema.TerminateWorkerSupervisorRequest{
		SessionID: "session-1",
		WorkerID:  "nonexistent",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.service.HandleTerminateWorker(1001, body)
	if err == nil {
		t.Fatal("expected error for nonexistent worker")
	}
}

func TestService_ListWorkers_BySession(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	leases := []schema.WorkerLease{
		{WorkerID: "worker_1", OwnerSessionID: "session-1", RequesterUID: 1001, State: schema.LeaseRunning},
		{WorkerID: "worker_2", OwnerSessionID: "session-1", RequesterUID: 1001, State: schema.LeaseRunning},
		{WorkerID: "worker_3", OwnerSessionID: "session-2", RequesterUID: 1001, State: schema.LeaseRunning},
	}
	for _, l := range leases {
		if err := h.store.Add(l); err != nil {
			t.Fatal(err)
		}
	}

	req := schema.ListWorkersRequest{SessionID: "session-1"}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := h.service.HandleListWorkers(1001, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %s", string(resp.Body))
	}

	var listResp schema.ListWorkersResponse
	if err := json.Unmarshal(resp.Body, &listResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(listResp.Workers) != 2 {
		t.Fatalf("expected 2 workers for session-1, got %d", len(listResp.Workers))
	}
}

func TestService_ListWorkers_NonAdminSeesOwn(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	leases := []schema.WorkerLease{
		{WorkerID: "worker_1", OwnerSessionID: "session-1", RequesterUID: 1001, State: schema.LeaseRunning},
		{WorkerID: "worker_2", OwnerSessionID: "session-2", RequesterUID: 1002, State: schema.LeaseRunning},
	}
	for _, l := range leases {
		if err := h.store.Add(l); err != nil {
			t.Fatal(err)
		}
	}

	// Empty body = no session filter
	resp, err := h.service.HandleListWorkers(1001, json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %s", string(resp.Body))
	}

	var listResp schema.ListWorkersResponse
	if err := json.Unmarshal(resp.Body, &listResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(listResp.Workers) != 1 {
		t.Fatalf("expected 1 worker for uid 1001, got %d", len(listResp.Workers))
	}
}

func TestService_ListWorkers_AdminSeesAll(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	leases := []schema.WorkerLease{
		{WorkerID: "worker_1", OwnerSessionID: "session-1", RequesterUID: 1001, State: schema.LeaseRunning},
		{WorkerID: "worker_2", OwnerSessionID: "session-2", RequesterUID: 1002, State: schema.LeaseRunning},
	}
	for _, l := range leases {
		if err := h.store.Add(l); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := h.service.HandleListWorkers(0, json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %s", string(resp.Body))
	}

	var listResp schema.ListWorkersResponse
	if err := json.Unmarshal(resp.Body, &listResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(listResp.Workers) != 2 {
		t.Fatalf("expected 2 workers for admin, got %d", len(listResp.Workers))
	}
}

func TestService_ListWorkers_EmptyBody(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	// Empty body (no JSON) should still work
	resp, err := h.service.HandleListWorkers(0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %s", string(resp.Body))
	}
}

func TestService_DescribeProfiles(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	resp, err := h.service.HandleDescribeProfiles(1001, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %s", string(resp.Body))
	}

	var describeResp schema.DescribeProfilesResponse
	if err := json.Unmarshal(resp.Body, &describeResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(describeResp.Profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(describeResp.Profiles))
	}
	if describeResp.Profiles[0].Profile != "coder" {
		t.Fatalf("expected 'coder' profile, got %q", describeResp.Profiles[0].Profile)
	}
}

func TestService_DescribeProfiles_NoGrant(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	resp, err := h.service.HandleDescribeProfiles(9999, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK, got: %s", string(resp.Body))
	}

	var describeResp schema.DescribeProfilesResponse
	if err := json.Unmarshal(resp.Body, &describeResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(describeResp.Profiles) != 0 {
		t.Fatalf("expected 0 profiles for uid with no grant, got %d", len(describeResp.Profiles))
	}
}

func TestService_HandleConn_DispatchEnsureWorker(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	// Create a Unix socket pair to simulate a connection
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		req := schema.Request{
			Method: methodEnsureWorker,
		}
		reqBytes, _ := json.Marshal(schema.EnsureWorkerRequest{
			RequestID:     "req-1",
			SessionID:     "session-1",
			WorkerProfile: "coder",
		})
		req.Body = reqBytes
		json.NewEncoder(server).Encode(req)

		// Read the response so HandleConn doesn't block on pipe write
		var resp schema.Response
		json.NewDecoder(server).Decode(&resp)
	}()

	// Override peer creds for this test
	h.service.peerCreds = &fakePeerCredProvider{uid: 1001}

	h.service.HandleConn(client)
}

func TestService_HandleConn_DispatchReleaseWorker(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	// Pre-create a lease the test can release
	lease := schema.WorkerLease{
		WorkerID:       "worker_1",
		AgentUID:       60001,
		Profile:        "coder",
		OwnerSessionID: "session-1",
		RequesterUID:   1001,
		State:          schema.LeaseRunning,
	}
	if err := h.store.Add(lease); err != nil {
		t.Fatal(err)
	}

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		req := schema.Request{
			Method: methodReleaseWorker,
		}
		reqBytes, _ := json.Marshal(schema.ReleaseWorkerRequest{
			SessionID: "session-1",
			WorkerID:  "worker_1",
		})
		req.Body = reqBytes
		json.NewEncoder(server).Encode(req)

		// Read the response so HandleConn doesn't block on pipe write
		var resp schema.Response
		json.NewDecoder(server).Decode(&resp)
	}()

	h.service.peerCreds = &fakePeerCredProvider{uid: 1001}
	h.service.HandleConn(client)
}

func TestService_HandleConn_DispatchUnknownMethod(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		req := schema.Request{
			Method: "nonexistent_method",
		}
		json.NewEncoder(server).Encode(req)

		// Read the response so HandleConn doesn't block on pipe write
		var resp schema.Response
		json.NewDecoder(server).Decode(&resp)
	}()

	h.service.peerCreds = &fakePeerCredProvider{uid: 1001}
	h.service.HandleConn(client)
}

func TestService_IsolationClient_Spawn(t *testing.T) {
	t.Parallel()

	isoSock := startFakeIsolationService(t)
	client := NewIsolationClient(isoSock)

	info, err := client.Spawn(schema.SpawnAgentRequest{
		Name:      "test-agent",
		CPUQuota:  "50%",
		MemoryMax: "512M",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "coder-test" {
		t.Fatalf("expected name 'coder-test', got %q", info.Name)
	}
}

func TestService_IsolationClient_Terminate(t *testing.T) {
	t.Parallel()

	isoSock := startFakeIsolationService(t)
	client := NewIsolationClient(isoSock)

	if err := client.Terminate(60001); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestService_IsolationClient_List(t *testing.T) {
	t.Parallel()

	isoSock := startFakeIsolationService(t)
	client := NewIsolationClient(isoSock)

	agents, err := client.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agents == nil {
		t.Fatal("expected non-nil list, got nil")
	}
}

func TestService_BusClient_Publish(t *testing.T) {
	t.Parallel()

	busSock := startTestBus(t)
	sub := mustDialBus(t, busSock)
	if err := sub.Subscribe("agent.lifecycle.*"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)

	client := NewBusClient(busSock)
	client.Publish(schema.TopicAgentLifecycleSpawned, schema.AgentLifecycleEvent{
		Agent: schema.AgentInfo{
			Name: "test-agent",
			UID:  60001,
		},
	})

	ev, err := sub.Receive()
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if ev.Topic != schema.TopicAgentLifecycleSpawned {
		t.Fatalf("got topic %q, want %q", ev.Topic, schema.TopicAgentLifecycleSpawned)
	}
}

func TestService_EnsureWorker_PublishesLifecycleEvent(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	sub := mustDialBus(t, h.busSock)
	if err := sub.Subscribe("agent.lifecycle.*"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)

	req := schema.EnsureWorkerRequest{
		RequestID:     "req-lifecycle",
		SessionID:     "session-lifecycle",
		WorkerProfile: "coder",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.service.HandleEnsureWorker(1001, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ev, err := sub.Receive()
	if err != nil {
		t.Fatalf("receive lifecycle event: %v", err)
	}
	if ev.Topic != schema.TopicAgentLifecycleSpawned {
		t.Fatalf("got topic %q, want %q", ev.Topic, schema.TopicAgentLifecycleSpawned)
	}

	var lifecycle schema.WorkerLifecycleEvent
	if err := json.Unmarshal(ev.Body, &lifecycle); err != nil {
		t.Fatalf("unmarshal lifecycle: %v", err)
	}
	if lifecycle.Lease.WorkerID == "" {
		t.Fatal("expected non-empty worker ID in lifecycle event")
	}
	if lifecycle.SessionID != "session-lifecycle" {
		t.Fatalf("expected session-lifecycle, got %q", lifecycle.SessionID)
	}
}

func TestService_HandleConn_BadDecode(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		// Send invalid JSON
		server.Write([]byte("not valid json\n"))
		server.Close()
	}()

	h.service.peerCreds = &fakePeerCredProvider{uid: 1001}
	h.service.HandleConn(client)
	// Should not panic -- just writes error and returns
}

func TestService_EnsureWorker_ReusePublishesReusedEvent(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)
	h.reuser.canReuse = true

	existingLease := schema.WorkerLease{
		WorkerID:        "worker_1",
		AgentUID:        60001,
		Profile:         "coder",
		OwnerSessionID:  "session-reuse",
		RequesterUID:    1001,
		AssignmentTopic: "agent.work.assign.worker_1",
		State:           schema.LeaseRunning,
	}
	if err := h.store.Add(existingLease); err != nil {
		t.Fatal(err)
	}

	sub := mustDialBus(t, h.busSock)
	if err := sub.Subscribe("agent.lifecycle.*"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)

	req := schema.EnsureWorkerRequest{
		RequestID:     "req-reuse",
		SessionID:     "session-reuse",
		WorkerProfile: "coder",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.service.HandleEnsureWorker(1001, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ev, err := sub.Receive()
	if err != nil {
		t.Fatalf("receive lifecycle event: %v", err)
	}
	if ev.Topic != schema.TopicAgentLifecycleReused {
		t.Fatalf("got topic %q, want %q", ev.Topic, schema.TopicAgentLifecycleReused)
	}
}

func TestService_ReleaseWorker_PublishesTerminatedEvent(t *testing.T) {
	t.Parallel()

	h := newTestHarness(t)

	lease := schema.WorkerLease{
		WorkerID:       "worker_term_pub",
		AgentUID:       60001,
		Profile:        "coder",
		OwnerSessionID: "session-term-pub",
		RequesterUID:   1001,
		State:          schema.LeaseRunning,
	}
	if err := h.store.Add(lease); err != nil {
		t.Fatal(err)
	}

	sub := mustDialBus(t, h.busSock)
	if err := sub.Subscribe("agent.lifecycle.*"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)

	req := schema.ReleaseWorkerRequest{
		SessionID: "session-term-pub",
		WorkerID:  "worker_term_pub",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.service.HandleReleaseWorker(1001, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ev, err := sub.Receive()
	if err != nil {
		t.Fatalf("receive lifecycle event: %v", err)
	}
	if ev.Topic != schema.TopicAgentLifecycleTerminated {
		t.Fatalf("got topic %q, want %q", ev.Topic, schema.TopicAgentLifecycleTerminated)
	}
}
