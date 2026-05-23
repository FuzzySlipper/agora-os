package supervisor

import (
	"testing"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

// ---------------------------------------------------------------------------
// ProfileRegistry tests
// ---------------------------------------------------------------------------

func TestProfileRegistry_Empty(t *testing.T) {
	r, err := NewProfileRegistry(nil)
	if err != nil {
		t.Fatalf("NewProfileRegistry(nil) err = %v", err)
	}
	if _, ok := r.Get("nonexistent"); ok {
		t.Error("Get on empty registry should return false")
	}
	if len(r.List()) != 0 {
		t.Error("List on empty registry should be empty")
	}
	if err := r.ValidateProfile("x"); err == nil {
		t.Error("ValidateProfile on empty registry should return error")
	}
}

func TestProfileRegistry_Duplicate(t *testing.T) {
	profiles := []schema.WorkerProfile{
		{Profile: "dupe", Runtime: schema.RuntimeDeterministic},
		{Profile: "dupe", Runtime: schema.RuntimeLocalLLM},
	}
	_, err := NewProfileRegistry(profiles)
	if err == nil {
		t.Fatal("expected error for duplicate profile name")
	}
}

func TestProfileRegistry_Get(t *testing.T) {
	r := mustRegistry(t, []schema.WorkerProfile{
		{Profile: "alpha", Runtime: schema.RuntimeDeterministic, CPUQuota: "50%", MemoryMax: "256M", NetAccess: schema.NetDeny, MaxLeaseSeconds: 300},
		{Profile: "beta", Runtime: schema.RuntimeLocalLLM, NetAccess: schema.NetLocalOnly, MaxLeaseSeconds: 600},
	})

	p, ok := r.Get("alpha")
	if !ok {
		t.Fatal("Get('alpha') should be found")
	}
	if p.Profile != "alpha" {
		t.Errorf("expected profile 'alpha', got %q", p.Profile)
	}
	if p.Runtime != schema.RuntimeDeterministic {
		t.Errorf("expected runtime %q, got %q", schema.RuntimeDeterministic, p.Runtime)
	}
	if p.CPUQuota != "50%" {
		t.Errorf("expected cpu_quota %q, got %q", "50%", p.CPUQuota)
	}
	if p.NetAccess != schema.NetDeny {
		t.Errorf("expected net_access %q, got %q", schema.NetDeny, p.NetAccess)
	}

	if _, ok := r.Get("nonexistent"); ok {
		t.Error("Get('nonexistent') should not be found")
	}
}

func TestProfileRegistry_List(t *testing.T) {
	profiles := []schema.WorkerProfile{
		{Profile: "a", Runtime: schema.RuntimeDeterministic},
		{Profile: "b", Runtime: schema.RuntimeLocalLLM},
	}
	r := mustRegistry(t, profiles)

	got := r.List()
	if len(got) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(got))
	}

	// Ensure both names are present (order is not deterministic)
	names := make(map[string]bool)
	for _, p := range got {
		names[p.Profile] = true
	}
	if !names["a"] || !names["b"] {
		t.Errorf("missing expected profiles in list: %v", names)
	}
}

func TestProfileRegistry_ValidateProfile(t *testing.T) {
	r := mustRegistry(t, []schema.WorkerProfile{
		{Profile: "known", Runtime: schema.RuntimeDeterministic},
	})

	if err := r.ValidateProfile("known"); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if err := r.ValidateProfile("unknown"); err == nil {
		t.Error("expected error for unknown profile")
	}
}

// ---------------------------------------------------------------------------
// GrantRegistry tests
// ---------------------------------------------------------------------------

func TestGrantRegistry_Empty(t *testing.T) {
	g := NewGrantRegistry(nil)
	if _, ok := g.Get(1); ok {
		t.Error("Get on empty registry should return false")
	}
	if p := g.AllowedProfiles(1); p != nil {
		t.Errorf("AllowedProfiles on empty should be nil, got %v", p)
	}
	if g.AllowedToRequest(1, "anything") {
		t.Error("AllowedToRequest on empty should be false")
	}
}

