package schema

import (
	"fmt"
	"math"
	"regexp"
	"time"
)

const BuiltinDevStandardLayoutID = "builtin/dev-standard"
const DefaultLayoutTagID = "default"

var layoutIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,95}$`)

type LayoutMode string

const (
	LayoutModeManual      LayoutMode = "manual"
	LayoutModeGrid        LayoutMode = "grid"
	LayoutModeMasterStack LayoutMode = "master_stack"
	LayoutModeMonocle     LayoutMode = "monocle"
)

type SurfaceManagementState string

const (
	SurfaceManaged   SurfaceManagementState = "managed"
	SurfaceUnmanaged SurfaceManagementState = "unmanaged"
	SurfaceTransient SurfaceManagementState = "transient"
)

type NormalizedRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type RegionInsets struct {
	Top    int `json:"top,omitempty"`
	Right  int `json:"right,omitempty"`
	Bottom int `json:"bottom,omitempty"`
	Left   int `json:"left,omitempty"`
}

type LayoutRegion struct {
	RegionID         string            `json:"region_id"`
	Name             string            `json:"name,omitempty"`
	OutputID         string            `json:"output_id,omitempty"`
	Workspace        *SurfaceWorkspace `json:"workspace,omitempty"`
	TagID            string            `json:"tag_id,omitempty"`
	RelativeGeometry *NormalizedRect   `json:"relative_geometry,omitempty"`
	ResolvedGeometry *SurfaceGeometry  `json:"resolved_geometry,omitempty"`
	Insets           *RegionInsets     `json:"insets,omitempty"`
}

type LayoutScope struct {
	ProjectID string            `json:"project_id,omitempty"`
	TaskID    int               `json:"task_id,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	OutputID  string            `json:"output_id,omitempty"`
	Workspace *SurfaceWorkspace `json:"workspace,omitempty"`
}

