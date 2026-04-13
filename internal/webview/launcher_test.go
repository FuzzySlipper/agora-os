package webview

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/patch/agora-os/internal/schema"
)

func TestResolveTargetRejectsInvalidCombinations(t *testing.T) {
	t.Parallel()

	if _, err := resolveTarget("", ""); err == nil {
		t.Fatal("expected error when neither --url nor --path is provided")
	}
	if _, err := resolveTarget("https://example.com", "./index.html"); err == nil {
		t.Fatal("expected error when both --url and --path are provided")
	}
}

func TestResolveTargetURL(t *testing.T) {
	t.Parallel()

	got, err := resolveTarget("https://example.com/app?x=1", "")
	if err != nil {
		t.Fatalf("resolveTarget returned error: %v", err)
	}
	if got != "https://example.com/app?x=1" {
		t.Fatalf("got %q, want %q", got, "https://example.com/app?x=1")
	}
}

func TestResolveTargetPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "index.html")
	if err := os.WriteFile(path, []byte("<html></html>"), 0600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	got, err := resolveTarget("", path)
	if err != nil {
		t.Fatalf("resolveTarget returned error: %v", err)
	}
	want := (&url.URL{Scheme: "file", Path: path}).String()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLifecycleMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		helper    string
		wantTopic string
		wantEvent schema.CompositorSurfaceEventName
		wantOK    bool
	}{
		{name: "created", helper: helperEventCreated, wantTopic: schema.TopicCompositorSurfaceCreated, wantEvent: schema.SurfaceEventMapped, wantOK: true},
		{name: "focused", helper: helperEventFocused, wantTopic: schema.TopicCompositorSurfaceFocused, wantEvent: schema.SurfaceEventFocused, wantOK: true},
		{name: "closed", helper: helperEventClosed, wantTopic: schema.TopicCompositorSurfaceDestroyed, wantEvent: schema.SurfaceEventUnmapped, wantOK: true},
		{name: "unknown", helper: "bogus", wantOK: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotTopic, gotEvent, gotOK := lifecycleMapping(tc.helper)
			if gotOK != tc.wantOK {
				t.Fatalf("got ok=%v, want %v", gotOK, tc.wantOK)
			}
			if gotTopic != tc.wantTopic {
				t.Fatalf("got topic %q, want %q", gotTopic, tc.wantTopic)
			}
			if gotEvent != tc.wantEvent {
				t.Fatalf("got event %q, want %q", gotEvent, tc.wantEvent)
			}
		})
	}
}

func TestHelperEnvAddsWaylandDefaults(t *testing.T) {
	t.Parallel()

	env := helperEnv([]string{"HOME=/tmp"})
	joined := make(map[string]struct{}, len(env))
	for _, item := range env {
		joined[item] = struct{}{}
	}
	if _, ok := joined["GDK_BACKEND=wayland"]; !ok {
		t.Fatal("expected GDK_BACKEND=wayland in helper env")
	}
	if _, ok := joined["PYTHONUNBUFFERED=1"]; !ok {
		t.Fatal("expected PYTHONUNBUFFERED=1 in helper env")
	}
}
