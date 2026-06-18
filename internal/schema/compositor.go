package schema

import (
	"encoding/json"
	"time"
)

const (
	CompositorPluginSocket  = "/run/agent-os/compositor-bridge.sock"
	CompositorControlSocket = "/run/agent-os/compositor-control.sock"
)

const (
	ErrorSurfaceNotFound         = "surface_not_found"
	ErrorSurfaceStale            = "surface_stale"
	ErrorCaptureDenied           = "capture_denied"
	ErrorCaptureBlackFrame       = "capture_black_frame"
	ErrorInputDenied             = "input_denied"
	ErrorGrantExpired            = "grant_expired"
	ErrorAppNotReady             = "app_not_ready"
	ErrorFrameTimeout            = "frame_timeout"
	ErrorSemanticTreeUnavailable = "semantic_tree_unavailable"
	ErrorCompositorUnavailable   = "compositor_unavailable"
	ErrorBackendUnsupported      = "backend_unsupported"
	ErrorSessionNotFound         = "session_not_found"
	ErrorSessionTokenRequired    = "session_token_required"
	ErrorAppCommandUnavailable   = "app_command_unavailable"
	ErrorInvalidCoordinates      = "invalid_coordinates"
	ErrorProtocolError           = "protocol_error"
)

const (
	MethodListSurfaces        = "list_surfaces"
	MethodGetSurface          = "get_surface"
	MethodCaptureSurface      = "capture_surface"
	MethodInjectInput         = "inject_input"
	MethodCreateSession       = "create_session"
	MethodDestroySession      = "destroy_session"
	MethodResetSession        = "reset_session"
	MethodListSessions        = "list_sessions"
	MethodGetSession          = "get_session"
	MethodLaunchApp           = "launch_app"
	MethodListProcesses       = "list_processes"
	MethodTerminateLaunch     = "terminate_launch"
	MethodWaitForSurface      = "wait_for_surface"
	MethodWaitForFrame        = "wait_for_frame"
	MethodWaitForAppReady     = "wait_for_app_ready"
	MethodWaitForRenderIdle   = "wait_for_render_idle"
	MethodWaitForNoPending    = "wait_for_no_pending"
	MethodListArtifacts       = "list_artifacts"
	MethodGetArtifact         = "get_artifact"
	MethodExportArtifacts     = "export_artifacts"
	MethodCreateOutput        = "create_output"
	MethodDestroyOutput       = "destroy_output"
	MethodResizeOutput        = "resize_output"
	MethodSetOutputScale      = "set_output_scale"
	MethodListOutputs         = "list_outputs"
	MethodMoveSurfaceToOutput = "move_surface_to_output"
	MethodListOutputSurfaces  = "list_output_surfaces"
	MethodCaptureOutput       = "capture_output"
	MethodA11yTree            = "a11y_tree"
	MethodA11ySemantic        = "a11y_semantic"
	MethodA11yFind            = "a11y_find"
	MethodA11yClick           = "a11y_click"
	MethodAppCommand          = "app_command"
	MethodAppResult           = "app_result"
	MethodUpsertSurfacePolicy = "upsert_surface_policy"
	MethodRemoveSurfacePolicy = "remove_surface_policy"
	MethodSetInputContext     = "set_input_context"
	MethodSetViewProperty     = "set_view_property"
	MethodCloseSurface        = "close_surface"
	MethodCloseSurfacesByUID  = "close_surfaces_by_uid"
	MethodGrantViewport       = "grant_viewport"
	MethodRevokeViewport      = "revoke_viewport"
	MethodCheckSurfaceAccess  = "check_surface_access"
)

