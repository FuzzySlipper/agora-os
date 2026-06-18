package schema

import (
	"encoding/json"
	"testing"
)

func TestSetViewPropertyRequestRoundTrip(t *testing.T) {
	t.Parallel()

	want := SetViewPropertyRequest{
		SurfaceID: "view-42",
		Properties: map[string]any{
			"always_on_top": true,
			"opacity":       0.75,
		},
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SetViewPropertyRequest
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SurfaceID != want.SurfaceID {
		t.Fatalf("surface id = %q, want %q", got.SurfaceID, want.SurfaceID)
	}
	if got.Properties["always_on_top"] != true {
		t.Fatalf("always_on_top = %#v, want true", got.Properties["always_on_top"])
	}
	if got.Properties["opacity"] != 0.75 {
		t.Fatalf("opacity = %#v, want 0.75", got.Properties["opacity"])
	}
}

func TestCompositorSetViewPropertyMessageRoundTrip(t *testing.T) {
	t.Parallel()

	want := CompositorSetViewProperty{
		Type:      PluginMessageSetViewProperty,
		SurfaceID: "view-7",
		Properties: map[string]any{
			"always_on_top": false,
		},
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got CompositorSetViewProperty
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != PluginMessageSetViewProperty || got.SurfaceID != want.SurfaceID || got.Properties["always_on_top"] != false {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}
