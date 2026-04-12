package schema

import "encoding/json"

// --- 3PO / R2 Topic Constants ---
//
// These topics define the structured coordination surface between the shell,
// 3PO, the agent supervisor, and R2 workers. Sender identity for messages on
// the event bus is authoritative only when taken from broker-stamped peer
// metadata (SO_PEERCRED). Payload fields such as role or profile names are
// advisory routing/context values, not proof of authority.

const (
	TopicConversationTurnRequested = "conversation.turn.requested"
	TopicConversationTurnResponded = "conversation.turn.responded"

	TopicAgentSpawnRequested = "agent.spawn.requested"
	TopicAgentSpawnAccepted  = "agent.spawn.accepted"
	TopicAgentSpawnRejected  = "agent.spawn.rejected"

	TopicAgentLifecycleAssigned   = "agent.lifecycle.assigned"
	TopicAgentLifecycleReused     = "agent.lifecycle.reused"
	TopicAgentLifecycleTerminated = "agent.lifecycle.terminated"

	TopicAgentWorkAssigned  = "agent.work.assigned"
	TopicAgentWorkProgress  = "agent.work.progress"
	TopicAgentWorkResult    = "agent.work.result"
	TopicAgentWorkCancelled = "agent.work.cancelled"
	TopicAgentWorkNeeds3PO  = "agent.work.needs_3po"

	TopicAdminEscalationRequested = "admin.escalation.requested"
	TopicAdminEscalationDecided   = "admin.escalation.decided"
)

// --- 3PO / R2 Domain Values ---

type RequesterRole string

const (
	RequesterRoleShell RequesterRole = "shell"
	RequesterRole3PO   RequesterRole = "3po"
)

type WorkerRuntime string

const (
	RuntimeDeterministic WorkerRuntime = "deterministic"
	RuntimeLocalLLM      WorkerRuntime = "local_llm"
	RuntimeLocalLarge    WorkerRuntime = "local_large"
)

type WorkerReusePolicy string

const (
	ReuseNever   WorkerReusePolicy = "never"
	ReuseSession WorkerReusePolicy = "session"
	ReuseLease   WorkerReusePolicy = "lease"
)

type WorkerLeaseState string

const (
	LeaseRunning    WorkerLeaseState = "running"
	LeaseReleased   WorkerLeaseState = "released"
	LeaseExpired    WorkerLeaseState = "expired"
	LeaseTerminated WorkerLeaseState = "terminated"
)

type WorkResultStatus string

const (
	WorkStatusOK       WorkResultStatus = "ok"
	WorkStatusFailed   WorkResultStatus = "failed"
	WorkStatusNeeds3PO WorkResultStatus = "needs_3po"
	WorkStatusCanceled WorkResultStatus = "canceled"
)

type ArtifactKind string

const (
	ArtifactFile    ArtifactKind = "file"
	ArtifactLog     ArtifactKind = "log"
	ArtifactPatch   ArtifactKind = "patch"
	ArtifactSummary ArtifactKind = "summary"
)

// --- Worker Profiles ---

// WorkerProfile describes the sandbox shape and runtime class for a reusable
// R2 worker type. Callers request a named profile; they do not supply arbitrary
// command lines or one-off sandbox settings.
type WorkerProfile struct {
	Profile         string            `json:"profile"`
	Runtime         WorkerRuntime     `json:"runtime"`
	Tools           []string          `json:"tools,omitempty"`
	CPUQuota        string            `json:"cpu_quota,omitempty"`
	MemoryMax       string            `json:"memory_max,omitempty"`
	NetAccess       NetPolicy         `json:"net_access,omitempty"`
	WatchPaths      []string          `json:"watch_paths,omitempty"`
	MaxLeaseSeconds int               `json:"max_lease_seconds,omitempty"`
	ReusePolicy     WorkerReusePolicy `json:"reuse_policy,omitempty"`
}

// ProfileGrant captures which profiles a requester uid may ask the supervisor
// to create or reuse, along with coarse budget limits.
type ProfileGrant struct {
	RequesterUID         uint32   `json:"requester_uid"`
	AllowedProfiles      []string `json:"allowed_profiles,omitempty"`
	MaxConcurrentWorkers int      `json:"max_concurrent_workers,omitempty"`
	MaxLeaseSeconds      int      `json:"max_lease_seconds,omitempty"`
}

// --- Supervisor Control API ---

// EnsureWorkerRequest asks the supervisor to return an existing compatible
// worker or create a new one. Caller authority comes from peer credentials,
// not from any field in this payload.
type EnsureWorkerRequest struct {
	SessionID     string          `json:"session_id"`
	RequestID     string          `json:"request_id"`
	RequesterRole RequesterRole   `json:"requester_role,omitempty"`
	WorkerProfile string          `json:"worker_profile"`
	Objective     string          `json:"objective"`
	Inputs        json.RawMessage `json:"inputs,omitempty"`
	LeaseSeconds  int             `json:"lease_seconds,omitempty"`
	ReplyTopic    string          `json:"reply_topic,omitempty"`
}

// WorkerAssignment describes the worker a caller should use for subsequent
// event-bus work orders.
type WorkerAssignment struct {
	WorkerID        string    `json:"worker_id"`
	Created         bool      `json:"created"`
	Agent           AgentInfo `json:"agent"`
	Profile         string    `json:"profile"`
	LeaseExpiresAt  string    `json:"lease_expires_at,omitempty"`
	AssignmentTopic string    `json:"assignment_topic"`
}