const (
	TopicCompositorSurfaceCreated   = "compositor.surface.created"
	TopicCompositorSurfaceDestroyed = "compositor.surface.destroyed"
	TopicCompositorSurfaceFocused   = "compositor.surface.focused"
	TopicCompositorSurfaceInput     = "compositor.surface.input"

	// Advisory compositor topics: published by non-privileged clients (e.g.,
	// webview launcher) alongside the authoritative compositor.surface.*
	// topics published by the root-owned compositor bridge.
	TopicCompositorAdvisorySurfaceCreated   = "compositor.advisory.surface.created"
	TopicCompositorAdvisorySurfaceDestroyed = "compositor.advisory.surface.destroyed"
	TopicCompositorAdvisorySurfaceFocused   = "compositor.advisory.surface.focused"
)

type CompositorPluginMessageType string

const (
	PluginMessageSurfaceEvent       CompositorPluginMessageType = "surface_event"
	PluginMessageCaptureSurface     CompositorPluginMessageType = "capture_surface"
	PluginMessageCaptureResponse    CompositorPluginMessageType = "capture_response"
	PluginMessageInjectInput        CompositorPluginMessageType = "inject_input"
	PluginMessageInputResponse      CompositorPluginMessageType = "input_response"
	PluginMessagePlaceSurface       CompositorPluginMessageType = "place_surface"
	PluginMessagePlaceResponse      CompositorPluginMessageType = "place_response"
	PluginMessagePolicyReplace      CompositorPluginMessageType = "policy_replace"
	PluginMessagePolicyUpsert       CompositorPluginMessageType = "policy_upsert"
	PluginMessagePolicyRemove       CompositorPluginMessageType = "policy_remove"
	PluginMessageInputContext       CompositorPluginMessageType = "input_context"
	PluginMessageSetViewProperty    CompositorPluginMessageType = "set_view_property"
	PluginMessageCloseSurface       CompositorPluginMessageType = "close_surface"
	PluginMessageCloseSurfacesByUID CompositorPluginMessageType = "close_surfaces_by_uid"
)

type CompositorSurfaceEventName string

const (
	SurfaceEventMapped      CompositorSurfaceEventName = "mapped"
	SurfaceEventUnmapped    CompositorSurfaceEventName = "unmapped"
	SurfaceEventFocused     CompositorSurfaceEventName = "focused"
	SurfaceEventFrameDone   CompositorSurfaceEventName = "frame_done"
	SurfaceEventInputDenied CompositorSurfaceEventName = "input_denied"
)

type CompositorAccessAction string

const (
	AccessPointer    CompositorAccessAction = "pointer"
	AccessKeyboard   CompositorAccessAction = "keyboard"
	AccessReadPixels CompositorAccessAction = "read_pixels"
)

const (
	SurfaceKindXDGView    = "xdg_view"
	SurfaceKindLayerShell = "layer_shell"
)

type LayerShellSurfaceMetadata struct {
	Namespace     string   `json:"namespace,omitempty"`
	Layer         string   `json:"layer,omitempty"`
	Anchors       []string `json:"anchors,omitempty"`
	ExclusiveZone *bool    `json:"exclusive_zone,omitempty"`
}

type CompositorSurface struct {
	ID            string                     `json:"id"`
	WayfireViewID uint32                     `json:"wayfire_view_id"`
	SurfaceKind   string                     `json:"surface_kind,omitempty"`
	AppID         string                     `json:"app_id,omitempty"`
	Title         string                     `json:"title,omitempty"`
	Role          string                     `json:"role,omitempty"`
	LayerShell    *LayerShellSurfaceMetadata `json:"layer_shell,omitempty"`
	Geometry      *SurfaceGeometry           `json:"geometry,omitempty"`
	PixelSize     *SurfaceGeometry           `json:"pixel_size,omitempty"`
	ScaleFactor   float64                    `json:"scale_factor,omitempty"`
	Visible       *bool                      `json:"visible,omitempty"`
	OutputID      string                     `json:"output_id,omitempty"`
}

type CompositorClientIdentity struct {
	PID int32  `json:"pid"`
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}

