package schema

import (
	"encoding/json"
	"testing"
)

func TestFullscreenSurfaceRequestRoundTrip(t *testing.T) {
	t.Parallel()

	want := FullscreenSurfaceRequest{SurfaceID: "view-42", Enabled: true, WaitTimeoutMs: 1500}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got FullscreenSurfaceRequest
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, want)
	}
}

func TestCompositorSetSurfaceStateRoundTrip(t *testing.T) {
	t.Parallel()

	fullscreen := true
	want := CompositorSetSurfaceState{Type: PluginMessageSetSurfaceState, RequestID: "state-1", SurfaceID: "view-7", Fullscreen: &fullscreen}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CompositorSetSurfaceState
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != PluginMessageSetSurfaceState || got.RequestID != want.RequestID || got.SurfaceID != want.SurfaceID || got.Fullscreen == nil || !*got.Fullscreen {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestCompositorSurfaceFullscreenRoundTrip(t *testing.T) {
	t.Parallel()

	fullscreen := true
	want := CompositorSurface{ID: "view-7", Role: "toplevel", Fullscreen: &fullscreen}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CompositorSurface
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != want.ID || got.Fullscreen == nil || !*got.Fullscreen {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}
