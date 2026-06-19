package webview

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

func TestConfigJSONIncludesRole(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(Config{URL: "https://example.com", Role: "panel"})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if !strings.Contains(string(encoded), `"role":"panel"`) {
		t.Fatalf("encoded config missing role tag: %s", encoded)
	}
}

func TestNormalizeConfigDefaultsRole(t *testing.T) {
	t.Parallel()

	cfg, err := normalizeConfig(Config{URL: "https://example.com"})
	if err != nil {
		t.Fatalf("normalizeConfig returned error: %v", err)
	}
	if cfg.Role != defaultRole {
		t.Fatalf("got role %q, want %q", cfg.Role, defaultRole)
	}
}

func TestNormalizeConfigRejectsUnknownRole(t *testing.T) {
	t.Parallel()

	_, err := normalizeConfig(Config{URL: "https://example.com", Role: "tooltip"})
	if err == nil {
		t.Fatal("expected unsupported role error")
	}
}

func TestHelperArgsPassesRole(t *testing.T) {
	t.Parallel()

	args := helperArgs("/tmp/helper.py", resolvedConfig{
		TargetURI: "https://example.com",
		AppID:     "io.agoraos.Test",
		Width:     640,
		Height:    480,
		Title:     "Test",
		Role:      "panel",
	})
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--role" && args[i+1] == "panel" {
			return
		}
	}
	t.Fatalf("helper args missing --role panel: %#v", args)
}

func TestHelperPythonDefaultAndOverride(t *testing.T) {
	t.Setenv("AGORA_WEBVIEW_PYTHON", "")
	if got := helperPython(); got != defaultPython {
		t.Fatalf("got helper python %q, want %q", got, defaultPython)
	}
	t.Setenv("AGORA_WEBVIEW_PYTHON", "/tmp/python")
	if got := helperPython(); got != "/tmp/python" {
		t.Fatalf("got helper python %q, want override", got)
	}
}

func TestStartHelperErrorIncludesRole(t *testing.T) {
	t.Setenv("AGORA_WEBVIEW_PYTHON", filepath.Join(t.TempDir(), "missing-python"))

	_, _, _, err := startHelper(context.Background(), "/tmp/helper.py", resolvedConfig{
		TargetURI: "https://example.com",
		AppID:     "io.agoraos.Test",
		Width:     640,
		Height:    480,
		Title:     "Test",
		Role:      "panel",
	})
	if err == nil {
		t.Fatal("expected helper start error")
	}
	if !strings.Contains(err.Error(), `role "panel"`) {
		t.Fatalf("error missing role context: %v", err)
	}
}

func TestLifecycleRoleUsesHelperEffectiveRole(t *testing.T) {
	t.Parallel()

	got := lifecycleRole(resolvedConfig{Role: "panel"}, helperEvent{Role: "toplevel"})
	if got != "toplevel" {
		t.Fatalf("got role %q, want toplevel", got)
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
		{name: "created", helper: helperEventCreated, wantTopic: schema.TopicCompositorAdvisorySurfaceCreated, wantEvent: schema.SurfaceEventMapped, wantOK: true},
		{name: "focused", helper: helperEventFocused, wantTopic: schema.TopicCompositorAdvisorySurfaceFocused, wantEvent: schema.SurfaceEventFocused, wantOK: true},
		{name: "closed", helper: helperEventClosed, wantTopic: schema.TopicCompositorAdvisorySurfaceDestroyed, wantEvent: schema.SurfaceEventUnmapped, wantOK: true},
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

	env := helperEnv([]string{"HOME=/tmp"}, resolvedConfig{AppCommandPort: 41234})
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
	if _, ok := joined["AGORA_APP_COMMAND_PORT=41234"]; !ok {
		t.Fatal("expected AGORA_APP_COMMAND_PORT in helper env")
	}
}

func TestHelperReadinessScriptCollectsVoxelMarkers(t *testing.T) {
	t.Parallel()

	for _, needle := range []string{
		"window.ashaVoxelInteraction?.scenarioId",
		"bodyEditApplied",
		"bodyPostInputRenderChanged",
		"selectionHash",
		"renderBeforeHash",
		"renderAfterHash",
		"meshBeforeHash",
		"meshAfterHash",
		"proofSummaryText",
	} {
		if !strings.Contains(helperScript, needle) {
			t.Fatalf("readiness script missing %q", needle)
		}
	}
}