type CompositorPluginEvent struct {
	Type       CompositorPluginMessageType `json:"type"`
	Event      CompositorSurfaceEventName  `json:"event,omitempty"`
	Device     string                      `json:"device,omitempty"`
	Surface    CompositorSurface           `json:"surface"`
	Client     CompositorClientIdentity    `json:"client"`
	RequestID  string                      `json:"request_id,omitempty"`
	SurfaceID  string                      `json:"surface_id,omitempty"`
	OK         bool                        `json:"ok,omitempty"`
	Width      uint32                      `json:"width,omitempty"`
	Height     uint32                      `json:"height,omitempty"`
	Format     string                      `json:"format,omitempty"`
	DataBase64 string                      `json:"data_base64,omitempty"`
	Accepted   uint32                      `json:"accepted,omitempty"`
	Rejected   uint32                      `json:"rejected,omitempty"`
	Error      string                      `json:"error,omitempty"`
}

type CompositorSurfacePolicy struct {
	SurfaceID         string   `json:"surface_id"`
	OwnerUID          uint32   `json:"owner_uid"`
	AllowPointerUIDs  []uint32 `json:"allow_pointer_uids,omitempty"`
	AllowKeyboardUIDs []uint32 `json:"allow_keyboard_uids,omitempty"`
}

type CompositorPolicyReplace struct {
	Type     CompositorPluginMessageType `json:"type"`
	Surfaces []CompositorSurfacePolicy   `json:"surfaces,omitempty"`
}

type CompositorCaptureSurface struct {
	Type      CompositorPluginMessageType `json:"type"`
	RequestID string                      `json:"request_id"`
	SurfaceID string                      `json:"surface_id"`
}

type CompositorCapturePluginResponse struct {
	Type       CompositorPluginMessageType `json:"type"`
	RequestID  string                      `json:"request_id"`
	SurfaceID  string                      `json:"surface_id"`
	OK         bool                        `json:"ok"`
	Width      uint32                      `json:"width,omitempty"`
	Height     uint32                      `json:"height,omitempty"`
	Format     string                      `json:"format,omitempty"`
	DataBase64 string                      `json:"data_base64,omitempty"`
	Error      string                      `json:"error,omitempty"`
}

type CompositorInputEvent struct {
	Type     string  `json:"type"`
	X        float64 `json:"x,omitempty"`
	Y        float64 `json:"y,omitempty"`
	Button   uint32  `json:"button,omitempty"`
	Keycode  uint32  `json:"keycode,omitempty"`
	State    string  `json:"state,omitempty"`
	Value    float64 `json:"value,omitempty"`
	Discrete int32   `json:"discrete,omitempty"`
	Axis     uint32  `json:"axis,omitempty"`
	TouchID  int32   `json:"touch_id,omitempty"`
	Phase    string  `json:"phase,omitempty"`
}

type CompositorInjectInput struct {
	Type            CompositorPluginMessageType `json:"type"`
	RequestID       string                      `json:"request_id"`
	SurfaceID       string                      `json:"surface_id"`
	CoordinateSpace string                      `json:"coordinate_space,omitempty"`
	Events          []CompositorInputEvent      `json:"events"`
}

type CompositorInputPluginResponse struct {
	Type      CompositorPluginMessageType `json:"type"`
	RequestID string                      `json:"request_id"`
	SurfaceID string                      `json:"surface_id"`
	OK        bool                        `json:"ok"`
	Accepted  uint32                      `json:"accepted"`
	Rejected  uint32                      `json:"rejected"`
	Error     string                      `json:"error,omitempty"`
}

type CompositorPolicyUpsert struct {
	Type    CompositorPluginMessageType `json:"type"`
	Surface CompositorSurfacePolicy     `json:"surface"`
}

type CompositorPolicyRemove struct {
	Type      CompositorPluginMessageType `json:"type"`
	SurfaceID string                      `json:"surface_id"`
}

type CompositorInputContextUpdate struct {
	Type     CompositorPluginMessageType `json:"type"`
	ActorUID *uint32                     `json:"actor_uid,omitempty"`
}

type CompositorSetViewProperty struct {
	Type       CompositorPluginMessageType `json:"type"`
	SurfaceID  string                      `json:"surface_id"`
	Properties map[string]any              `json:"properties"`
}