func TestGrantRegistry_Get(t *testing.T) {
	grants := []schema.ProfileGrant{
		{RequesterUID: 100, AllowedProfiles: []string{"alpha", "beta"}, MaxConcurrentWorkers: 3, MaxLeaseSeconds: 600},
		{RequesterUID: 200, AllowedProfiles: []string{"gamma"}, MaxConcurrentWorkers: 1},
	}
	g := NewGrantRegistry(grants)

	grant, ok := g.Get(100)
	if !ok {
		t.Fatal("Get(100) should be found")
	}
	if grant.MaxConcurrentWorkers != 3 {
		t.Errorf("expected MaxConcurrentWorkers=3, got %d", grant.MaxConcurrentWorkers)
	}
	if grant.MaxLeaseSeconds != 600 {
		t.Errorf("expected MaxLeaseSeconds=600, got %d", grant.MaxLeaseSeconds)
	}
	if len(grant.AllowedProfiles) != 2 {
		t.Errorf("expected 2 allowed profiles, got %d", len(grant.AllowedProfiles))
	}

	if _, ok := g.Get(999); ok {
		t.Error("Get(999) should not be found")
	}
}

func TestGrantRegistry_AllowedProfiles(t *testing.T) {
	g := NewGrantRegistry([]schema.ProfileGrant{
		{RequesterUID: 10, AllowedProfiles: []string{"foo", "bar"}},
	})

	profiles := g.AllowedProfiles(10)
	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}
	// Verify it's a copy
	profiles[0] = "mutated"
	second := g.AllowedProfiles(10)
	if second[0] == "mutated" {
		t.Error("AllowedProfiles should return a copy")
	}

	if p := g.AllowedProfiles(999); p != nil {
		t.Error("AllowedProfiles for unknown requester should be nil")
	}
}

