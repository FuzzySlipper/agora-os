package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
)

func TestBuildLaunchRequestDefaultsRole(t *testing.T) {
	t.Parallel()

	req, err := buildLaunchRequest([]string{"--cmd", "webview-launcher --url http://example.test"})
	if err != nil {
		t.Fatalf("buildLaunchRequest returned error: %v", err)
	}
	if req.Role != "toplevel" {
		t.Fatalf("got role %q, want toplevel", req.Role)
	}
}

func TestBuildLaunchRequestAcceptsRole(t *testing.T) {
	t.Parallel()

	req, err := buildLaunchRequest([]string{"--cmd", "webview-launcher --url http://example.test", "--role", "panel"})
	if err != nil {
		t.Fatalf("buildLaunchRequest returned error: %v", err)
	}
	if req.Role != "panel" {
		t.Fatalf("got role %q, want panel", req.Role)
	}
}

func TestBuildLaunchRequestAcceptsURL(t *testing.T) {
	t.Parallel()

	req, err := buildLaunchRequest([]string{"--url", "http://127.0.0.1:7780/shell/dist/desktop/", "--role", "panel"})
	if err != nil {
		t.Fatalf("buildLaunchRequest returned error: %v", err)
	}
	if req.Role != "panel" {
		t.Fatalf("got role %q, want panel", req.Role)
	}
	if req.Command != "webview-launcher --url 'http://127.0.0.1:7780/shell/dist/desktop/'" {
		t.Fatalf("command = %q", req.Command)
	}
}

func TestBuildLaunchRequestRejectsCmdAndURL(t *testing.T) {
	t.Parallel()

	_, err := buildLaunchRequest([]string{"--cmd", "webview-launcher --url http://example.test", "--url", "http://example.test"})
	if err == nil {
		t.Fatal("expected mutual exclusion error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildLaunchRequestRejectsInvalidRole(t *testing.T) {
	t.Parallel()

	_, err := buildLaunchRequest([]string{"--cmd", "webview-launcher --url http://example.test", "--role", "invalid"})
	if err == nil {
		t.Fatal("expected invalid role error")
	}
	if !strings.Contains(err.Error(), "valid values: toplevel, panel, dock, background, overlay") {
		t.Fatalf("error does not list valid values: %v", err)
	}
}

func TestBuildSetViewPropertyRequestParsesAlwaysOnTop(t *testing.T) {
	t.Parallel()

	req, err := buildSetViewPropertyRequest([]string{"--surface", "view-42", "--always-on-top", "true"})
	if err != nil {
		t.Fatalf("buildSetViewPropertyRequest returned error: %v", err)
	}
	if req.SurfaceID != "view-42" {
		t.Fatalf("surface = %q, want view-42", req.SurfaceID)
	}
	if req.Properties["always_on_top"] != true {
		t.Fatalf("always_on_top = %#v, want true", req.Properties["always_on_top"])
	}
}

func TestBuildSetViewPropertyRequestRequiresProperty(t *testing.T) {
	t.Parallel()

	_, err := buildSetViewPropertyRequest([]string{"--surface", "view-42"})
	if err == nil {
		t.Fatal("expected missing property error")
	}
	if !strings.Contains(err.Error(), "at least one property flag is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDefaultShellConfigDirUsesSharedDirWhenPresent(t *testing.T) {
	sharedDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))

	got := defaultShellConfigDirWithShared(sharedDir)
	if got != sharedDir {
		t.Fatalf("got default shell config dir %q, want shared dir %q", got, sharedDir)
	}
}

func TestDefaultShellConfigDirFallsBackToXDGWhenSharedMissing(t *testing.T) {
	missingSharedDir := filepath.Join(t.TempDir(), "missing-shared")
	xdg := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))

	got := defaultShellConfigDirWithShared(missingSharedDir)
	want := filepath.Join(xdg, "agora-shell")
	if got != want {
		t.Fatalf("got default shell config dir %q, want %q", got, want)
	}
}