type EnsureWorkerResponse struct {
	Assignment WorkerAssignment `json:"assignment"`
	Error      string           `json:"error,omitempty"`
}

type ReleaseWorkerRequest struct {
	SessionID string `json:"session_id"`
	WorkerID  string `json:"worker_id"`
}

type ReleaseWorkerResponse struct {
	Released bool   `json:"released"`
	Error    string `json:"error,omitempty"`
}

type TerminateWorkerSupervisorRequest struct {
	SessionID string `json:"session_id"`
	WorkerID  string `json:"worker_id"`
	Reason    string `json:"reason,omitempty"`
}

type TerminateWorkerSupervisorResponse struct {
	Terminated bool   `json:"terminated"`
	Error      string `json:"error,omitempty"`
}

type ListWorkersRequest struct {
	SessionID string `json:"session_id,omitempty"`
}

type WorkerLease struct {
	WorkerID        string           `json:"worker_id"`
	AgentUID        uint32           `json:"agent_uid"`
	Profile         string           `json:"profile"`
	OwnerSessionID  string           `json:"owner_session_id"`
	RequesterUID    uint32           `json:"requester_uid"`
	LeaseExpiresAt  string           `json:"lease_expires_at,omitempty"`
	AssignmentTopic string           `json:"assignment_topic"`
	State           WorkerLeaseState `json:"state"`
}

type ListWorkersResponse struct {
	Workers []WorkerLease `json:"workers,omitempty"`
}

type DescribeProfilesRequest struct{}

type DescribeProfilesResponse struct {
	Profiles []WorkerProfile `json:"profiles,omitempty"`
}

// --- Event-Bus Message Families ---

// ConversationTurnRequest is the shell-to-3PO event describing one structured
// user-facing turn.
type ConversationTurnRequest struct {
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id"`
	Prompt    string          `json:"prompt"`
	Context   json.RawMessage `json:"context,omitempty"`
}

type ConversationTurnResponse struct {
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id"`
	Summary   string          `json:"summary"`
	Result    json.RawMessage `json:"result,omitempty"`
}

// SpawnRequestedEvent mirrors the supervisor control request onto the event
// bus for observability. Payload values are descriptive only; authorization
// still comes from peer credentials at the supervisor socket.
type SpawnRequestedEvent struct {
	SessionID     string        `json:"session_id"`
	RequestID     string        `json:"request_id"`
	RequesterRole RequesterRole `json:"requester_role,omitempty"`
	WorkerProfile string        `json:"worker_profile"`
	Objective     string        `json:"objective"`
}

type SpawnAcceptedEvent struct {
	SessionID  string           `json:"session_id"`
	RequestID  string           `json:"request_id"`
	Assignment WorkerAssignment `json:"assignment"`
}

type SpawnRejectedEvent struct {
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id"`
	Reason    string `json:"reason"`
}

type WorkerLifecycleEvent struct {
	SessionID string           `json:"session_id,omitempty"`
	WorkerID  string           `json:"worker_id"`
	Lease     WorkerLease      `json:"lease"`
	Profile   string           `json:"profile"`
	State     WorkerLeaseState `json:"state"`
}

// WorkBudget bounds how much effort an R2 worker should spend on one work
// order before returning a result or asking 3PO for help.
type WorkBudget struct {
	MaxSteps        int `json:"max_steps,omitempty"`
	DeadlineSeconds int `json:"deadline_seconds,omitempty"`
}

// WorkOrder is the concrete task assignment sent to an R2 worker. The input
// payload may vary by profile, but the surrounding contract stays fixed.
type WorkOrder struct {
	TaskID        string          `json:"task_id"`
	SessionID     string          `json:"session_id"`
	WorkerID      string          `json:"worker_id,omitempty"`
	AssignedRole  string          `json:"assigned_role,omitempty"`
	WorkerProfile string          `json:"worker_profile,omitempty"`
	Objective     string          `json:"objective"`
	Inputs        json.RawMessage `json:"inputs,omitempty"`
	Budget        WorkBudget      `json:"budget,omitempty"`
	ReplyTopic    string          `json:"reply_topic,omitempty"`
}

type WorkProgress struct {
	TaskID   string `json:"task_id"`
	Stage    string `json:"stage"`
	Message  string `json:"message"`
	Step     int    `json:"step,omitempty"`
	MaxSteps int    `json:"max_steps,omitempty"`
}

type ArtifactRef struct {
	Kind ArtifactKind `json:"kind"`
	Path string       `json:"path,omitempty"`
	Text string       `json:"text,omitempty"`
}

// WorkResult is the machine-readable completion payload returned by an R2.
// 3PO is responsible for turning this into human-facing narrative.
type WorkResult struct {
	TaskID    string           `json:"task_id"`
	Status    WorkResultStatus `json:"status"`
	Summary   string           `json:"summary"`
	Artifacts []ArtifactRef    `json:"artifacts,omitempty"`
	FollowUp  []string         `json:"follow_up,omitempty"`
	Needs3PO  bool             `json:"needs_3po,omitempty"`
	Error     string           `json:"error,omitempty"`
}

type WorkCancelled struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason,omitempty"`
}

type WorkNeeds3PO struct {
	TaskID  string `json:"task_id"`
	Reason  string `json:"reason"`
	Summary string `json:"summary,omitempty"`
}
