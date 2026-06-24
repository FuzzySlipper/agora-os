package schema

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLayoutSchemasJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 6, 24, 8, 0, 0, 0, time.UTC)
	uid := uint32(60002)
	geom := SurfaceGeometry{X: 10, Y: 20, Width: 300, Height: 200}
	placement := SurfacePlacement{SurfaceID: "view-1", ManagementState: SurfaceManaged, PrimaryTagID: "default", TagIDs: []string{"default"}, LayoutID: BuiltinDevStandardLayoutID, RegionID: "main", ZoneID: "editor", ZoneNumber: 1, Mode: LayoutModeManual, TargetGeometry: &geom, ResultGeometry: &geom, AppliedBy: "runner", AppliedAt: now, ActorUID: &uid}
	values := []any{
		DefaultLayoutTag(now),
		LayoutRegion{RegionID: "main", RelativeGeometry: &NormalizedRect{X: 0, Y: 0, Width: 1, Height: 1}, Insets: &RegionInsets{Top: 24}},
		BuiltinDevStandardLayout().Zones[0],
		LayoutModeManual,
		BuiltinDevStandardLayout(),
		Arrangement{ArrangementID: "current", Tags: []LayoutTag{DefaultLayoutTag(now)}, Layouts: []LayoutDefinition{BuiltinDevStandardLayout()}, Placements: []SurfacePlacement{placement}, CreatedAt: now},
		PlacementPlan{Action: "layout.assign_surface_tag", TagID: "default", LayoutID: BuiltinDevStandardLayoutID, Placements: []SurfacePlacementTarget{{SurfaceID: "view-1", ZoneID: "editor", TargetGeometry: &geom}}, ActorUID: &uid},
		PlacementResult{Action: "layout.assign_surface_tag", Decision: SurfaceActionAccepted, TagID: "default", LayoutID: BuiltinDevStandardLayoutID, Placements: []SurfacePlacement{placement}, ActorUID: &uid},
		placement,
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %T: %v", value, err)
		}
		if !json.Valid(encoded) {
			t.Fatalf("invalid JSON for %T: %s", value, encoded)
		}
	}
}

func TestBuiltinDevStandardLayoutValidatesAndResolves(t *testing.T) {
	layout := BuiltinDevStandardLayout()
	if err := ValidateLayoutDefinition(layout); err != nil {
		t.Fatalf("builtin layout validation failed: %v", err)
	}
	region := SurfaceGeometry{X: 0, Y: 0, Width: 2000, Height: 1000}
	zones := map[string]SurfaceGeometry{}
	for _, zone := range layout.Zones {
		geom, err := ResolveZoneGeometry(region, layout.Region.Insets, zone)
		if err != nil {
			t.Fatalf("resolve %s: %v", zone.ZoneID, err)
		}
		zones[zone.ZoneID] = geom
	}
	if got := zones["editor"]; got.X != 0 || got.Y != 0 || got.Width != 1240 || got.Height != 1000 {
		t.Fatalf("editor geometry = %+v", got)
	}
	if got := zones["terminal"]; got.X != 1240 || got.Y != 520 || got.Width != 760 || got.Height != 480 {
		t.Fatalf("terminal geometry = %+v", got)
	}
	if got := zones["preview"]; got.X != 1240 || got.Y != 0 || got.Width != 760 || got.Height != 520 {
		t.Fatalf("preview geometry = %+v", got)
	}
}

func TestResolveZoneGeometryInsetsClampAndConstraints(t *testing.T) {
	zone := LayoutZone{ZoneID: "small", Name: "Small", RelativeGeometry: NormalizedRect{X: 0.333, Y: 0.25, Width: 0.333, Height: 0.5}, Constraints: &ZoneConstraints{MaxWidth: 100}}
	geom, err := ResolveZoneGeometry(SurfaceGeometry{X: 10, Y: 20, Width: 301, Height: 101}, &RegionInsets{Top: 5, Right: 1, Bottom: 6, Left: 3}, zone)
	if err != nil {
		t.Fatalf("ResolveZoneGeometry returned error: %v", err)
	}
	if geom.X != 112 || geom.Y != 48 || geom.Width != 99 || geom.Height != 45 {
		t.Fatalf("resolved geometry with insets = %+v", geom)
	}

	zone.Constraints = &ZoneConstraints{MinWidth: 500}
	if _, err := ResolveZoneGeometry(SurfaceGeometry{Width: 100, Height: 100}, nil, zone); err == nil {
		t.Fatal("expected unsatisfied min_width error")
	}
}

func TestValidateLayoutRejectsInvalidIDsAndMode(t *testing.T) {
	if err := ValidateTagID("Bad Tag"); err == nil {
		t.Fatal("expected invalid tag id")
	}
	if err := ValidateZoneID("bad zone"); err == nil {
		t.Fatal("expected invalid zone id")
	}
	if err := ValidateLayoutMode(LayoutMode("floating")); err == nil {
		t.Fatal("expected invalid layout mode")
	}
	if err := ValidateImplementedLayoutMode(LayoutModeGrid); err == nil {
		t.Fatal("expected unsupported implemented layout mode")
	}
}

func TestValidateLayoutRejectsInvalidZone(t *testing.T) {
	layout := BuiltinDevStandardLayout()
	layout.Zones[0].RelativeGeometry = NormalizedRect{X: 0.8, Y: 0, Width: 0.5, Height: 1}
	if err := ValidateLayoutDefinition(layout); err == nil {
		t.Fatal("expected invalid zone geometry")
	}
}
