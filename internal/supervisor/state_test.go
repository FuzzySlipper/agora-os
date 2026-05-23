package supervisor

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

// testLease returns a minimal WorkerLease suitable for use in tests.
func testLease(workerID string) schema.WorkerLease {
	return schema.WorkerLease{
		WorkerID:        workerID,
		AgentUID:        1001,
		Profile:         "default",
		OwnerSessionID:  "sess-1",
		RequesterUID:    42,
		AssignmentTopic: "agent.work.assigned",
		State:           schema.LeaseRunning,
	}
}

func TestState_AddAndGet(t *testing.T) {
	s := NewWorkerLeaseStore()

	l := testLease("w-1")
	if err := s.Add(l); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	got, ok := s.Get("w-1")
	if !ok {
		t.Fatal("Get returned false for existing worker")
	}
	if got.WorkerID != "w-1" {
		t.Errorf("expected worker_id w-1, got %q", got.WorkerID)
	}
	if got.State != schema.LeaseRunning {
		t.Errorf("expected state running, got %q", got.State)
	}
}

func TestState_Add_Duplicate(t *testing.T) {
	s := NewWorkerLeaseStore()

	l1 := testLease("w-dup")
	if err := s.Add(l1); err != nil {
		t.Fatalf("first Add failed: %v", err)
	}

	l2 := testLease("w-dup")
	l2.AgentUID = 2002 // different agent but same worker_id
	err := s.Add(l2)
	if err == nil {
		t.Fatal("expected error for duplicate worker_id")
	}
	var e *ErrLeaseExists
	if !as(err, &e) {
		t.Fatalf("expected ErrLeaseExists, got %T: %v", err, err)
	}
	if e.WorkerID != "w-dup" {
		t.Errorf("expected worker_id w-dup in error, got %q", e.WorkerID)
	}
}

func TestState_Get_NotFound(t *testing.T) {
	s := NewWorkerLeaseStore()

	_, ok := s.Get("nonexistent")
	if ok {
		t.Fatal("expected false for nonexistent worker")
	}
}

func TestState_GetByUID(t *testing.T) {
	s := NewWorkerLeaseStore()

	l1 := testLease("w-1")
	l1.AgentUID = 10
	l2 := testLease("w-2")
	l2.AgentUID = 20

	if err := s.Add(l1); err != nil {
		t.Fatalf("Add w-1 failed: %v", err)
	}
	if err := s.Add(l2); err != nil {
		t.Fatalf("Add w-2 failed: %v", err)
	}

	got, ok := s.GetByUID(20)
	if !ok {
		t.Fatal("GetByUID(20) returned false")
	}
	if got.WorkerID != "w-2" {
		t.Errorf("expected worker_id w-2, got %q", got.WorkerID)
	}

	_, ok = s.GetByUID(999)
	if ok {
		t.Fatal("GetByUID(999) should return false for unknown UID")
	}
}

func TestState_ListBySession(t *testing.T) {
	s := NewWorkerLeaseStore()

	for _, id := range []string{"w-a1", "w-a2", "w-b1", "w-b2", "w-c1"} {
		l := testLease(id)
		switch id {
		case "w-b1", "w-b2":
			l.OwnerSessionID = "sess-b"
		case "w-c1":
			l.OwnerSessionID = "sess-c"
		default:
			l.OwnerSessionID = "sess-a"
		}
		if err := s.Add(l); err != nil {
			t.Fatalf("Add %s failed: %v", id, err)
		}
	}

	sessA := s.ListBySession("sess-a")
	if len(sessA) != 2 {
		t.Errorf("expected 2 leases for sess-a, got %d", len(sessA))
	}

	sessB := s.ListBySession("sess-b")
	if len(sessB) != 2 {
		t.Errorf("expected 2 leases for sess-b, got %d", len(sessB))
	}

	sessC := s.ListBySession("sess-c")
	if len(sessC) != 1 {
		t.Errorf("expected 1 lease for sess-c, got %d", len(sessC))
	}

	sessNone := s.ListBySession("sess-nonexistent")
	if len(sessNone) != 0 {
		t.Errorf("expected 0 leases for nonexistent session, got %d", len(sessNone))
	}
}