type CompositorCloseSurface struct {
	Type      CompositorPluginMessageType `json:"type"`
	SurfaceID string                      `json:"surface_id"`
}

type CompositorCloseSurfacesByUID struct {
	Type     CompositorPluginMessageType `json:"type"`
	OwnerUID uint32                      `json:"owner_uid"`
}

type SurfaceGeometry struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type SurfaceGrantState struct {
	OwnerUID     uint32   `json:"owner_uid"`
	GrantedUIDs  []uint32 `json:"granted_uids,omitempty"`
	GrantActions []string `json:"grant_actions,omitempty"`
}

type CompositorTrackedSurface struct {
	Surface              CompositorSurface          `json:"surface"`
	Client               CompositorClientIdentity   `json:"client"`
	LastEvent            CompositorSurfaceEventName `json:"last_event"`
	Device               string                     `json:"device,omitempty"`
	UpdatedAt            time.Time                  `json:"updated_at"`
	Geometry             *SurfaceGeometry           `json:"geometry,omitempty"`
	Focused              bool                       `json:"focused"`
	PixelSize            *SurfaceGeometry           `json:"pixel_size,omitempty"`
	ScaleFactor          float64                    `json:"scale_factor,omitempty"`
	Capturable           bool                       `json:"capturable"`
	InputInjectable      bool                       `json:"input_injectable"`
	FrameCount           uint64                     `json:"frame_count"`
	LastPresentTimestamp *time.Time                 `json:"last_present_timestamp,omitempty"`
	Visible              bool                       `json:"visible"`
	SessionID            string                     `json:"session_id,omitempty"`
	OutputID             string                     `json:"output_id,omitempty"`
	GrantState           *SurfaceGrantState         `json:"grant_state,omitempty"`
}

type CompositorBusEvent struct {
	Surface CompositorSurface          `json:"surface"`
	Client  CompositorClientIdentity   `json:"client"`
	Event   CompositorSurfaceEventName `json:"event"`
	Device  string                     `json:"device,omitempty"`
}

type ListSurfacesResponse struct {
	Surfaces []CompositorTrackedSurface `json:"surfaces,omitempty"`
}

type GetSurfaceRequest struct {
	SurfaceID string `json:"surface_id"`
}

type CaptureSurfaceRequest struct {
	SurfaceID             string `json:"surface_id"`
	Format                string `json:"format,omitempty"`
	MaxWidth              uint32 `json:"max_width,omitempty"`
	MaxHeight             uint32 `json:"max_height,omitempty"`
	Export                bool   `json:"export,omitempty"`
	SessionID             string `json:"session_id,omitempty"`
	SessionToken          string `json:"session_token,omitempty"`
	AuditCorrelationID    string `json:"audit_correlation_id,omitempty"`
	EvidenceClass         string `json:"evidence_class,omitempty"`
	ASHACommandSequenceID string `json:"asha_command_sequence_id,omitempty"`
}

type CaptureSurfaceResponse struct {
	SurfaceID        string                    `json:"surface_id"`
	RequestID        string                    `json:"request_id,omitempty"`
	Path             string                    `json:"path"`
	ImagePath        string                    `json:"image_path,omitempty"`
	Width            uint32                    `json:"width"`
	Height           uint32                    `json:"height"`
	Format           string                    `json:"format"`
	SHA256           string                    `json:"sha256"`
	VisualInspection *ArtifactVisualInspection `json:"visual_inspection,omitempty"`
	Artifact         *ArtifactRecord           `json:"artifact,omitempty"`
}