func TestDefaultShellConfigDirFallsBackToHomeWhenSharedAndXDGMissing(t *testing.T) {
	missingSharedDir := filepath.Join(t.TempDir(), "missing-shared")
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)

	got := defaultShellConfigDirWithShared(missingSharedDir)
	want := filepath.Join(home, ".config", "agora-shell")
	if got != want {
		t.Fatalf("got default shell config dir %q, want %q", got, want)
	}
}

func TestBuildShellSetThemePayloadValidatesAndCopiesCSS(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	cssPath := filepath.Join(t.TempDir(), "theme.css")
	if err := os.WriteFile(cssPath, []byte(".shell-clock { color: #fff; }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	payload, err := buildShellSetThemePayload("{\"--taskbar-bg\":\"#000\"}", cssPath, configDir)
	if err != nil {
		t.Fatalf("buildShellSetThemePayload returned error: %v", err)
	}
	if payload.CSSURL != defaultThemeCSSURL {
		t.Fatalf("css_url = %q, want %q", payload.CSSURL, defaultThemeCSSURL)
	}
	if got := payload.Properties["--taskbar-bg"]; got != "#000" {
		t.Fatalf("property = %#v, want #000", got)
	}
	copied, err := os.ReadFile(filepath.Join(configDir, "theme.css"))
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != ".shell-clock { color: #fff; }\n" {
		t.Fatalf("copied css = %q", copied)
	}
}

func TestBuildShellSetThemePayloadRejectsInvalidPropertiesJSON(t *testing.T) {
	t.Parallel()

	_, err := buildShellSetThemePayload(`not-json`, "", t.TempDir())
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
	if !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildShellSetThemePayloadRejectsInvalidPropertyContract(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"{\"bad property\":\"#000\"}",
		"{\"-bad\":\"#000\"}",
		"{\"_bad\":\"#000\"}",
		"{\"-\":\"#000\"}",
		"{\"--1bad\":\"#000\"}",
		"{\"--empty\":\"\"}",
		"{\"--object\":{\"nested\":true}}",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := buildShellSetThemePayload(raw, "", t.TempDir())
			if err == nil {
				t.Fatal("expected property validation error")
			}
		})
	}
}

