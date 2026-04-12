// Package schema defines the request/response types and protocol constants
// shared across Phase 1 services.
package schema

import (
	"encoding/json"
	"time"
)

// --- Socket Paths ---

const (
	SocketDir       = "/run/agent-os"
	IsolationSocket = "/run/agent-os/isolation.sock"
	AdminSocket     = "/run/agent-os/admin-agent.sock"
	AuditSocket     = "/run/agent-os/audit.sock"
	BusSocket       = "/run/agent-os/bus.sock"
)

// --- Wire Protocol ---
// All three services use the same envelope over Unix sockets.

// Method constants for the request envelope.
const (
	MethodSpawnAgent     = "spawn_agent"
	MethodTerminateAgent = "terminate_agent"
	MethodListAgents     = "list_agents"
	MethodEscalate       = "escalate"
)

type Request struct {
	Method string          `json:"method"`
	Body   json.RawMessage `json:"body"`
}

type Response struct {
	OK   bool            `json:"ok"`
	Body json.RawMessage `json:"body,omitempty"`
}

// --- Agent UID Range ---

const (
	AgentUIDBase uint32 = 60000 // agent uids start here to avoid collisions
	AgentUIDMax  uint32 = 61000
)

// --- Isolation Service ---

type SpawnAgentRequest struct {
	Name       string    `json:"name"`
	Command    []string  `json:"command,omitempty"`     // command + args to execute as the agent uid
	CPUQuota   string    `json:"cpu_quota,omitempty"`   // e.g. "50%" -- percent of one core
	MemoryMax  string    `json:"memory_max,omitempty"`  // e.g. "512M"
	NetAccess  NetPolicy `json:"net_access,omitempty"`
	WatchPaths []string  `json:"watch_paths,omitempty"` // paths for audit service to monitor
}

type NetPolicy string

const (
	NetDeny      NetPolicy = "deny"       // no network access (default)
	NetLocalOnly NetPolicy = "local_only" // loopback only
	NetAllow     NetPolicy = "allow"      // unrestricted
)

// AgentStatus represents the lifecycle state of an agent.
type AgentStatus string

const (
	StatusRunning AgentStatus = "running"
	StatusExited  AgentStatus = "exited"
	StatusStopped AgentStatus = "stopped"
)

type AgentInfo struct {
	Name      string      `json:"name"`
	UID       uint32      `json:"uid"`
	Status    AgentStatus `json:"status"`
	Slice     string      `json:"slice"` // systemd slice name
	CreatedAt time.Time   `json:"created_at"`
}

type SpawnAgentResponse struct {
	Agent AgentInfo `json:"agent"`
	Error string    `json:"error,omitempty"`
}

type TerminateAgentRequest struct {
	UID uint32 `json:"uid"`
}

type ListAgentsResponse struct {
	Agents []AgentInfo `json:"agents"`
}

// --- Admin Agent ---

type EscalationRequest struct {
	AgentUID          uint32 `json:"agent_uid"`
	TaskContext       string `json:"task_context"`
	RequestedAction   string `json:"requested_action"`
	RequestedResource string `json:"requested_resource"`
	Justification     string `json:"justification"`
}

type EscalationDecision string

const (
	DecisionApprove  EscalationDecision = "approve"
	DecisionDeny     EscalationDecision = "deny"
	DecisionEscalate EscalationDecision = "escalate" // needs human review
)

type EscalationResponse struct {
	Decision    EscalationDecision `json:"decision"`
	Reasoning   string             `json:"reasoning"`
	Constraints []string           `json:"constraints,omitempty"` // conditions on approval
	Error       string             `json:"error,omitempty"`
}

// --- Audit Service ---

// AuditAction represents the type of filesystem event observed.
type AuditAction string

const (
	ActionFileModify     AuditAction = "file_modify"
	ActionFileCloseWrite AuditAction = "file_close_write"
	ActionFileOpen       AuditAction = "file_open"
)

// AuditOutcome represents whether an observed action was permitted.
type AuditOutcome string

const (
	OutcomeAllowed AuditOutcome = "allowed"
	OutcomeDenied  AuditOutcome = "denied"
)

type AuditEvent struct {
	Timestamp time.Time    `json:"timestamp"`
	AgentUID  uint32       `json:"agent_uid"`
	AgentName string       `json:"agent_name,omitempty"`
	Action    AuditAction  `json:"action"`
	Resource  string       `json:"resource"` // path or process info
	Outcome   AuditOutcome `json:"outcome"`
}