type ArtifactRecord struct {
	ArtifactID            string                    `json:"artifact_id"`
	SessionID             string                    `json:"session_id"`
	SurfaceID             string                    `json:"surface_id"`
	RequestID             string                    `json:"request_id"`
	ImagePath             string                    `json:"image_path"`
	IndexPath             string                    `json:"index_path"`
	Width                 uint32                    `json:"width"`
	Height                uint32                    `json:"height"`
	Format                string                    `json:"format"`
	SHA256                string                    `json:"sha256"`
	CaptureBackend        string                    `json:"capture_backend"`
	AuditCorrelationID    string                    `json:"audit_correlation_id,omitempty"`
	EvidenceClass         string                    `json:"evidence_class"`
	Timestamp             time.Time                 `json:"timestamp"`
	ASHACommandSequenceID string                    `json:"asha_command_sequence_id,omitempty"`
	VisualInspection      *ArtifactVisualInspection `json:"visual_inspection,omitempty"`
	Warnings              []string                  `json:"warnings,omitempty"`
}

type ArtifactVisualInspection struct {
	Status              string     `json:"status"`
	Classification      string     `json:"classification,omitempty"`
	Width               int        `json:"width"`
	Height              int        `json:"height"`
	Mode                string     `json:"mode"`
	Extrema             [][2]uint8 `json:"extrema,omitempty"`
	UniqueColorsSampled int        `json:"unique_colors_sampled,omitempty"`
}

type ListArtifactsRequest struct {
	SessionID string `json:"session_id"`
}

type ListArtifactsResponse struct {
	Artifacts []ArtifactRecord `json:"artifacts,omitempty"`
}

type GetArtifactRequest struct {
	ArtifactID string `json:"artifact_id"`
}

type ExportArtifactsRequest struct {
	SessionID string `json:"session_id"`
	To        string `json:"to"`
}

type ExportArtifactsResponse struct {
	SessionID string   `json:"session_id"`
	To        string   `json:"to"`
	Copied    []string `json:"copied,omitempty"`
}

type SetViewPropertyRequest struct {
	SurfaceID  string         `json:"surface_id"`
	Properties map[string]any `json:"properties"`
}

type InjectInputRequest struct {
	SurfaceID          string                 `json:"surface_id"`
	SessionID          string                 `json:"session_id,omitempty"`
	SessionToken       string                 `json:"session_token,omitempty"`
	AuditCorrelationID string                 `json:"audit_correlation_id,omitempty"`
	CoordinateSpace    string                 `json:"coordinate_space,omitempty"`
	Events             []CompositorInputEvent `json:"events"`
}

type InjectInputResponse struct {
	SurfaceID string `json:"surface_id"`
	Accepted  uint32 `json:"accepted"`
	Rejected  uint32 `json:"rejected"`
}

type CompositorSession struct {
	SessionID          string                     `json:"session_id"`
	Label              string                     `json:"label,omitempty"`
	ProjectID          string                     `json:"project_id,omitempty"`
	TaskID             int                        `json:"task_id,omitempty"`
	AgentIdentity      string                     `json:"agent_identity,omitempty"`
	SessionToken       string                     `json:"session_token,omitempty"`
	ASHAScenarioID     string                     `json:"asha_scenario_id,omitempty"`
	RepoCommit         string                     `json:"repo_commit,omitempty"`
	RepoBranch         string                     `json:"repo_branch,omitempty"`
	ASHARuntimeMode    string                     `json:"asha_runtime_mode,omitempty"`
	ArtifactRoot       string                     `json:"artifact_root,omitempty"`
	AuditCorrelationID string                     `json:"audit_correlation_id,omitempty"`
	CreatedAt          time.Time                  `json:"created_at"`
	LastUsedAt         time.Time                  `json:"last_used_at"`
	Surfaces           []CompositorTrackedSurface `json:"surfaces,omitempty"`
	Processes          []CompositorLaunchProcess  `json:"processes,omitempty"`
}

type CreateSessionRequest struct {
	Label              string `json:"label,omitempty"`
	ProjectID          string `json:"project_id,omitempty"`
	TaskID             int    `json:"task_id,omitempty"`
	AgentIdentity      string `json:"agent_identity,omitempty"`
	ASHAScenarioID     string `json:"asha_scenario_id,omitempty"`
	RepoCommit         string `json:"repo_commit,omitempty"`
	RepoBranch         string `json:"repo_branch,omitempty"`
	ASHARuntimeMode    string `json:"asha_runtime_mode,omitempty"`
	ArtifactRoot       string `json:"artifact_root,omitempty"`
	AuditCorrelationID string `json:"audit_correlation_id,omitempty"`
}