func TestState_ListByRequester(t *testing.T) {
	s := NewWorkerLeaseStore()

	for _, id := range []string{"w-1", "w-2", "w-3"} {
		l := testLease(id)
		switch id {
		case "w-3":
			l.RequesterUID = 99
		default:
			l.RequesterUID = 42
		}
		if err := s.Add(l); err != nil {
			t.Fatalf("Add %s failed: %v", id, err)
		}
	}

	by42 := s.ListByRequester(42)
	if len(by42) != 2 {
		t.Errorf("expected 2 leases for requester 42, got %d", len(by42))
	}

	by99 := s.ListByRequester(99)
	if len(by99) != 1 {
		t.Errorf("expected 1 lease for requester 99, got %d", len(by99))
	}

	by0 := s.ListByRequester(0)
	if len(by0) != 0 {
		t.Errorf("expected 0 leases for requester 0, got %d", len(by0))
	}
}

func TestState_UpdateState_ValidTransitions(t *testing.T) {
	s := NewWorkerLeaseStore()

	l := testLease("w-trans")
	if err := s.Add(l); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// running -> released
	if err := s.UpdateState("w-trans", schema.LeaseReleased); err != nil {
		t.Fatalf("running -> released: %v", err)
	}
	got, _ := s.Get("w-trans")
	if got.State != schema.LeaseReleased {
		t.Errorf("expected released, got %q", got.State)
	}

	// released -> terminated
	if err := s.UpdateState("w-trans", schema.LeaseTerminated); err != nil {
		t.Fatalf("released -> terminated: %v", err)
	}
	got, _ = s.Get("w-trans")
	if got.State != schema.LeaseTerminated {
		t.Errorf("expected terminated, got %q", got.State)
	}
}

func TestState_UpdateState_RunningToExpired(t *testing.T) {
	s := NewWorkerLeaseStore()

	l := testLease("w-exp")
	if err := s.Add(l); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if err := s.UpdateState("w-exp", schema.LeaseExpired); err != nil {
		t.Fatalf("running -> expired: %v", err)
	}
	got, _ := s.Get("w-exp")
	if got.State != schema.LeaseExpired {
		t.Errorf("expected expired, got %q", got.State)
	}
}

func TestState_UpdateState_RunningToTerminated(t *testing.T) {
	s := NewWorkerLeaseStore()

	l := testLease("w-term")
	if err := s.Add(l); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if err := s.UpdateState("w-term", schema.LeaseTerminated); err != nil {
		t.Fatalf("running -> terminated: %v", err)
	}
}

func TestState_UpdateState_InvalidTransitions(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(store *WorkerLeaseStore)
		from     schema.WorkerLeaseState
		to       schema.WorkerLeaseState
		workerID string
	}{
		{
			name:     "expired_to_released",
			workerID: "w-exp-rel",
			setup: func(s *WorkerLeaseStore) {
				l := testLease("w-exp-rel")
				l.State = schema.LeaseExpired
				s.Add(l)
			},
			from: schema.LeaseExpired,
			to:   schema.LeaseReleased,
		},
		{
			name:     "expired_to_running",
			workerID: "w-exp-run",
			setup: func(s *WorkerLeaseStore) {
				l := testLease("w-exp-run")
				l.State = schema.LeaseExpired
				s.Add(l)
			},
			from: schema.LeaseExpired,
			to:   schema.LeaseRunning,
		},
		{
			name:     "terminated_to_running",
			workerID: "w-term-run",
			setup: func(s *WorkerLeaseStore) {
				l := testLease("w-term-run")
				l.State = schema.LeaseTerminated
				s.Add(l)
			},
			from: schema.LeaseTerminated,
			to:   schema.LeaseRunning,
		},
		{
			name:     "terminated_to_released",
			workerID: "w-term-rel",
			setup: func(s *WorkerLeaseStore) {
				l := testLease("w-term-rel")
				l.State = schema.LeaseTerminated
				s.Add(l)
			},
			from: schema.LeaseTerminated,
			to:   schema.LeaseReleased,
		},
		{
			name:     "released_to_expired",
			workerID: "w-rel-exp",
			setup: func(s *WorkerLeaseStore) {
				l := testLease("w-rel-exp")
				l.State = schema.LeaseReleased
				s.Add(l)
			},
			from: schema.LeaseReleased,
			to:   schema.LeaseExpired,
		},
		{
			name:     "released_to_running",
			workerID: "w-rel-run",
			setup: func(s *WorkerLeaseStore) {
				l := testLease("w-rel-run")
				l.State = schema.LeaseReleased
				s.Add(l)
			},
			from: schema.LeaseReleased,
			to:   schema.LeaseRunning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewWorkerLeaseStore()
			tt.setup(s)

			err := s.UpdateState(tt.workerID, tt.to)
			if err == nil {
				t.Fatalf("expected error for %s -> %s", tt.from, tt.to)
			}

			var e *ErrInvalidTransition
			if !as(err, &e) {
				t.Fatalf("expected ErrInvalidTransition, got %T: %v", err, err)
			}
			if e.From != tt.from {
				t.Errorf("expected From=%q, got %q", tt.from, e.From)
			}
			if e.To != tt.to {
				t.Errorf("expected To=%q, got %q", tt.to, e.To)
			}
		})
	}
}

