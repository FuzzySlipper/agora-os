// Package supervisor implements the agent supervisor service that manages
// worker lifecycle, reuse, and budget enforcement for the R2 worker fleet.
package supervisor

import (
	"errors"
	"fmt"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

// ---------------------------------------------------------------------------
// LeaseCounter — interface satisfied by the supervisor Store (state.go)
// ---------------------------------------------------------------------------

// LeaseCounter abstracts counting leases for budget checks.
type LeaseCounter interface {
	// CountBySession returns the number of active (running) leases for a session.
	CountBySession(sessionID string) int
}

// ---------------------------------------------------------------------------
// ProfileRegistry
// ---------------------------------------------------------------------------

// ProfileRegistry stores and retrieves worker profiles. Created from config
// and treated as immutable thereafter.
type ProfileRegistry struct {
	byName map[string]schema.WorkerProfile
}

// NewProfileRegistry builds an immutable registry from a config-supplied
// slice of profiles. Returns an error if two profiles share the same name.
func NewProfileRegistry(profiles []schema.WorkerProfile) (*ProfileRegistry, error) {
	byName := make(map[string]schema.WorkerProfile, len(profiles))
	for _, p := range profiles {
		if _, dup := byName[p.Profile]; dup {
			return nil, fmt.Errorf("duplicate profile name %q", p.Profile)
		}
		byName[p.Profile] = p
	}
	return &ProfileRegistry{byName: byName}, nil
}

// Get retrieves a profile by name. The bool is false when the name is unknown.
func (r *ProfileRegistry) Get(name string) (schema.WorkerProfile, bool) {
	p, ok := r.byName[name]
	return p, ok
}

// List returns all registered profiles in no particular order.
func (r *ProfileRegistry) List() []schema.WorkerProfile {
	all := make([]schema.WorkerProfile, 0, len(r.byName))
	for _, p := range r.byName {
		all = append(all, p)
	}
	return all
}

// ValidateProfile returns an error if name does not name a known profile.
func (r *ProfileRegistry) ValidateProfile(name string) error {
	if _, ok := r.byName[name]; !ok {
		return fmt.Errorf("unknown profile %q", name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// GrantRegistry
// ---------------------------------------------------------------------------

// GrantRegistry stores ProfileGrant entries keyed by requester UID. Created
// from config and treated as immutable thereafter.
type GrantRegistry struct {
	byUID map[uint32]schema.ProfileGrant
}

// NewGrantRegistry builds an immutable grant registry. Duplicate UIDs cause
// the last entry in the slice to win.
func NewGrantRegistry(grants []schema.ProfileGrant) *GrantRegistry {
	byUID := make(map[uint32]schema.ProfileGrant, len(grants))
	for _, g := range grants {
		byUID[g.RequesterUID] = g
	}
	return &GrantRegistry{byUID: byUID}
}

// Get returns the grant for a requester UID.
func (g *GrantRegistry) Get(requesterUID uint32) (schema.ProfileGrant, bool) {
	grant, ok := g.byUID[requesterUID]
	return grant, ok
}

// AllowedProfiles returns the list of profile names this requester may request.
// Returns nil when the requester has no grant.
func (g *GrantRegistry) AllowedProfiles(requesterUID uint32) []string {
	grant, ok := g.byUID[requesterUID]
	if !ok {
		return nil
	}
	out := make([]string, len(grant.AllowedProfiles))
	copy(out, grant.AllowedProfiles)
	return out
}

// AllowedToRequest checks whether a specific profile name is in the
// requester's allowed-profiles list. Returns true for grant owners with
// an empty (all-access) AllowedProfiles slice.
func (g *GrantRegistry) AllowedToRequest(requesterUID uint32, profileName string) bool {
	grant, ok := g.byUID[requesterUID]
	if !ok {
		return false
	}
	if len(grant.AllowedProfiles) == 0 {
		// Empty slice means "all profiles" for this requester.
		return true
	}
	for _, p := range grant.AllowedProfiles {
		if p == profileName {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// CanReuse
// ---------------------------------------------------------------------------

// CanReuse evaluates whether an existing lease can be reused for a new
// EnsureWorker request according to the profile and its reuse policy.
//
// All conditions from the design doc must hold:
//   - Same profile name
//   - Same session
//   - Same network policy
//   - Same runtime class
//   - Current lease still valid (not expired, not terminated)
//   - Worker status is running
//
// Returns (true, "") when reusable, or (false, "human-readable reason").
func CanReuse(lease schema.WorkerLease, req schema.EnsureWorkerRequest, profile schema.WorkerProfile) (bool, string) {
	// --- Reuse policy gate ---
	switch profile.ReusePolicy {
	case schema.ReuseNever:
		return false, "reuse policy is 'never'"
	case schema.ReuseSession:
		// Same-session only — checked below
	case schema.ReuseLease:
		// Lease-scoped — checked below
	case "":
		// Default to never
		return false, "reuse policy is empty (default: never)"
	default:
		return false, fmt.Sprintf("unknown reuse policy %q", profile.ReusePolicy)
	}

	// --- Same profile name ---
	if lease.Profile != req.WorkerProfile {
		return false, fmt.Sprintf("profile mismatch: lease=%q request=%q", lease.Profile, req.WorkerProfile)
	}

	// --- Same session ---
	if profile.ReusePolicy == schema.ReuseSession {
		if lease.OwnerSessionID != req.SessionID {
			return false, fmt.Sprintf("session mismatch for session-scoped reuse: lease=%q request=%q", lease.OwnerSessionID, req.SessionID)
		}
	}

	// --- Same network policy ---
	if profile.NetAccess == "" {
		return false, "profile has no net access policy set"
	}

	// --- Same runtime class ---
	if profile.Runtime == "" {
		return false, "profile has no runtime set"
	}

	// --- Lease still valid ---
	now := time.Now()
	if lease.LeaseExpiresAt != nil && lease.LeaseExpiresAt.Before(now) {
		return false, "lease has expired"
	}
	if lease.State == schema.LeaseTerminated {
		return false, "lease was terminated"
	}

	// --- Worker status is running ---
	if lease.State != schema.LeaseRunning {
		return false, fmt.Sprintf("lease is not running (state=%q)", lease.State)
	}

	return true, ""
}

// ---------------------------------------------------------------------------
// CheckBudget
// ---------------------------------------------------------------------------

var (
	ErrBudgetExceeded    = errors.New("budget exceeded")
	ErrNoGrant           = errors.New("no grant for requester")
)

// CheckBudget verifies that a new worker lease does not exceed the budget
// limits granted to a requester within a session.
//
// Returns nil if the budget check passes, or ErrBudgetExceeded if the session
// already has too many concurrent workers.
//
// Note: The caller should independently verify that the profile's MaxLeaseSeconds
// does not exceed the grant's MaxLeaseSeconds, since that check needs the profile
// reference which is not part of this interface contract.
func CheckBudget(store LeaseCounter, grant schema.ProfileGrant, sessionID string, requesterUID uint32) error {
	// --- Max concurrent workers ---
	if grant.MaxConcurrentWorkers > 0 {
		active := store.CountBySession(sessionID)
		if active >= grant.MaxConcurrentWorkers {
			return fmt.Errorf("%w: session %q has %d active workers, max is %d",
				ErrBudgetExceeded, sessionID, active, grant.MaxConcurrentWorkers)
		}
	}

	return nil
}