type SessionRequest struct {
	SessionID string `json:"session_id"`
}

type ListSessionsResponse struct {
	Sessions []CompositorSession `json:"sessions,omitempty"`
}

type CompositorLaunchProcess struct {
	LaunchID  string     `json:"launch_id"`
	SessionID string     `json:"session_id,omitempty"`
	PID       int        `json:"pid"`
	Command   string     `json:"command"`
	Cwd       string     `json:"cwd,omitempty"`
	Status    string     `json:"status"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	ExitedAt  *time.Time `json:"exited_at,omitempty"`
	Surfaces  []string   `json:"surfaces,omitempty"`
}

type LaunchAppRequest struct {
	SessionID          string            `json:"session_id,omitempty"`
	Command            string            `json:"command"`
	Cwd                string            `json:"cwd,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	SessionToken       string            `json:"session_token,omitempty"`
	AuditCorrelationID string            `json:"audit_correlation_id,omitempty"`
	RunAsUID           *uint32           `json:"run_as_uid,omitempty"`
	RunAsGID           *uint32           `json:"run_as_gid,omitempty"`
	ExpectedAppID      string            `json:"expected_app_id,omitempty"`
	ExpectedTitle      string            `json:"expected_title,omitempty"`
	Role               string            `json:"role,omitempty"`
	Output             string            `json:"output,omitempty"`
	WaitSurface        bool              `json:"wait_surface,omitempty"`
	WaitTimeoutMs      int               `json:"wait_timeout_ms,omitempty"`
}

type LaunchAppResponse struct {
	LaunchID  string                    `json:"launch_id"`
	SessionID string                    `json:"session_id,omitempty"`
	PID       int                       `json:"pid"`
	Surface   *CompositorTrackedSurface `json:"surface,omitempty"`
}

type ListProcessesRequest struct {
	SessionID string `json:"session_id,omitempty"`
}

type ListProcessesResponse struct {
	Processes []CompositorLaunchProcess `json:"processes,omitempty"`
}

type TerminateLaunchRequest struct {
	LaunchID     string `json:"launch_id"`
	SessionToken string `json:"session_token,omitempty"`
}

type TerminateLaunchResponse struct {
	LaunchID       string   `json:"launch_id"`
	SignalSent     bool     `json:"signal_sent"`
	ClosedSurfaces []string `json:"closed_surfaces,omitempty"`
}