func TestGrantRegistry_AllowedToRequest(t *testing.T) {
	g := NewGrantRegistry([]schema.ProfileGrant{
		{RequesterUID: 1, AllowedProfiles: []string{"alpha", "beta"}},
		{RequesterUID: 2, AllowedProfiles: nil}, // empty = all access
	})

	tests := []struct {
		uid     uint32
		profile string
		want    bool
	}{
		{1, "alpha", true},
		{1, "beta", true},
		{1, "gamma", false},
		{2, "anything", true},  // empty AllowedProfiles = all access
		{2, "", true},
		{999, "alpha", false},  // no grant
	}

	for _, tc := range tests {
		got := g.AllowedToRequest(tc.uid, tc.profile)
		if got != tc.want {
			t.Errorf("AllowedToRequest(%d, %q) = %v, want %v", tc.uid, tc.profile, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// CanReuse tests
// ---------------------------------------------------------------------------

func TestCanReuse_Success_ReuseSession(t *testing.T) {
	now := time.Now()
	future := now.Add(1 * time.Hour)

	lease := schema.WorkerLease{
		WorkerID:       "w1",
		Profile:        "deterministic-worker",
		OwnerSessionID: "session-1",
		LeaseExpiresAt: &future,
		State:          schema.LeaseRunning,
	}

	req := schema.EnsureWorkerRequest{
		SessionID:     "session-1",
		WorkerProfile: "deterministic-worker",
	}

	profile := schema.WorkerProfile{
		Profile:     "deterministic-worker",
		Runtime:     schema.RuntimeDeterministic,
		NetAccess:   schema.NetDeny,
		ReusePolicy: schema.ReuseSession,
	}

	ok, reason := CanReuse(lease, req, profile)
	if !ok {
		t.Fatalf("expected reusable, got false: %s", reason)
	}
	if reason != "" {
		t.Errorf("expected empty reason on success, got %q", reason)
	}
}

func TestCanReuse_Success_ReuseLease(t *testing.T) {
	now := time.Now()
	future := now.Add(1 * time.Hour)

	lease := schema.WorkerLease{
		WorkerID:       "w1",
		Profile:        "worker-a",
		OwnerSessionID: "session-old",
		LeaseExpiresAt: &future,
		State:          schema.LeaseRunning,
	}

	req := schema.EnsureWorkerRequest{
		SessionID:     "session-new",
		WorkerProfile: "worker-a",
	}

	profile := schema.WorkerProfile{
		Profile:     "worker-a",
		Runtime:     schema.RuntimeDeterministic,
		NetAccess:   schema.NetAllow,
		ReusePolicy: schema.ReuseLease,
	}

	// ReuseLease permits cross-session reuse
	ok, reason := CanReuse(lease, req, profile)
	if !ok {
		t.Fatalf("expected reusable (ReuseLease), got false: %s", reason)
	}
}

func TestCanReuse_ReusePolicyNever(t *testing.T) {
	lease := schema.WorkerLease{WorkerID: "w1", Profile: "p", OwnerSessionID: "s", State: schema.LeaseRunning}
	req := schema.EnsureWorkerRequest{SessionID: "s", WorkerProfile: "p"}
	profile := schema.WorkerProfile{Profile: "p", Runtime: schema.RuntimeDeterministic, NetAccess: schema.NetDeny, ReusePolicy: schema.ReuseNever}

	ok, reason := CanReuse(lease, req, profile)
	if ok {
		t.Fatal("expected not reusable for ReuseNever")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestCanReuse_EmptyReusePolicy(t *testing.T) {
	lease := schema.WorkerLease{WorkerID: "w1", Profile: "p", OwnerSessionID: "s", State: schema.LeaseRunning}
	req := schema.EnsureWorkerRequest{SessionID: "s", WorkerProfile: "p"}
	// Empty ReusePolicy defaults to "never"
	profile := schema.WorkerProfile{Profile: "p", Runtime: schema.RuntimeDeterministic, NetAccess: schema.NetDeny}

	ok, _ := CanReuse(lease, req, profile)
	if ok {
		t.Fatal("expected not reusable for empty ReusePolicy")
	}
}

func TestCanReuse_ProfileMismatch(t *testing.T) {
	lease := schema.WorkerLease{WorkerID: "w1", Profile: "profile-a", OwnerSessionID: "s", State: schema.LeaseRunning}
	req := schema.EnsureWorkerRequest{SessionID: "s", WorkerProfile: "profile-b"}
	profile := schema.WorkerProfile{Profile: "profile-a", Runtime: schema.RuntimeDeterministic, NetAccess: schema.NetDeny, ReusePolicy: schema.ReuseSession}

	ok, reason := CanReuse(lease, req, profile)
	if ok {
		t.Fatal("expected not reusable for profile mismatch")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestCanReuse_SessionMismatch_SessionPolicy(t *testing.T) {
	lease := schema.WorkerLease{WorkerID: "w1", Profile: "p", OwnerSessionID: "session-a", State: schema.LeaseRunning}
	req := schema.EnsureWorkerRequest{SessionID: "session-b", WorkerProfile: "p"}
	profile := schema.WorkerProfile{Profile: "p", Runtime: schema.RuntimeDeterministic, NetAccess: schema.NetDeny, ReusePolicy: schema.ReuseSession}

	ok, reason := CanReuse(lease, req, profile)
	if ok {
		t.Fatal("expected not reusable for session mismatch under ReuseSession")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestCanReuse_ExpiredLease(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour)
	lease := schema.WorkerLease{WorkerID: "w1", Profile: "p", OwnerSessionID: "s", LeaseExpiresAt: &past, State: schema.LeaseRunning}
	req := schema.EnsureWorkerRequest{SessionID: "s", WorkerProfile: "p"}
	profile := schema.WorkerProfile{Profile: "p", Runtime: schema.RuntimeDeterministic, NetAccess: schema.NetDeny, ReusePolicy: schema.ReuseSession}

	ok, reason := CanReuse(lease, req, profile)
	if ok {
		t.Fatal("expected not reusable for expired lease")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestCanReuse_Terminated(t *testing.T) {
	lease := schema.WorkerLease{WorkerID: "w1", Profile: "p", OwnerSessionID: "s", State: schema.LeaseTerminated}
	req := schema.EnsureWorkerRequest{SessionID: "s", WorkerProfile: "p"}
	profile := schema.WorkerProfile{Profile: "p", Runtime: schema.RuntimeDeterministic, NetAccess: schema.NetDeny, ReusePolicy: schema.ReuseSession}

	ok, reason := CanReuse(lease, req, profile)
	if ok {
		t.Fatal("expected not reusable for terminated lease")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestCanReuse_NotRunning(t *testing.T) {
	lease := schema.WorkerLease{WorkerID: "w1", Profile: "p", OwnerSessionID: "s", State: schema.LeaseReleased}
	req := schema.EnsureWorkerRequest{SessionID: "s", WorkerProfile: "p"}
	profile := schema.WorkerProfile{Profile: "p", Runtime: schema.RuntimeDeterministic, NetAccess: schema.NetDeny, ReusePolicy: schema.ReuseSession}

	ok, reason := CanReuse(lease, req, profile)
	if ok {
		t.Fatal("expected not reusable for non-running worker")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestCanReuse_EmptyNetAccess(t *testing.T) {
	lease := schema.WorkerLease{WorkerID: "w1", Profile: "p", OwnerSessionID: "s", State: schema.LeaseRunning}
	req := schema.EnsureWorkerRequest{SessionID: "s", WorkerProfile: "p"}
	profile := schema.WorkerProfile{Profile: "p", Runtime: schema.RuntimeDeterministic, ReusePolicy: schema.ReuseSession}

	ok, reason := CanReuse(lease, req, profile)
	if ok {
		t.Fatal("expected not reusable when profile has empty NetAccess")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestCanReuse_EmptyRuntime(t *testing.T) {
	lease := schema.WorkerLease{WorkerID: "w1", Profile: "p", OwnerSessionID: "s", State: schema.LeaseRunning}
	req := schema.EnsureWorkerRequest{SessionID: "s", WorkerProfile: "p"}
	profile := schema.WorkerProfile{Profile: "p", NetAccess: schema.NetDeny, ReusePolicy: schema.ReuseSession}

	ok, reason := CanReuse(lease, req, profile)
	if ok {
		t.Fatal("expected not reusable when profile has empty Runtime")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestCanReuse_UnknownPolicy(t *testing.T) {
	lease := schema.WorkerLease{WorkerID: "w1", Profile: "p", OwnerSessionID: "s", State: schema.LeaseRunning}
	req := schema.EnsureWorkerRequest{SessionID: "s", WorkerProfile: "p"}
	profile := schema.WorkerProfile{Profile: "p", Runtime: schema.RuntimeDeterministic, NetAccess: schema.NetDeny, ReusePolicy: "unknown"}

	ok, reason := CanReuse(lease, req, profile)
	if ok {
		t.Fatal("expected not reusable for unknown reuse policy")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestCanReuse_NilLeaseExpiry(t *testing.T) {
	// A nil LeaseExpiresAt means no expiration; the lease is still valid.
	lease := schema.WorkerLease{
		WorkerID:       "w1",
		Profile:        "p",
		OwnerSessionID: "s",
		LeaseExpiresAt: nil,
		State:          schema.LeaseRunning,
	}
	req := schema.EnsureWorkerRequest{SessionID: "s", WorkerProfile: "p"}
	profile := schema.WorkerProfile{Profile: "p", Runtime: schema.RuntimeDeterministic, NetAccess: schema.NetDeny, ReusePolicy: schema.ReuseSession}

	ok, reason := CanReuse(lease, req, profile)
	if !ok {
		t.Fatalf("expected reusable for nil expiry, got false: %s", reason)
	}
}

// ---------------------------------------------------------------------------
// CheckBudget tests
// ---------------------------------------------------------------------------

// mockLeaseCounter implements LeaseCounter for testing.
type mockLeaseCounter struct {
	sessionCount   int
	requesterNames []schema.WorkerLease
}

func (m *mockLeaseCounter) CountBySession(sessionID string) int {
	return m.sessionCount
}

func (m *mockLeaseCounter) ListByRequester(requesterUID uint32) []schema.WorkerLease {
	return m.requesterNames
}

func TestCheckBudget_Success(t *testing.T) {
	store := &mockLeaseCounter{sessionCount: 0}
	grant := schema.ProfileGrant{MaxConcurrentWorkers: 5, MaxLeaseSeconds: 600}

	err := CheckBudget(store, grant, "session-1", 100)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestCheckBudget_Success_NoLimits(t *testing.T) {
	store := &mockLeaseCounter{sessionCount: 99}
	grant := schema.ProfileGrant{} // zero values = no limits

	err := CheckBudget(store, grant, "session-1", 100)
	if err != nil {
		t.Fatalf("expected no error with zero limits, got %v", err)
	}
}

func TestCheckBudget_ConcurrentExceeded(t *testing.T) {
	store := &mockLeaseCounter{sessionCount: 5}
	grant := schema.ProfileGrant{MaxConcurrentWorkers: 5}

	err := CheckBudget(store, grant, "session-1", 100)
	if err == nil {
		t.Fatal("expected error for exceeded concurrent workers")
	}
	if err != ErrBudgetExceeded && !errorsIs(err, ErrBudgetExceeded) {
		t.Errorf("expected ErrBudgetExceeded, got %T %v", err, err)
	}
}

func TestCheckBudget_LeaseSecondsExceeded(t *testing.T) {
	store := &mockLeaseCounter{sessionCount: 0}
	grant := schema.ProfileGrant{MaxConcurrentWorkers: 5, MaxLeaseSeconds: 300}

	err := CheckBudget(store, grant, "session-1", 100)
	if err != nil {
		t.Fatalf("expected no error (lease-seconds check removed from CheckBudget), got %v", err)
	}
}

func TestCheckBudget_LimitAtBoundary(t *testing.T) {
	store := &mockLeaseCounter{sessionCount: 4}
	grant := schema.ProfileGrant{MaxConcurrentWorkers: 5, MaxLeaseSeconds: 300}

	// At boundary: 4 active < 5 max
	err := CheckBudget(store, grant, "session-1", 100)
	if err != nil {
		t.Fatalf("expected no error at boundary, got %v", err)
	}
}

func TestCheckBudget_ZeroMaxConcurrent(t *testing.T) {
	// Zero MaxConcurrentWorkers means no concurrent limit.
	store := &mockLeaseCounter{sessionCount: 100}
	grant := schema.ProfileGrant{MaxConcurrentWorkers: 0, MaxLeaseSeconds: 600}

	err := CheckBudget(store, grant, "session-1", 100)
	if err != nil {
		t.Fatalf("expected no error with MaxConcurrentWorkers=0, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustRegistry(t *testing.T, profiles []schema.WorkerProfile) *ProfileRegistry {
	t.Helper()
	r, err := NewProfileRegistry(profiles)
	if err != nil {
		t.Fatalf("NewProfileRegistry: %v", err)
	}
	return r
}

// errorsIs wraps errors.Is for Go 1.26 compatibility.
func errorsIs(err, target error) bool {
	if err == target {
		return true
	}
	// Fallback: just check wrap via string prefix since we control
	// the error types and know they're wrapped with fmt.Errorf("...%w...")
	for e := err; e != nil; {
		if e == target {
			return true
		}
		if uw, ok := e.(interface{ Unwrap() error }); ok {
			e = uw.Unwrap()
		} else {
			break
		}
	}
	return false
}
