// Package schema defines the request/response types shared across Phase 1 services.
package schema

import (
	"encoding/json"
	"time"
)

// --- Isolation Service ---

type SpawnAgentRequest struct {
	Name       string     `json:"name"`
	Command    []string   `json:"command,omitempty"`     // command + args to execute as the agent uid
	CPUQuota   string     `json:"cpu_quota,omitempty"`   // e.g. "50%" — percent of one core
	MemoryMax  string     `json:"memory_max,omitempty"`  // e.g. "512M"
	NetAccess  NetPolicy  `json:"net_access,omitempty"`
	WatchPaths []string   `json:"watch_paths,omitempty"` // paths for audit service to monitor
}

type NetPolicy string

const (
	NetDeny      NetPolicy = "deny"       // no network access (default)
	NetLocalOnly NetPolicy = "local_only" // loopback only
	NetAllow     NetPolicy = "allow"      // unrestricted
)

type AgentInfo struct {
	Name      string    `json:"name"`
	UID       uint32    `json:"uid"`
	Status    string    `json:"status"` // "running", "exited", "stopped"
	Slice     string    `json:"slice"`  // systemd slice name
	CreatedAt time.Time `json:"created_at"`
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

type AuditEvent struct {
	Timestamp time.Time `json:"timestamp"`
	AgentUID  uint32    `json:"agent_uid"`
	AgentName string    `json:"agent_name,omitempty"`
	Action    string    `json:"action"`    // "file_write", "file_read", "file_delete", "process_exec"
	Resource  string    `json:"resource"`  // path or process info
	Outcome   string    `json:"outcome"`   // "allowed", "denied"
}

// --- Wire Protocol ---
// All three services use the same envelope over Unix sockets.

type Request struct {
	Method string          `json:"method"` // "spawn_agent", "terminate_agent", "list_agents", "escalate"
	Body   json.RawMessage `json:"body"`
}

type Response struct {
	OK   bool            `json:"ok"`
	Body json.RawMessage `json:"body,omitempty"`
}
