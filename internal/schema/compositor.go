package schema

import "time"

const (
	CompositorPluginSocket  = "/run/agent-os/compositor-bridge.sock"
	CompositorControlSocket = "/run/agent-os/compositor-control.sock"
)

const (
	MethodListSurfaces        = "list_surfaces"
	MethodUpsertSurfacePolicy = "upsert_surface_policy"
	MethodRemoveSurfacePolicy = "remove_surface_policy"
	MethodSetInputContext     = "set_input_context"
	MethodCloseSurface        = "close_surface"
	MethodCloseSurfacesByUID  = "close_surfaces_by_uid"
)

const (
	TopicCompositorSurfaceCreated   = "compositor.surface.created"
	TopicCompositorSurfaceDestroyed = "compositor.surface.destroyed"
	TopicCompositorSurfaceFocused   = "compositor.surface.focused"
	TopicCompositorSurfaceInput     = "compositor.surface.input"
)

type CompositorPluginMessageType string

const (
	PluginMessageSurfaceEvent       CompositorPluginMessageType = "surface_event"
	PluginMessagePolicyReplace      CompositorPluginMessageType = "policy_replace"
	PluginMessagePolicyUpsert       CompositorPluginMessageType = "policy_upsert"
	PluginMessagePolicyRemove       CompositorPluginMessageType = "policy_remove"
	PluginMessageInputContext       CompositorPluginMessageType = "input_context"
	PluginMessageCloseSurface       CompositorPluginMessageType = "close_surface"
	PluginMessageCloseSurfacesByUID CompositorPluginMessageType = "close_surfaces_by_uid"
)

type CompositorSurfaceEventName string

const (
	SurfaceEventMapped      CompositorSurfaceEventName = "mapped"
	SurfaceEventUnmapped    CompositorSurfaceEventName = "unmapped"
	SurfaceEventFocused     CompositorSurfaceEventName = "focused"
	SurfaceEventInputDenied CompositorSurfaceEventName = "input_denied"
)

type CompositorSurface struct {
	ID            string `json:"id"`
	WayfireViewID uint32 `json:"wayfire_view_id"`
	AppID         string `json:"app_id,omitempty"`
	Title         string `json:"title,omitempty"`
	Role          string `json:"role,omitempty"`
}

type CompositorClientIdentity struct {
	PID int32  `json:"pid"`
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}

type CompositorPluginEvent struct {
	Type    CompositorPluginMessageType `json:"type"`
	Event   CompositorSurfaceEventName  `json:"event,omitempty"`
	Device  string                      `json:"device,omitempty"`
	Surface CompositorSurface           `json:"surface"`
	Client  CompositorClientIdentity    `json:"client"`
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

type CompositorCloseSurface struct {
	Type      CompositorPluginMessageType `json:"type"`
	SurfaceID string                      `json:"surface_id"`
}

type CompositorCloseSurfacesByUID struct {
	Type     CompositorPluginMessageType `json:"type"`
	OwnerUID uint32                      `json:"owner_uid"`
}

type CompositorTrackedSurface struct {
	Surface   CompositorSurface          `json:"surface"`
	Client    CompositorClientIdentity   `json:"client"`
	LastEvent CompositorSurfaceEventName `json:"last_event"`
	Device    string                     `json:"device,omitempty"`
	UpdatedAt time.Time                  `json:"updated_at"`
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