func TestBuildShellAddWidgetPayloadCopiesDirectoryAndManifest(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "index.html"), []byte("<h1>Weather</h1>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".secret"), []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	payload, err := buildShellAddWidgetPayload("weather_1", src, configDir)
	if err != nil {
		t.Fatalf("buildShellAddWidgetPayload returned error: %v", err)
	}
	if payload.Name != "weather_1" || payload.URL != src {
		t.Fatalf("payload = %+v", payload)
	}
	widgetDir := filepath.Join(configDir, "widgets", "weather_1")
	if raw, err := os.ReadFile(filepath.Join(widgetDir, "index.html")); err != nil || string(raw) != "<h1>Weather</h1>" {
		t.Fatalf("copied index = %q, %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(widgetDir, ".secret")); !os.IsNotExist(err) {
		t.Fatalf("dotfile should not be copied, stat err=%v", err)
	}
	manifestRaw, err := os.ReadFile(filepath.Join(widgetDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["name"] != "weather_1" {
		t.Fatalf("manifest name = %#v", manifest["name"])
	}
}

func TestBuildShellAddWidgetPayloadNormalizesManifestName(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "index.html"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "manifest.json"), []byte("{\"name\":\"wrong\",\"title\":\"Custom\",\"bus_topics\":[\"widget.custom.current\"]}"), 0644); err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	if _, err := buildShellAddWidgetPayload("right", src, configDir); err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile(filepath.Join(configDir, "widgets", "right", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["name"] != "right" || manifest["title"] != "Custom" {
		t.Fatalf("manifest was not normalized/preserved as expected: %#v", manifest)
	}
}

func TestBuildShellAddWidgetPayloadPreservesExistingOnInvalidReplacement(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	widgetDir := filepath.Join(configDir, "widgets", "weather")
	if err := os.MkdirAll(widgetDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(widgetDir, "index.html"), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing-widget")
	if _, err := buildShellAddWidgetPayload("weather", missing, configDir); err == nil {
		t.Fatal("expected missing source error")
	}
	raw, err := os.ReadFile(filepath.Join(widgetDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "existing" {
		t.Fatalf("existing widget was modified: %q", raw)
	}
}

func TestBuildShellAddWidgetPayloadRejectsInvalidName(t *testing.T) {
	t.Parallel()

	_, err := buildShellAddWidgetPayload("bad.name", t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("expected invalid name error")
	}
}

func TestShellWidgetListAndRemove(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	widgetDir := filepath.Join(configDir, "widgets", "weather")
	if err := os.MkdirAll(widgetDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(widgetDir, "index.html"), []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(widgetDir, "manifest.json"), []byte("{\"bus_topics\":[\"widget.weather.current\"]}"), 0644); err != nil {
		t.Fatal(err)
	}
	widgets, err := listShellWidgets(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(widgets) != 1 || widgets[0].Name != "weather" || !widgets[0].HasIndex || !widgets[0].HasManifest || widgets[0].BusTopics[0] != "widget.weather.current" {
		t.Fatalf("widgets = %+v", widgets)
	}
	if _, err := buildShellRemoveWidgetPayload("weather", configDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(widgetDir); !os.IsNotExist(err) {
		t.Fatalf("widget dir should be removed, stat err=%v", err)
	}
}

func TestShellStateTracksThemeAndWidgets(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	if err := mergeShellThemeState(configDir, shellThemePayload{Properties: map[string]any{"--taskbar-bg": "#111"}, CSSURL: defaultThemeCSSURL}); err != nil {
		t.Fatal(err)
	}
	if err := mergeShellThemeState(configDir, shellThemePayload{WallpaperURL: "/tmp/wallpaper.png"}); err != nil {
		t.Fatal(err)
	}
	state, err := readShellState(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Theme.Properties["--taskbar-bg"] != "#111" || state.Theme.CSSURL != defaultThemeCSSURL || state.WallpaperURL != "/tmp/wallpaper.png" {
		t.Fatalf("state theme = %+v", state.Theme)
	}
	if err := clearShellThemeState(configDir); err != nil {
		t.Fatal(err)
	}
	state, err = readShellState(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Theme.Properties != nil || state.Theme.CSSURL != "" || state.Theme.WallpaperURL != "" {
		t.Fatalf("theme should be reset, got %+v", state.Theme)
	}
}

func TestPublishShellEventPublishesToEventBus(t *testing.T) {
	t.Parallel()

	sock := startTestBus(t)
	subscriber, err := bus.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer subscriber.Close()
	if err := subscriber.Subscribe("shell.apply_theme"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := publishShellEvent(sock, schema.TopicShellApplyTheme, shellThemePayload{Properties: map[string]any{"--taskbar-bg": "#000"}}); err != nil {
		t.Fatalf("publishShellEvent: %v", err)
	}
	done := make(chan bus.Event, 1)
	go func() {
		event, _ := subscriber.Receive()
		done <- event
	}()
	select {
	case event := <-done:
		if event.Topic != schema.TopicShellApplyTheme {
			t.Fatalf("topic = %q", event.Topic)
		}
		var body shellThemePayload
		if err := json.Unmarshal(event.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Properties["--taskbar-bg"] != "#000" {
			t.Fatalf("body = %+v", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for shell.apply_theme")
	}
}

func startTestBus(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "bus.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	broker := bus.NewBrokerWithOptions(false)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = bus.ServeConn(conn, broker) }()
		}
	}()
	return sock
}