type LayoutTag struct {
	TagID       string            `json:"tag_id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Scope       LayoutScope       `json:"scope"`
	Visible     bool              `json:"visible"`
	Primary     bool              `json:"primary,omitempty"`
	LayoutID    string            `json:"layout_id,omitempty"`
	SurfaceIDs  []string          `json:"surface_ids,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at,omitempty"`
}

type TagView struct {
	ViewID        string            `json:"view_id"`
	OutputID      string            `json:"output_id,omitempty"`
	Workspace     *SurfaceWorkspace `json:"workspace,omitempty"`
	TagIDs        []string          `json:"tag_ids"`
	FocusedTag    string            `json:"focused_tag_id,omitempty"`
	ArrangementID string            `json:"arrangement_id,omitempty"`
}

type LayoutZone struct {
	ZoneID           string            `json:"zone_id"`
	Name             string            `json:"name"`
	ZoneNumber       int               `json:"zone_number,omitempty"`
	RegionID         string            `json:"region_id,omitempty"`
	RelativeGeometry NormalizedRect    `json:"relative_geometry"`
	ResolvedGeometry *SurfaceGeometry  `json:"resolved_geometry,omitempty"`
	RoleHint         string            `json:"role_hint,omitempty"`
	Constraints      *ZoneConstraints  `json:"constraints,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

type ZoneConstraints struct {
	MinWidth  int `json:"min_width,omitempty"`
	MinHeight int `json:"min_height,omitempty"`
	MaxWidth  int `json:"max_width,omitempty"`
	MaxHeight int `json:"max_height,omitempty"`
}

type LayoutDefinition struct {
	LayoutID  string            `json:"layout_id"`
	Name      string            `json:"name"`
	Mode      LayoutMode        `json:"mode"`
	Scope     LayoutScope       `json:"scope,omitempty"`
	Region    LayoutRegion      `json:"region"`
	Zones     []LayoutZone      `json:"zones"`
	Settings  map[string]any    `json:"settings,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Version   string            `json:"version,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
	UpdatedAt time.Time         `json:"updated_at,omitempty"`
}

type Arrangement struct {
	ArrangementID string             `json:"arrangement_id"`
	Name          string             `json:"name,omitempty"`
	Scope         LayoutScope        `json:"scope,omitempty"`
	TagViews      []TagView          `json:"tag_views,omitempty"`
	Tags          []LayoutTag        `json:"tags,omitempty"`
	Layouts       []LayoutDefinition `json:"layouts,omitempty"`
	Placements    []SurfacePlacement `json:"placements,omitempty"`
	CreatedAt     time.Time          `json:"created_at,omitempty"`
	CreatedBy     string             `json:"created_by,omitempty"`
	Metadata      map[string]string  `json:"metadata,omitempty"`
}

type SurfacePlacement struct {
	SurfaceID          string                 `json:"surface_id"`
	ManagementState    SurfaceManagementState `json:"management_state"`
	PrimaryTagID       string                 `json:"primary_tag_id,omitempty"`
	TagIDs             []string               `json:"tag_ids,omitempty"`
	LayoutID           string                 `json:"layout_id,omitempty"`
	RegionID           string                 `json:"region_id,omitempty"`
	ZoneID             string                 `json:"zone_id,omitempty"`
	ZoneNumber         int                    `json:"zone_number,omitempty"`
	Mode               LayoutMode             `json:"mode,omitempty"`
	TargetGeometry     *SurfaceGeometry       `json:"target_geometry,omitempty"`
	ResultGeometry     *SurfaceGeometry       `json:"result_geometry,omitempty"`
	PlacementReason    string                 `json:"placement_reason,omitempty"`
	AppliedBy          string                 `json:"applied_by,omitempty"`
	AppliedAt          time.Time              `json:"applied_at,omitempty"`
	Actor              string                 `json:"actor,omitempty"`
	ActorUID           *uint32                `json:"actor_uid,omitempty"`
	AuditCorrelationID string                 `json:"audit_correlation_id,omitempty"`
	RequestID          string                 `json:"request_id,omitempty"`
	Warnings           []string               `json:"warnings,omitempty"`
}

type PlacementPlan struct {
	PlanID             string                   `json:"plan_id,omitempty"`
	Action             string                   `json:"action"`
	TagID              string                   `json:"tag_id,omitempty"`
	LayoutID           string                   `json:"layout_id,omitempty"`
	RegionID           string                   `json:"region_id,omitempty"`
	Mode               LayoutMode               `json:"mode,omitempty"`
	Placements         []SurfacePlacementTarget `json:"placements"`
	Actor              string                   `json:"actor,omitempty"`
	ActorUID           *uint32                  `json:"actor_uid,omitempty"`
	RequestID          string                   `json:"request_id,omitempty"`
	AuditCorrelationID string                   `json:"audit_correlation_id,omitempty"`
	WaitTimeoutMs      int                      `json:"wait_timeout_ms,omitempty"`
}

type SurfacePlacementTarget struct {
	SurfaceID      string           `json:"surface_id"`
	TagID          string           `json:"tag_id,omitempty"`
	ZoneID         string           `json:"zone_id,omitempty"`
	ZoneNumber     int              `json:"zone_number,omitempty"`
	TargetGeometry *SurfaceGeometry `json:"target_geometry,omitempty"`
}

type PlacementResult struct {
	Action             string                `json:"action"`
	Decision           SurfaceActionDecision `json:"decision"`
	Reason             string                `json:"reason,omitempty"`
	Error              string                `json:"error,omitempty"`
	ErrorClass         string                `json:"error_class,omitempty"`
	TagID              string                `json:"tag_id,omitempty"`
	LayoutID           string                `json:"layout_id,omitempty"`
	ArrangementID      string                `json:"arrangement_id,omitempty"`
	Placements         []SurfacePlacement    `json:"placements,omitempty"`
	Actor              string                `json:"actor,omitempty"`
	ActorUID           *uint32               `json:"actor_uid,omitempty"`
	RequestID          string                `json:"request_id,omitempty"`
	AuditCorrelationID string                `json:"audit_correlation_id,omitempty"`
	Warnings           []string              `json:"warnings,omitempty"`
}

type ListLayoutZonesRequest struct {
	TagID    string `json:"tag_id,omitempty"`
	LayoutID string `json:"layout_id,omitempty"`
	OutputID string `json:"output_id,omitempty"`
}

type ListLayoutZonesResponse struct {
	Layout LayoutDefinition `json:"layout"`
	Zones  []LayoutZone     `json:"zones"`
}

type AssignSurfaceTagRequest struct {
	SurfaceID          string     `json:"surface_id"`
	TagID              string     `json:"tag_id"`
	ZoneID             string     `json:"zone_id,omitempty"`
	LayoutID           string     `json:"layout_id,omitempty"`
	Mode               LayoutMode `json:"mode,omitempty"`
	PlacementReason    string     `json:"placement_reason,omitempty"`
	WaitTimeoutMs      int        `json:"wait_timeout_ms,omitempty"`
	AuditCorrelationID string     `json:"audit_correlation_id,omitempty"`
}

type GetArrangementRequest struct {
	TagID    string `json:"tag_id,omitempty"`
	OutputID string `json:"output_id,omitempty"`
}

type GetArrangementResponse struct {
	Arrangement Arrangement `json:"arrangement"`
}

func BuiltinDevStandardLayout() LayoutDefinition {
	return LayoutDefinition{
		LayoutID: BuiltinDevStandardLayoutID,
		Name:     "Development Standard",
		Mode:     LayoutModeManual,
		Region:   LayoutRegion{RegionID: "main"},
		Version:  "1",
		Zones: []LayoutZone{
			{ZoneID: "editor", Name: "Editor", ZoneNumber: 1, RelativeGeometry: NormalizedRect{X: 0, Y: 0, Width: 0.62, Height: 1}, RoleHint: "editor"},
			{ZoneID: "terminal", Name: "Terminal", ZoneNumber: 2, RelativeGeometry: NormalizedRect{X: 0.62, Y: 0.52, Width: 0.38, Height: 0.48}, RoleHint: "terminal"},
			{ZoneID: "preview", Name: "Preview", ZoneNumber: 3, RelativeGeometry: NormalizedRect{X: 0.62, Y: 0, Width: 0.38, Height: 0.52}, RoleHint: "preview"},
		},
	}
}

func DefaultLayoutTag(now time.Time) LayoutTag {
	return LayoutTag{TagID: DefaultLayoutTagID, Name: "Default", Scope: LayoutScope{}, Visible: true, Primary: true, LayoutID: BuiltinDevStandardLayoutID, CreatedAt: now, UpdatedAt: now}
}

func ValidateLayoutID(id string) error { return validateLayoutSlug("layout_id", id) }
func ValidateTagID(id string) error    { return validateLayoutSlug("tag_id", id) }
func ValidateZoneID(id string) error   { return validateLayoutSlug("zone_id", id) }

func validateLayoutSlug(field, id string) error {
	if !layoutIDPattern.MatchString(id) {
		return fmt.Errorf("%s %q must match %s", field, id, layoutIDPattern.String())
	}
	return nil
}

func ValidateLayoutMode(mode LayoutMode) error {
	switch mode {
	case LayoutModeManual, LayoutModeGrid, LayoutModeMasterStack, LayoutModeMonocle:
		return nil
	default:
		return fmt.Errorf("unsupported layout mode %q", mode)
	}
}

func ValidateImplementedLayoutMode(mode LayoutMode) error {
	if mode == "" {
		mode = LayoutModeManual
	}
	if err := ValidateLayoutMode(mode); err != nil {
		return err
	}
	if mode != LayoutModeManual {
		return fmt.Errorf("layout mode %q is not implemented in this vertical slice", mode)
	}
	return nil
}

func ValidateNormalizedRect(rect NormalizedRect) error {
	values := []struct {
		name  string
		value float64
	}{{"x", rect.X}, {"y", rect.Y}, {"width", rect.Width}, {"height", rect.Height}}
	for _, item := range values {
		if math.IsNaN(item.value) || math.IsInf(item.value, 0) {
			return fmt.Errorf("%s must be finite", item.name)
		}
		if item.value < 0 {
			return fmt.Errorf("%s must be non-negative", item.name)
		}
	}
	if rect.Width <= 0 || rect.Height <= 0 {
		return fmt.Errorf("width and height must be positive")
	}
	const eps = 1e-9
	if rect.X+rect.Width > 1+eps || rect.Y+rect.Height > 1+eps {
		return fmt.Errorf("normalized rectangle exceeds bounds")
	}
	return nil
}

func ValidateLayoutZone(zone LayoutZone) error {
	if err := ValidateZoneID(zone.ZoneID); err != nil {
		return err
	}
	if zone.Name == "" {
		return fmt.Errorf("zone %s name is required", zone.ZoneID)
	}
	if zone.ZoneNumber < 0 {
		return fmt.Errorf("zone %s zone_number must be positive when set", zone.ZoneID)
	}
	if err := ValidateNormalizedRect(zone.RelativeGeometry); err != nil {
		return fmt.Errorf("zone %s geometry: %w", zone.ZoneID, err)
	}
	if zone.Constraints != nil {
		if zone.Constraints.MinWidth < 0 || zone.Constraints.MinHeight < 0 || zone.Constraints.MaxWidth < 0 || zone.Constraints.MaxHeight < 0 {
			return fmt.Errorf("zone %s constraints must be non-negative", zone.ZoneID)
		}
	}
	return nil
}

func ValidateLayoutDefinition(layout LayoutDefinition) error {
	if err := ValidateLayoutID(layout.LayoutID); err != nil {
		return err
	}
	if layout.Name == "" {
		return fmt.Errorf("layout name is required")
	}
	if err := ValidateLayoutMode(layout.Mode); err != nil {
		return err
	}
	if layout.Region.RegionID == "" {
		return fmt.Errorf("region_id is required")
	}
	if len(layout.Zones) == 0 {
		return fmt.Errorf("at least one zone is required")
	}
	zoneIDs := map[string]struct{}{}
	numbers := map[int]struct{}{}
	for _, zone := range layout.Zones {
		if err := ValidateLayoutZone(zone); err != nil {
			return err
		}
		if _, ok := zoneIDs[zone.ZoneID]; ok {
			return fmt.Errorf("duplicate zone_id %q", zone.ZoneID)
		}
		zoneIDs[zone.ZoneID] = struct{}{}
		if zone.ZoneNumber > 0 {
			if _, ok := numbers[zone.ZoneNumber]; ok {
				return fmt.Errorf("duplicate zone_number %d", zone.ZoneNumber)
			}
			numbers[zone.ZoneNumber] = struct{}{}
		}
	}
	return nil
}

func ResolveZoneGeometry(region SurfaceGeometry, insets *RegionInsets, zone LayoutZone) (SurfaceGeometry, error) {
	if err := ValidateLayoutZone(zone); err != nil {
		return SurfaceGeometry{}, err
	}
	usable := region
	if insets != nil {
		usable.X += insets.Left
		usable.Y += insets.Top
		usable.Width -= insets.Left + insets.Right
		usable.Height -= insets.Top + insets.Bottom
	}
	if usable.Width <= 0 || usable.Height <= 0 {
		return SurfaceGeometry{}, fmt.Errorf("invalid layout geometry: usable region is empty")
	}
	x := usable.X + int(math.Round(zone.RelativeGeometry.X*float64(usable.Width)))
	y := usable.Y + int(math.Round(zone.RelativeGeometry.Y*float64(usable.Height)))
	w := int(math.Round(zone.RelativeGeometry.Width * float64(usable.Width)))
	h := int(math.Round(zone.RelativeGeometry.Height * float64(usable.Height)))
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	if x+w > usable.X+usable.Width {
		w = usable.X + usable.Width - x
	}
	if y+h > usable.Y+usable.Height {
		h = usable.Y + usable.Height - y
	}
	if zone.Constraints != nil {
		if zone.Constraints.MinWidth > 0 && w < zone.Constraints.MinWidth {
			return SurfaceGeometry{}, fmt.Errorf("invalid layout geometry: zone %s min_width cannot be satisfied", zone.ZoneID)
		}
		if zone.Constraints.MinHeight > 0 && h < zone.Constraints.MinHeight {
			return SurfaceGeometry{}, fmt.Errorf("invalid layout geometry: zone %s min_height cannot be satisfied", zone.ZoneID)
		}
		if zone.Constraints.MaxWidth > 0 && w > zone.Constraints.MaxWidth {
			w = zone.Constraints.MaxWidth
		}
		if zone.Constraints.MaxHeight > 0 && h > zone.Constraints.MaxHeight {
			h = zone.Constraints.MaxHeight
		}
	}
	if w <= 0 || h <= 0 {
		return SurfaceGeometry{}, fmt.Errorf("invalid layout geometry: resolved zone %s is empty", zone.ZoneID)
	}
	return SurfaceGeometry{X: x, Y: y, Width: w, Height: h}, nil
}