func TestState_UpdateState_NotFound(t *testing.T) {
	s := NewWorkerLeaseStore()

	err := s.UpdateState("nonexistent", schema.LeaseTerminated)
	if err == nil {
		t.Fatal("expected error for nonexistent worker")
	}
	var e *ErrLeaseNotFound
	if !as(err, &e) {
		t.Fatalf("expected ErrLeaseNotFound, got %T: %v", err, err)
	}
	if e.WorkerID != "nonexistent" {
		t.Errorf("expected worker_id nonexistent, got %q", e.WorkerID)
	}
}

func TestState_Release(t *testing.T) {
	s := NewWorkerLeaseStore()

	l := testLease("w-rel")
	if err := s.Add(l); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if err := s.Release("w-rel"); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	got, _ := s.Get("w-rel")
	if got.State != schema.LeaseReleased {
		t.Errorf("expected released, got %q", got.State)
	}
}

func TestState_Remove(t *testing.T) {
	s := NewWorkerLeaseStore()

	l := testLease("w-rm")
	if err := s.Add(l); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	s.Remove("w-rm")

	_, ok := s.Get("w-rm")
	if ok {
		t.Fatal("expected false after Remove")
	}

	// Remove of nonexistent should not panic.
	s.Remove("nonexistent")
}

func TestState_List(t *testing.T) {
	s := NewWorkerLeaseStore()

	if len(s.List()) != 0 {
		t.Fatal("expected empty list for fresh store")
	}

	for _, id := range []string{"w-1", "w-2", "w-3"} {
		if err := s.Add(testLease(id)); err != nil {
			t.Fatalf("Add %s failed: %v", id, err)
		}
	}

	all := s.List()
	if len(all) != 3 {
		t.Fatalf("expected 3 leases, got %d", len(all))
	}

	ids := make(map[string]bool)
	for _, l := range all {
		ids[l.WorkerID] = true
	}
	for _, id := range []string{"w-1", "w-2", "w-3"} {
		if !ids[id] {
			t.Errorf("missing worker %q in list", id)
		}
	}
}

func TestState_CountBySession(t *testing.T) {
	s := NewWorkerLeaseStore()

	// Add some running leases for sess-a
	for _, id := range []string{"w-a1", "w-a2"} {
		l := testLease(id)
		l.OwnerSessionID = "sess-a"
		l.State = schema.LeaseRunning
		if err := s.Add(l); err != nil {
			t.Fatalf("Add %s failed: %v", id, err)
		}
	}

	// Add a non-running lease for sess-a
	l := testLease("w-a3")
	l.OwnerSessionID = "sess-a"
	l.State = schema.LeaseReleased
	if err := s.Add(l); err != nil {
		t.Fatalf("Add w-a3 failed: %v", err)
	}

	// Add running leases for other sessions
	l2 := testLease("w-b1")
	l2.OwnerSessionID = "sess-b"
	l2.State = schema.LeaseRunning
	if err := s.Add(l2); err != nil {
		t.Fatalf("Add w-b1 failed: %v", err)
	}

	if n := s.CountBySession("sess-a"); n != 2 {
		t.Errorf("expected 2 running leases for sess-a, got %d", n)
	}
	if n := s.CountBySession("sess-b"); n != 1 {
		t.Errorf("expected 1 running lease for sess-b, got %d", n)
	}
	if n := s.CountBySession("nonexistent"); n != 0 {
		t.Errorf("expected 0 for nonexistent session, got %d", n)
	}
}

