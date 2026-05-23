// Package supervisor implements the agent supervisor daemon that manages
// R2 worker lifecycle — spawning, assigning, reusing, and terminating
// worker processes on behalf of 3PO and other system components.
package supervisor

import (
	"fmt"
	"sync"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

// ErrLeaseExists is returned when Add is called for a worker_id that already
// has a lease in the store.
type ErrLeaseExists struct{ WorkerID string }

func (e *ErrLeaseExists) Error() string {
	return fmt.Sprintf("lease already exists for worker %q", e.WorkerID)
}

// ErrLeaseNotFound is returned when an operation references a worker_id that
// is not tracked in the store.
type ErrLeaseNotFound struct{ WorkerID string }

func (e *ErrLeaseNotFound) Error() string {
	return fmt.Sprintf("lease not found for worker %q", e.WorkerID)
}

// ErrInvalidTransition is returned when UpdateState is called with a state
// transition that is not allowed by the state machine.
type ErrInvalidTransition struct {
	WorkerID string
	From     schema.WorkerLeaseState
	To       schema.WorkerLeaseState
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("invalid state transition for worker %q: %s -> %s",
		e.WorkerID, e.From, e.To)
}

// validTransitions defines all allowed state transitions for worker leases.
// Terminal states (expired, terminated) have no outgoing transitions.
var validTransitions = map[schema.WorkerLeaseState]map[schema.WorkerLeaseState]bool{
	schema.LeaseRunning: {
		schema.LeaseReleased:   true,
		schema.LeaseExpired:    true,
		schema.LeaseTerminated: true,
	},
	schema.LeaseReleased: {
		schema.LeaseTerminated: true,
	},
}

// isTerminal returns true if the state is a terminal (absorbing) state.
func isTerminal(s schema.WorkerLeaseState) bool {
	return s == schema.LeaseExpired || s == schema.LeaseTerminated
}

// transitionAllowed checks whether moving from `from` to `to` is a valid
// state machine transition.
func transitionAllowed(from, to schema.WorkerLeaseState) bool {
	if targets, ok := validTransitions[from]; ok {
		return targets[to]
	}
	return false
}

// WorkerLeaseStore is an in-memory, mutex-safe store for tracking worker
// leases managed by the agent supervisor. All public methods are safe for
// concurrent use.
type WorkerLeaseStore struct {
	mu     sync.RWMutex
	leases map[string]schema.WorkerLease
}

// NewWorkerLeaseStore returns an initialised, empty lease store.
func NewWorkerLeaseStore() *WorkerLeaseStore {
	return &WorkerLeaseStore{
		leases: make(map[string]schema.WorkerLease),
	}
}

// Add inserts a new lease into the store. Returns an error if a lease for
// the same worker_id already exists.
func (s *WorkerLeaseStore) Add(lease schema.WorkerLease) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.leases[lease.WorkerID]; exists {
		return &ErrLeaseExists{WorkerID: lease.WorkerID}
	}
	s.leases[lease.WorkerID] = lease
	return nil
}

// Get retrieves a lease by worker ID. Returns the lease and true if found,
// or the zero value and false if not.
func (s *WorkerLeaseStore) Get(workerID string) (schema.WorkerLease, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lease, ok := s.leases[workerID]
	return lease, ok
}

// GetByUID retrieves a lease by the assigned agent UID. Since multiple
// workers could theoretically share a UID (though unusual), this returns
// the first match encountered.
func (s *WorkerLeaseStore) GetByUID(agentUID uint32) (schema.WorkerLease, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, lease := range s.leases {
		if lease.AgentUID == agentUID {
			return lease, true
		}
	}
	return schema.WorkerLease{}, false
}

// ListBySession returns all leases owned by the given session.
func (s *WorkerLeaseStore) ListBySession(sessionID string) []schema.WorkerLease {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []schema.WorkerLease
	for _, lease := range s.leases {
		if lease.OwnerSessionID == sessionID {
			result = append(result, lease)
		}
	}
	return result
}

// ListByRequester returns all leases created by the given requester.
func (s *WorkerLeaseStore) ListByRequester(requesterUID uint32) []schema.WorkerLease {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []schema.WorkerLease
	for _, lease := range s.leases {
		if lease.RequesterUID == requesterUID {
			result = append(result, lease)
		}
	}
	return result
}

// UpdateState transitions a lease from its current state to the given state.
// Returns an error if the worker is not found or if the transition is not
// valid according to the state machine.
func (s *WorkerLeaseStore) UpdateState(workerID string, state schema.WorkerLeaseState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lease, exists := s.leases[workerID]
	if !exists {
		return &ErrLeaseNotFound{WorkerID: workerID}
	}

	if !transitionAllowed(lease.State, state) {
		return &ErrInvalidTransition{
			WorkerID: workerID,
			From:     lease.State,
			To:       state,
		}
	}

	lease.State = state
	s.leases[workerID] = lease
	return nil
}

// Release is a convenience method that transitions a lease to the released
// state. Returns an error if the worker is not found.
func (s *WorkerLeaseStore) Release(workerID string) error {
	return s.UpdateState(workerID, schema.LeaseReleased)
}

// Remove deletes a lease from the store entirely. No error is returned if
// the worker does not exist — the method is idempotent.
func (s *WorkerLeaseStore) Remove(workerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.leases, workerID)
}

// List returns all leases currently tracked in the store.
func (s *WorkerLeaseStore) List() []schema.WorkerLease {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]schema.WorkerLease, 0, len(s.leases))
	for _, lease := range s.leases {
		result = append(result, lease)
	}
	return result
}

// CountBySession returns the number of active (running) leases owned by the
// given session.
func (s *WorkerLeaseStore) CountBySession(sessionID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, lease := range s.leases {
		if lease.OwnerSessionID == sessionID && lease.State == schema.LeaseRunning {
			count++
		}
	}
	return count
}

// ExpireLeases finds all running leases whose expiry time is before `now`,
// sets their state to expired, and returns the list of affected worker IDs.
// Leases with a nil expiry are never expired by this method.
func (s *WorkerLeaseStore) ExpireLeases(now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var expired []string
	for id, lease := range s.leases {
		if lease.State != schema.LeaseRunning {
			continue
		}
		if lease.LeaseExpiresAt == nil {
			continue
		}
		if lease.LeaseExpiresAt.Before(now) {
			lease.State = schema.LeaseExpired
			s.leases[id] = lease
			expired = append(expired, id)
		}
	}
	return expired
}