type WaitForSurfaceRequest struct {
	SessionID string `json:"session_id,omitempty"`
	AppID     string `json:"app_id,omitempty"`
	Title     string `json:"title,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type WaitForSurfaceResponse struct {
	Surface CompositorTrackedSurface `json:"surface"`
}

type WaitForFrameRequest struct {
	SurfaceID  string `json:"surface_id"`
	AfterFrame uint64 `json:"after_frame,omitempty"`
	TimeoutMs  int    `json:"timeout_ms,omitempty"`
}

type WaitForFrameResponse struct {
	SurfaceID  string    `json:"surface_id"`
	FrameCount uint64    `json:"frame_count"`
	Timestamp  time.Time `json:"timestamp"`
}

type WaitForAppReadyRequest struct {
	LaunchID  string `json:"launch_id"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type WaitForRenderIdleRequest struct {
	SurfaceID string `json:"surface_id"`
	IdleMs    int    `json:"idle_ms"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type WaitForNoPendingRequest struct {
	SurfaceID string `json:"surface_id"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type WaitGenericResponse struct {
	OK        bool      `json:"ok"`
	SurfaceID string    `json:"surface_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Logical outputs are fixed-resolution viewport zones mapped onto the physical
// Wayfire output. They provide the ASHA virtual-output contract without relying
// on a backend-specific virtual-output creation protocol.
type LogicalOutput struct {
	Name           string    `json:"name"`
	Width          int       `json:"width"`
	Height         int       `json:"height"`
	Scale          float64   `json:"scale"`
	Mode           string    `json:"mode"`
	PhysicalX      int       `json:"physical_x"`
	PhysicalY      int       `json:"physical_y"`
	PhysicalWidth  int       `json:"physical_width"`
	PhysicalHeight int       `json:"physical_height"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Surfaces       []string  `json:"surfaces,omitempty"`
}

type OutputRequest struct {
	Name string `json:"name"`
}

type CreateOutputRequest struct {
	Name   string  `json:"name"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Scale  float64 `json:"scale,omitempty"`
}

type ResizeOutputRequest struct {
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type SetOutputScaleRequest struct {
	Name  string  `json:"name"`
	Scale float64 `json:"scale"`
}

type ListOutputsResponse struct {
	Outputs []LogicalOutput `json:"outputs,omitempty"`
}

type MoveSurfaceToOutputRequest struct {
	SurfaceID string `json:"surface_id"`
	Output    string `json:"output"`
}

type MoveSurfaceToOutputResponse struct {
	SurfaceID string          `json:"surface_id"`
	Output    string          `json:"output"`
	Geometry  SurfaceGeometry `json:"geometry"`
}

type ListOutputSurfacesResponse struct {
	Output   LogicalOutput              `json:"output"`
	Surfaces []CompositorTrackedSurface `json:"surfaces,omitempty"`
}

type CaptureOutputRequest struct {
	Name                  string `json:"name"`
	Export                bool   `json:"export,omitempty"`
	SessionID             string `json:"session_id,omitempty"`
	SessionToken          string `json:"session_token,omitempty"`
	AuditCorrelationID    string `json:"audit_correlation_id,omitempty"`
	EvidenceClass         string `json:"evidence_class,omitempty"`
	ASHACommandSequenceID string `json:"asha_command_sequence_id,omitempty"`
}

type CaptureOutputResponse struct {
	Output   string                   `json:"output"`
	Captures []CaptureSurfaceResponse `json:"captures,omitempty"`
	Warnings []string                 `json:"warnings,omitempty"`
}

type CompositorPlaceSurface struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	SurfaceID string          `json:"surface_id"`
	Geometry  SurfaceGeometry `json:"geometry"`
}

type CompositorPlacePluginResponse struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SurfaceID string `json:"surface_id"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

type A11yTreeRequest struct {
	SurfaceID string `json:"surface_id"`
	Depth     int    `json:"depth,omitempty"`
}

type A11yFindRequest struct {
	SurfaceID string `json:"surface_id"`
	Name      string `json:"name"`
	Depth     int    `json:"depth,omitempty"`
}

type A11yClickRequest struct {
	NodeID      string `json:"node_id"`
	ActionIndex int    `json:"action_index,omitempty"`
}

type A11yNode struct {
	ID           string     `json:"id"`
	Name         string     `json:"name,omitempty"`
	Role         string     `json:"role,omitempty"`
	SourceRole   string     `json:"source_role,omitempty"`
	SemanticRole string     `json:"semantic_role,omitempty"`
	Description  string     `json:"description,omitempty"`
	BusName      string     `json:"bus_name,omitempty"`
	Path         string     `json:"path,omitempty"`
	ChildCount   int        `json:"child_count,omitempty"`
	Interfaces   []string   `json:"interfaces,omitempty"`
	Actions      []string   `json:"actions,omitempty"`
	Children     []A11yNode `json:"children,omitempty"`
}

type A11yTreeResponse struct {
	SurfaceID string   `json:"surface_id"`
	Backend   string   `json:"backend"`
	Root      A11yNode `json:"root"`
}

type A11yFindResponse struct {
	SurfaceID string     `json:"surface_id"`
	Backend   string     `json:"backend"`
	Matches   []A11yNode `json:"matches,omitempty"`
}

type A11yClickResponse struct {
	NodeID      string `json:"node_id"`
	ActionIndex int    `json:"action_index"`
	ActionName  string `json:"action_name,omitempty"`
	OK          bool   `json:"ok"`
}

type AppCommandRequest struct {
	SurfaceID          string          `json:"surface_id"`
	Command            json.RawMessage `json:"command"`
	SessionID          string          `json:"session_id,omitempty"`
	SessionToken       string          `json:"session_token,omitempty"`
	AuditCorrelationID string          `json:"audit_correlation_id,omitempty"`
	TimeoutMs          int             `json:"timeout_ms,omitempty"`
}

type AppCommandResponse struct {
	RequestID          string                    `json:"request_id"`
	SurfaceID          string                    `json:"surface_id"`
	Endpoint           string                    `json:"endpoint"`
	StatusCode         int                       `json:"status_code,omitempty"`
	Result             json.RawMessage           `json:"result,omitempty"`
	StartedAt          time.Time                 `json:"started_at"`
	CompletedAt        time.Time                 `json:"completed_at"`
	Before             *CompositorTrackedSurface `json:"before,omitempty"`
	After              *CompositorTrackedSurface `json:"after,omitempty"`
	SessionID          string                    `json:"session_id,omitempty"`
	AuditCorrelationID string                    `json:"audit_correlation_id,omitempty"`
}

type AppResultRequest struct {
	RequestID    string `json:"request_id"`
	SessionID    string `json:"session_id,omitempty"`
	SessionToken string `json:"session_token,omitempty"`
}

type AppResultResponse struct {
	Command AppCommandResponse `json:"command"`
}

type UpsertSurfacePolicyRequest struct {
	Surface CompositorSurfacePolicy `json:"surface"`
}

type RemoveSurfacePolicyRequest struct {
	SurfaceID string `json:"surface_id"`
}

type SetInputContextRequest struct {
	ActorUID *uint32 `json:"actor_uid,omitempty"`
}

type CloseSurfaceRequest struct {
	SurfaceID string `json:"surface_id"`
}

type CloseSurfacesByUIDRequest struct {
	OwnerUID uint32 `json:"owner_uid"`
}

type CloseSurfacesResponse struct {
	Queued int `json:"queued"`
}

type ViewportGrantRequest struct {
	SurfaceID string                   `json:"surface_id"`
	AgentUID  uint32                   `json:"agent_uid"`
	Actions   []CompositorAccessAction `json:"actions,omitempty"`
}

type RevokeViewportGrantRequest struct {
	SurfaceID string `json:"surface_id"`
	AgentUID  uint32 `json:"agent_uid"`
}

type SurfaceAccessCheckRequest struct {
	SurfaceID string                 `json:"surface_id"`
	AgentUID  uint32                 `json:"agent_uid"`
	Action    CompositorAccessAction `json:"action"`
}

type SurfaceAccessGrant struct {
	SurfaceID    string                   `json:"surface_id"`
	AgentUID     uint32                   `json:"agent_uid"`
	Actions      []CompositorAccessAction `json:"actions"`
	GrantedByUID uint32                   `json:"granted_by_uid"`
	GrantedAt    time.Time                `json:"granted_at"`
}

type SurfaceAccessCheckResponse struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

type GrantViewportResponse struct {
	Grant SurfaceAccessGrant `json:"grant"`
}

type SurfaceGrantRecordKind string

const (
	GrantRecordGrant  SurfaceGrantRecordKind = "grant"
	GrantRecordRevoke SurfaceGrantRecordKind = "revoke"
)

type SurfaceGrantRecord struct {
	Kind         SurfaceGrantRecordKind   `json:"kind"`
	SurfaceID    string                   `json:"surface_id"`
	AgentUID     uint32                   `json:"agent_uid"`
	Actions      []CompositorAccessAction `json:"actions,omitempty"`
	GrantedByUID uint32                   `json:"granted_by_uid"`
	RecordedAt   time.Time                `json:"recorded_at"`
}