func TestState_ExpireLeases(t *testing.T) {
	s := NewWorkerLeaseStore()

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// Lease that should expire
	l1 := testLease("w-expires")
	futurePast := now.Add(-1 * time.Hour) // already in the past
	l1.LeaseExpiresAt = &futurePast
	if err := s.Add(l1); err != nil {
		t.Fatalf("Add w-expires failed: %v", err)
	}

	// Lease still active (expires in the future)
	l2 := testLease("w-active")
	future := now.Add(1 * time.Hour)
	l2.LeaseExpiresAt = &future
	if err := s.Add(l2); err != nil {
		t.Fatalf("Add w-active failed: %v", err)
	}

	// Lease with nil expiry (never expires)
	l3 := testLease("w-never")
	l3.LeaseExpiresAt = nil
	if err := s.Add(l3); err != nil {
		t.Fatalf("Add w-never failed: %v", err)
	}

	// Non-running lease with past expiry (should not be touched)
	l4 := testLease("w-already-released")
	past := now.Add(-2 * time.Hour)
	l4.LeaseExpiresAt = &past
	l4.State = schema.LeaseReleased
	if err := s.Add(l4); err != nil {
		t.Fatalf("Add w-already-released failed: %v", err)
	}

	expired := s.ExpireLeases(now)

	if len(expired) != 1 {
		t.Fatalf("expected 1 expired lease, got %d: %v", len(expired), expired)
	}
	if expired[0] != "w-expires" {
		t.Errorf("expected expired[0]=w-expires, got %q", expired[0])
	}

	// Verify state changes
	for _, id := range expired {
		lease, ok := s.Get(id)
		if !ok {
			t.Fatalf("expired lease %q not found in store", id)
		}
		if lease.State != schema.LeaseExpired {
			t.Errorf("expected lease %q state expired, got %q", id, lease.State)
		}
	}

	// Verify active lease unchanged
	active, _ := s.Get("w-active")
	if active.State != schema.LeaseRunning {
		t.Errorf("expected w-active to remain running, got %q", active.State)
	}

	// Verify nil-expiry lease unchanged
	never, _ := s.Get("w-never")
	if never.State != schema.LeaseRunning {
		t.Errorf("expected w-never to remain running, got %q", never.State)
	}

	// Verify non-running lease unchanged
	rel, _ := s.Get("w-already-released")
	if rel.State != schema.LeaseReleased {
		t.Errorf("expected w-already-released to remain released, got %q", rel.State)
	}

	// Second call should not return the same lease again
	expired2 := s.ExpireLeases(now)
	if len(expired2) != 0 {
		t.Errorf("expected second ExpireLeases to return 0, got %d: %v", len(expired2), expired2)
	}
}

func TestState_ExpireLeases_EmptyStore(t *testing.T) {
	s := NewWorkerLeaseStore()

	expired := s.ExpireLeases(time.Now())
	if len(expired) != 0 {
		t.Errorf("expected 0 expired leases on empty store, got %d", len(expired))
	}
}

func TestState_ConcurrentAccess(t *testing.T) {
	s := NewWorkerLeaseStore()

	const numGoroutines = 20
	const numOps = 50

	var wg sync.WaitGroup

	// Concurrent Adds
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				id := fmt.Sprintf("w-%d-%d", n, j)
				l := testLease(id)
				_ = s.Add(l) // ignore duplicates from other goroutines
			}
		}(i)
	}
	wg.Wait()

	// Verify no panics under concurrent reads
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				s.List()
				s.CountBySession("sess-1")
				s.ListBySession("sess-1")
				s.ListByRequester(42)
				s.Get("some-worker")
				s.GetByUID(1001)
			}
		}()
	}
	wg.Wait()

	// Concurrent UpdateState and Read
	wg.Add(2)
	go func() {
		defer wg.Done()
		for _, lease := range s.List() {
			_ = s.UpdateState(lease.WorkerID, schema.LeaseTerminated)
		}
	}()
	go func() {
		defer wg.Done()
		for _, lease := range s.List() {
			s.Get(lease.WorkerID)
		}
	}()
	wg.Wait()

	// All operations should complete without data races.
	// This is detected by the `-race` flag at test runtime.
}

// as is a generic type-assertion helper equivalent to errors.As for pointer
// targets. It avoids importing the errors stdlib in the main package.
func as[T error](err error, target *T) bool {
	if err == nil {
		return false
	}
	e, ok := err.(T)
	if !ok {
		return false
	}
	*target = e
	return true
}
