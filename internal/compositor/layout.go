package compositor

import (
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

func builtinLayoutDefinitions() map[string]schema.LayoutDefinition {
	layout := schema.BuiltinDevStandardLayout()
	return map[string]schema.LayoutDefinition{layout.LayoutID: layout}
}

func builtinLayoutTags() map[string]schema.LayoutTag {
	now := time.Now()
	tag := schema.DefaultLayoutTag(now)
	return map[string]schema.LayoutTag{tag.TagID: tag}
}

func (b *Bridge) ListLayoutZones(req schema.ListLayoutZonesRequest) (schema.ListLayoutZonesResponse, error) {
	layoutID := req.LayoutID
	if layoutID == "" {
		layoutID = schema.BuiltinDevStandardLayoutID
	}
	b.mu.RLock()
	layout, ok := b.layoutDefinitions[layoutID]
	b.mu.RUnlock()
	if !ok {
		return schema.ListLayoutZonesResponse{}, compositorError(schema.ErrorLayoutNotFound, "layout %s not found", layoutID)
	}
	region := b.layoutRegionGeometry(req.OutputID, layout.Region)
	layout.Region.ResolvedGeometry = &region
	zones := make([]schema.LayoutZone, 0, len(layout.Zones))
	for _, zone := range layout.Zones {
		geom, err := schema.ResolveZoneGeometry(region, layout.Region.Insets, zone)
		if err != nil {
			return schema.ListLayoutZonesResponse{}, compositorError(schema.ErrorInvalidLayoutGeometry, "layout %s zone %s: %v", layout.LayoutID, zone.ZoneID, err)
		}
		zone.ResolvedGeometry = &geom
		zones = append(zones, zone)
	}
	sort.Slice(zones, func(i, j int) bool {
		if zones[i].ZoneNumber != zones[j].ZoneNumber {
			return zones[i].ZoneNumber < zones[j].ZoneNumber
		}
		return zones[i].ZoneID < zones[j].ZoneID
	})
	layout.Zones = zones
	return schema.ListLayoutZonesResponse{Layout: layout, Zones: zones}, nil
}

func (b *Bridge) layoutRegionGeometry(outputID string, region schema.LayoutRegion) schema.SurfaceGeometry {
	if region.ResolvedGeometry != nil && region.ResolvedGeometry.Width > 0 && region.ResolvedGeometry.Height > 0 {
		return *region.ResolvedGeometry
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if outputID == "" {
		outputID = region.OutputID
	}
	if outputID != "" {
		if out, ok := b.outputs[outputID]; ok {
			return schema.SurfaceGeometry{X: out.PhysicalX, Y: out.PhysicalY, Width: out.PhysicalWidth, Height: out.PhysicalHeight}
		}
	}
	w, h := b.physicalBoundsLocked()
	return schema.SurfaceGeometry{X: 0, Y: 0, Width: w, Height: h}
}

func (b *Bridge) AssignSurfaceTag(actorUID uint32, req schema.AssignSurfaceTagRequest) (schema.PlacementResult, error) {
	actor, uid := b.surfaceActionActor(actorUID)
	if req.SurfaceID == "" {
		return b.publishLayoutDenied(req, actor, uid, fmt.Errorf("surface_id is required"), schema.ErrorProtocolError, nil)
	}
	if req.TagID == "" {
		req.TagID = schema.DefaultLayoutTagID
	}
	if req.LayoutID == "" {
		req.LayoutID = schema.BuiltinDevStandardLayoutID
	}
	if req.Mode == "" {
		req.Mode = schema.LayoutModeManual
	}
	if err := schema.ValidateTagID(req.TagID); err != nil {
		return b.publishLayoutDenied(req, actor, uid, err, schema.ErrorProtocolError, nil)
	}
	if err := schema.ValidateImplementedLayoutMode(req.Mode); err != nil {
		return b.publishLayoutDenied(req, actor, uid, compositorError(schema.ErrorUnsupportedLayoutMode, "%v", err), schema.ErrorUnsupportedLayoutMode, nil)
	}

	b.mu.RLock()
	tracked, ok := b.surfaces[req.SurfaceID]
	_, stale := b.staleSurfaces[req.SurfaceID]
	layout, layoutOK := b.layoutDefinitions[req.LayoutID]
	tag, tagOK := b.layoutTags[req.TagID]
	b.mu.RUnlock()
	if !ok {
		if stale {
			return b.publishLayoutDenied(req, actor, uid, compositorError(schema.ErrorSurfaceStale, "surface %s is unmapped/stale", req.SurfaceID), schema.ErrorSurfaceStale, nil)
		}
		return b.publishLayoutDenied(req, actor, uid, compositorError(schema.ErrorSurfaceNotFound, "surface %s not found", req.SurfaceID), schema.ErrorSurfaceNotFound, nil)
	}
	if tracked.Surface.SurfaceKind == schema.SurfaceKindLayerShell {
		return b.publishLayoutDenied(req, actor, uid, compositorError(schema.ErrorBackendUnsupported, "surface %s is a layer-shell surface and cannot be managed in a work layout", req.SurfaceID), schema.ErrorBackendUnsupported, nil)
	}
	if !tracked.Visible {
		return b.publishLayoutDenied(req, actor, uid, compositorError(schema.ErrorSurfaceStale, "surface %s is not visible", req.SurfaceID), schema.ErrorSurfaceStale, nil)
	}
	if !layoutOK {
		return b.publishLayoutDenied(req, actor, uid, compositorError(schema.ErrorLayoutNotFound, "layout %s not found", req.LayoutID), schema.ErrorLayoutNotFound, nil)
	}
	if !tagOK {
		return b.publishLayoutDenied(req, actor, uid, compositorError(schema.ErrorLayoutTagNotFound, "tag %s not found", req.TagID), schema.ErrorLayoutTagNotFound, nil)
	}

	zone, err := findLayoutZone(layout, req.ZoneID)
	if err != nil {
		return b.publishLayoutDenied(req, actor, uid, err, schema.ErrorLayoutZoneNotFound, nil)
	}
	region := b.layoutRegionGeometry(tracked.OutputID, layout.Region)
	zoneGeometry, err := schema.ResolveZoneGeometry(region, layout.Region.Insets, zone)
	if err != nil {
		err = compositorError(schema.ErrorInvalidLayoutGeometry, "%v", err)
		return b.publishLayoutDenied(req, actor, uid, err, schema.ErrorInvalidLayoutGeometry, nil)
	}
	// Arbitrary xdg-toplevel resizing is not a reliable live Wayfire substrate.
	// Reuse the proven surface.tile behavior: preserve the current client size when
	// it fits and center/place that toplevel inside the selected layout zone.
	target := centeredTileGeometry(zoneGeometry, tracked.Geometry)
	if err := b.placeSurface(req.SurfaceID, target, time.Duration(req.WaitTimeoutMs)*time.Millisecond, true); err != nil {
		class, _ := classifyError(err)
		if class == schema.ErrorFrameTimeout {
			err = compositorError(schema.ErrorLayoutReadbackTimeout, "%v", err)
			class = schema.ErrorLayoutReadbackTimeout
		}
		return b.publishLayoutDenied(req, actor, uid, err, class, &target)
	}

	b.mu.RLock()
	updated, ok := b.surfaces[req.SurfaceID]
	if ok {
		updated = b.decorateSurfaceLocked(updated)
	}
	b.mu.RUnlock()
	if !ok || updated.Geometry == nil {
		err := compositorError(schema.ErrorLayoutReadbackTimeout, "surface %s layout placement produced no geometry readback", req.SurfaceID)
		return b.publishLayoutDenied(req, actor, uid, err, schema.ErrorLayoutReadbackTimeout, &target)
	}
	resultGeom := *updated.Geometry
	placement := schema.SurfacePlacement{
		SurfaceID:          req.SurfaceID,
		ManagementState:    schema.SurfaceManaged,
		PrimaryTagID:       tag.TagID,
		TagIDs:             []string{tag.TagID},
		LayoutID:           layout.LayoutID,
		RegionID:           layout.Region.RegionID,
		ZoneID:             zone.ZoneID,
		ZoneNumber:         zone.ZoneNumber,
		Mode:               req.Mode,
		TargetGeometry:     &target,
		ResultGeometry:     &resultGeom,
		PlacementReason:    req.PlacementReason,
		AppliedBy:          actor,
		AppliedAt:          time.Now(),
		Actor:              actor,
		ActorUID:           uid,
		AuditCorrelationID: req.AuditCorrelationID,
	}
	b.mu.Lock()
	b.surfacePlacements[req.SurfaceID] = placement
	if current, ok := b.surfaces[req.SurfaceID]; ok {
		current.Surface.ManagementState = placement.ManagementState
		current.Surface.Placement = &placement
		b.surfaces[req.SurfaceID] = current
	}
	tag.SurfaceIDs = appendUniqueSorted(tag.SurfaceIDs, req.SurfaceID)
	tag.UpdatedAt = time.Now()
	b.layoutTags[tag.TagID] = tag
	b.mu.Unlock()
	result := schema.PlacementResult{Action: "layout.assign_surface_tag", Decision: schema.SurfaceActionAccepted, Reason: "surface assigned to layout zone", TagID: tag.TagID, LayoutID: layout.LayoutID, Placements: []schema.SurfacePlacement{placement}, Actor: actor, ActorUID: uid, AuditCorrelationID: req.AuditCorrelationID}
	if b.bus != nil {
		if err := b.bus.Publish(schema.TopicShellLayoutApplied, result); err != nil {
			log.Printf("publish shell layout applied: %v", err)
		}
	}
	return result, nil
}

func findLayoutZone(layout schema.LayoutDefinition, zoneID string) (schema.LayoutZone, error) {
	if zoneID == "" && len(layout.Zones) > 0 {
		return layout.Zones[0], nil
	}
	for _, zone := range layout.Zones {
		if zone.ZoneID == zoneID {
			return zone, nil
		}
	}
	return schema.LayoutZone{}, compositorError(schema.ErrorLayoutZoneNotFound, "zone %s not found in layout %s", zoneID, layout.LayoutID)
}

func (b *Bridge) publishLayoutDenied(req schema.AssignSurfaceTagRequest, actor string, uid *uint32, err error, class string, target *schema.SurfaceGeometry) (schema.PlacementResult, error) {
	message := err.Error()
	if class == "" {
		class, message = classifyError(err)
		if class == "" {
			class = schema.ErrorProtocolError
		}
	}
	placement := schema.SurfacePlacement{SurfaceID: req.SurfaceID, ManagementState: schema.SurfaceUnmanaged, PrimaryTagID: req.TagID, LayoutID: req.LayoutID, ZoneID: req.ZoneID, Mode: req.Mode, TargetGeometry: target, PlacementReason: req.PlacementReason, Actor: actor, ActorUID: uid, AuditCorrelationID: req.AuditCorrelationID}
	result := schema.PlacementResult{Action: "layout.assign_surface_tag", Decision: schema.SurfaceActionDenied, Reason: message, Error: message, ErrorClass: class, TagID: req.TagID, LayoutID: req.LayoutID, Placements: []schema.SurfacePlacement{placement}, Actor: actor, ActorUID: uid, AuditCorrelationID: req.AuditCorrelationID}
	if b.bus != nil {
		if publishErr := b.bus.Publish(schema.TopicShellLayoutDenied, result); publishErr != nil {
			log.Printf("publish shell layout denied: %v", publishErr)
		}
	}
	return schema.PlacementResult{}, err
}

func appendUniqueSorted(values []string, value string) []string {
	seen := map[string]struct{}{}
	for _, existing := range values {
		if existing != "" {
			seen[existing] = struct{}{}
		}
	}
	if value != "" {
		seen[value] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for existing := range seen {
		out = append(out, existing)
	}
	sort.Strings(out)
	return out
}

func (b *Bridge) GetArrangement(req schema.GetArrangementRequest) (schema.GetArrangementResponse, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	now := time.Now()
	tags := make([]schema.LayoutTag, 0, len(b.layoutTags))
	for _, tag := range b.layoutTags {
		if req.TagID == "" || tag.TagID == req.TagID {
			copyTag := tag
			copyTag.SurfaceIDs = append([]string(nil), tag.SurfaceIDs...)
			tags = append(tags, copyTag)
		}
	}
	if req.TagID != "" && len(tags) == 0 {
		return schema.GetArrangementResponse{}, compositorError(schema.ErrorLayoutTagNotFound, "tag %s not found", req.TagID)
	}
	layouts := make([]schema.LayoutDefinition, 0, len(b.layoutDefinitions))
	for _, layout := range b.layoutDefinitions {
		layouts = append(layouts, layout)
	}
	placements := make([]schema.SurfacePlacement, 0, len(b.surfacePlacements))
	for _, placement := range b.surfacePlacements {
		if req.TagID == "" || placement.PrimaryTagID == req.TagID {
			placements = append(placements, placement)
		}
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].TagID < tags[j].TagID })
	sort.Slice(layouts, func(i, j int) bool { return layouts[i].LayoutID < layouts[j].LayoutID })
	sort.Slice(placements, func(i, j int) bool { return placements[i].SurfaceID < placements[j].SurfaceID })
	view := schema.TagView{ViewID: "default-view", TagIDs: []string{schema.DefaultLayoutTagID}, FocusedTag: schema.DefaultLayoutTagID, ArrangementID: "current"}
	arrangement := schema.Arrangement{ArrangementID: "current", Name: "Current shell arrangement", Tags: tags, Layouts: layouts, Placements: placements, TagViews: []schema.TagView{view}, CreatedAt: now, CreatedBy: "compositor-bridge"}
	return schema.GetArrangementResponse{Arrangement: arrangement}, nil
}

func unmanagedPlacement(surface schema.CompositorTrackedSurface) schema.SurfacePlacement {
	placement := schema.SurfacePlacement{SurfaceID: surface.Surface.ID, ManagementState: schema.SurfaceUnmanaged}
	if surface.Geometry != nil {
		geom := *surface.Geometry
		placement.ResultGeometry = &geom
	}
	return placement
}
