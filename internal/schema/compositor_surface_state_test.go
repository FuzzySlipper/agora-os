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

func TestMaximizeSurfaceRequestRoundTrip(t *testing.T) {
	t.Parallel()

	want := MaximizeSurfaceRequest{SurfaceID: "view-42", Enabled: true, WaitTimeoutMs: 1500}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got MaximizeSurfaceRequest
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, want)
	}
}

func TestCompositorSetSurfaceMaximizeRoundTrip(t *testing.T) {
	t.Parallel()

	maximized := true
	want := CompositorSetSurfaceState{Type: PluginMessageSetSurfaceState, RequestID: "state-1", SurfaceID: "view-7", Maximized: &maximized}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CompositorSetSurfaceState
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != PluginMessageSetSurfaceState || got.RequestID != want.RequestID || got.SurfaceID != want.SurfaceID || got.Maximized == nil || !*got.Maximized {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestCompositorSurfaceMaximizeRoundTrip(t *testing.T) {
	t.Parallel()

	maximized := true
	want := CompositorSurface{ID: "view-7", Role: "toplevel", Maximized: &maximized, TiledEdges: &SurfaceTiledEdges{Bits: 15, Edges: []string{"top", "bottom", "left", "right"}}}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CompositorSurface
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != want.ID || got.Maximized == nil || !*got.Maximized || got.TiledEdges == nil || got.TiledEdges.Bits != 15 || len(got.TiledEdges.Edges) != 4 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestMinimizeSurfaceRequestRoundTrip(t *testing.T) {
	t.Parallel()

	want := MinimizeSurfaceRequest{SurfaceID: "view-42", Enabled: true, WaitTimeoutMs: 1500}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got MinimizeSurfaceRequest
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, want)
	}
}

func TestCompositorSurfaceMinimizeRoundTrip(t *testing.T) {
	t.Parallel()

	minimized := true
	restorable := true
	want := CompositorSurface{ID: "view-7", Role: "toplevel", Minimized: &minimized, Restorable: &restorable, VisibilityState: "minimized"}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CompositorSurface
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != want.ID || got.Minimized == nil || !*got.Minimized || got.Restorable == nil || !*got.Restorable || got.VisibilityState != "minimized" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestCompositorSetSurfaceMinimizeRoundTrip(t *testing.T) {
	t.Parallel()

	minimized := true
	want := CompositorSetSurfaceState{Type: PluginMessageSetSurfaceState, RequestID: "state-1", SurfaceID: "view-7", Minimized: &minimized}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CompositorSetSurfaceState
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != PluginMessageSetSurfaceState || got.RequestID != want.RequestID || got.SurfaceID != want.SurfaceID || got.Minimized == nil || !*got.Minimized {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}
