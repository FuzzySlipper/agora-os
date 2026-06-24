package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/patch/agora-os/internal/schema"
)

func cmdLayout(args []string, pretty bool) error {
	if len(args) == 0 {
		return fmt.Errorf("layout subcommand is required: zones, assign, arrangement")
	}
	switch args[0] {
	case "zones", "list-zones":
		req, err := buildListLayoutZonesRequest(args[1:])
		if err != nil {
			return err
		}
		return callAndPrint(schema.MethodListLayoutZones, req, pretty)
	case "assign", "assign-surface":
		req, err := buildAssignSurfaceTagRequest(args[1:])
		if err != nil {
			return err
		}
		return callAndPrint(schema.MethodAssignSurfaceTag, req, pretty)
	case "arrangement", "get-arrangement":
		fs := flag.NewFlagSet("layout arrangement", flag.ExitOnError)
		tagID := fs.String("tag", "", "tag id")
		outputID := fs.String("output", "", "logical output id")
		fs.Parse(args[1:])
		return callAndPrint(schema.MethodGetArrangement, schema.GetArrangementRequest{TagID: *tagID, OutputID: *outputID}, pretty)
	default:
		return fmt.Errorf("unknown layout subcommand: %s", args[0])
	}
}

func buildListLayoutZonesRequest(args []string) (schema.ListLayoutZonesRequest, error) {
	fs := flag.NewFlagSet("layout zones", flag.ExitOnError)
	layoutID := fs.String("layout", "", "layout id (defaults to builtin/dev-standard)")
	tagID := fs.String("tag", "", "tag id")
	outputID := fs.String("output", "", "logical output id")
	fs.Parse(args)
	return schema.ListLayoutZonesRequest{LayoutID: *layoutID, TagID: *tagID, OutputID: *outputID}, nil
}

func buildAssignSurfaceTagRequest(args []string) (schema.AssignSurfaceTagRequest, error) {
	fs := flag.NewFlagSet("layout assign", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "surface id to manage")
	tagID := fs.String("tag", schema.DefaultLayoutTagID, "tag id")
	zoneID := fs.String("zone", "", "zone id")
	layoutID := fs.String("layout", schema.BuiltinDevStandardLayoutID, "layout id")
	mode := fs.String("mode", string(schema.LayoutModeManual), "layout mode")
	reason := fs.String("reason", "", "placement reason")
	timeout := fs.Int("timeout-ms", 2000, "placement readback timeout in milliseconds")
	auditID := fs.String("audit-correlation-id", os.Getenv("AGORA_AUDIT_CORRELATION_ID"), "audit correlation id")
	fs.Parse(args)
	if *surfaceID == "" {
		return schema.AssignSurfaceTagRequest{}, fmt.Errorf("--surface is required")
	}
	return schema.AssignSurfaceTagRequest{SurfaceID: *surfaceID, TagID: *tagID, ZoneID: *zoneID, LayoutID: *layoutID, Mode: schema.LayoutMode(*mode), PlacementReason: *reason, WaitTimeoutMs: *timeout, AuditCorrelationID: *auditID}, nil
}
