package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
	"github.com/patch/agora-os/internal/shelldefaults"
)

func TestBuildCatalogAppLaunchRequest(t *testing.T) {
	t.Parallel()
	catalogFile := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(catalogFile, []byte(`{"version":1,"entries":[{"id":"terminal","label":"Terminal","enabled":true,"command":"foot --title Agora","role":"toplevel","expected_app_id":"foot","wait_surface":true,"wait_timeout_ms":2500},{"id":"browser","label":"Browser","enabled":false,"reason":"not installed"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	req, err := buildCatalogAppLaunchRequest([]string{"--catalog-id", "terminal", "--catalog-file", catalogFile, "--audit-correlation-id", "turn:test"})
	if err != nil {
		t.Fatalf("buildCatalogAppLaunchRequest returned error: %v", err)
	}
	if req.entry.ID != "terminal" || req.launch.Command != "foot --title Agora" || req.launch.ExpectedAppID != "foot" || !req.launch.WaitSurface || req.launch.WaitTimeoutMs != 2500 || req.launch.AuditCorrelationID != "turn:test" {
		t.Fatalf("unexpected request %+v", req)
	}
	_, err = buildCatalogAppLaunchRequest([]string{"--catalog-id", "browser", "--catalog-file", catalogFile})
	if err == nil || !strings.Contains(err.Error(), "app_disabled") {
		t.Fatalf("expected app_disabled error, got %v", err)
	}
}

func TestBuildDesktopImportRequest(t *testing.T) {
	t.Parallel()

	req, err := buildDesktopImportRequest([]string{"--dir", "/tmp/a", "--dir", "/tmp/b", "--include-hidden", "--include-nodisplay", "--format", "catalog-overlay", "--output", "/tmp/out.json"})
	if err != nil {
		t.Fatalf("buildDesktopImportRequest returned error: %v", err)
	}
	if !reflect.DeepEqual(req.opts.Dirs, []string{"/tmp/a", "/tmp/b"}) || !req.opts.IncludeHidden || !req.opts.IncludeNoDisplay || req.format != "catalog-overlay" || req.output != "/tmp/out.json" {
		t.Fatalf("unexpected request: %+v", req)
	}
	_, err = buildDesktopImportRequest([]string{"--format", "bad"})
	if err == nil || !strings.Contains(err.Error(), "--format must be") {
		t.Fatalf("expected format error, got %v", err)
	}
}

func TestRunDesktopImportWritesDeterministicJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zeta.desktop"), []byte("[Desktop Entry]\nType=Application\nName=Zeta\nExec=/usr/bin/zeta %u\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha.desktop"), []byte("[Desktop Entry]\nType=Application\nName=Alpha\nExec=/usr/bin/alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "candidates.json")
	req, err := buildDesktopImportRequest([]string{"--dir", dir, "--output", out})
	if err != nil {
		t.Fatal(err)
	}
	if err := runDesktopImport(req, false); err != nil {
		t.Fatalf("runDesktopImport: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Index(text, "desktop-alpha") < 0 || strings.Index(text, "desktop-zeta") < 0 || strings.Index(text, "desktop-alpha") > strings.Index(text, "desktop-zeta") {
		t.Fatalf("output not deterministic or missing candidates:\n%s", text)
	}
	if !strings.Contains(text, "\"enabled\": false") || !strings.Contains(text, "requires_argument_policy") {
		t.Fatalf("output missing disabled entry or risk flag:\n%s", text)
	}
}

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

func TestBuildFocusSurfaceRequestParsesSurface(t *testing.T) {
	t.Parallel()

	req, err := buildFocusSurfaceRequest([]string{"--surface", "view-42", "--timeout-ms", "1500"})
	if err != nil {
		t.Fatalf("buildFocusSurfaceRequest returned error: %v", err)
	}
	if req.SurfaceID != "view-42" || req.WaitTimeoutMs != 1500 {
		t.Fatalf("request = %+v, want surface view-42 timeout 1500", req)
	}
}

func TestBuildFocusSurfaceRequestRequiresSurface(t *testing.T) {
	t.Parallel()

	_, err := buildFocusSurfaceRequest([]string{})
	if err == nil {
		t.Fatal("expected missing surface error")
	}
	if !strings.Contains(err.Error(), "--surface is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildRaiseSurfaceRequest(t *testing.T) {
	t.Parallel()

	req, err := buildRaiseSurfaceRequest([]string{"--surface", "view-42", "--timeout-ms", "1500"})
	if err != nil {
		t.Fatalf("buildRaiseSurfaceRequest returned error: %v", err)
	}
	if req.SurfaceID != "view-42" || req.Mode != "no-focus" || req.WaitTimeoutMs != 1500 {
		t.Fatalf("request = %+v, want surface view-42 mode no-focus timeout 1500", req)
	}
}

func TestBuildRaiseSurfaceRequestRejectsUnsupportedMode(t *testing.T) {
	t.Parallel()

	_, err := buildRaiseSurfaceRequest([]string{"--surface", "view-42", "--mode", "focus"})
	if err == nil {
		t.Fatal("expected unsupported mode error")
	}
	if !strings.Contains(err.Error(), "--mode must be no-focus") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildAlwaysOnTopRequest(t *testing.T) {
	t.Parallel()

	req, err := buildAlwaysOnTopRequest([]string{"--surface", "view-42", "--state", "false", "--timeout-ms", "1500"})
	if err != nil {
		t.Fatalf("buildAlwaysOnTopRequest returned error: %v", err)
	}
	if req.SurfaceID != "view-42" || req.Enabled || req.WaitTimeoutMs != 1500 {
		t.Fatalf("request = %+v, want surface view-42 enabled false timeout 1500", req)
	}
}

func TestBuildFullscreenSurfaceRequest(t *testing.T) {
	t.Parallel()

	req, err := buildFullscreenSurfaceRequest([]string{"--surface", "view-42", "--state", "false", "--timeout-ms", "1500"})
	if err != nil {
		t.Fatalf("buildFullscreenSurfaceRequest returned error: %v", err)
	}
	if req.SurfaceID != "view-42" || req.Enabled || req.WaitTimeoutMs != 1500 {
		t.Fatalf("request = %+v, want surface view-42 enabled false timeout 1500", req)
	}
}

func TestBuildFullscreenSurfaceRequestRejectsInvalidState(t *testing.T) {
	t.Parallel()

	_, err := buildFullscreenSurfaceRequest([]string{"--surface", "view-42", "--state", "maybe"})
	if err == nil {
		t.Fatal("expected invalid state error")
	}
	if !strings.Contains(err.Error(), "--state must be true or false") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildMaximizeSurfaceRequest(t *testing.T) {
	t.Parallel()

	req, err := buildMaximizeSurfaceRequest([]string{"--surface", "view-42", "--state", "false", "--timeout-ms", "1500"})
	if err != nil {
		t.Fatalf("buildMaximizeSurfaceRequest returned error: %v", err)
	}
	if req.SurfaceID != "view-42" || req.Enabled || req.WaitTimeoutMs != 1500 {
		t.Fatalf("request = %+v, want surface view-42 enabled false timeout 1500", req)
	}
}

func TestBuildMaximizeSurfaceRequestRejectsInvalidState(t *testing.T) {
	t.Parallel()

	_, err := buildMaximizeSurfaceRequest([]string{"--surface", "view-42", "--state", "maybe"})
	if err == nil {
		t.Fatal("expected invalid state error")
	}
	if !strings.Contains(err.Error(), "--state must be true or false") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildMinimizeSurfaceRequest(t *testing.T) {
	t.Parallel()

	req, err := buildMinimizeSurfaceRequest([]string{"--surface", "view-42", "--state", "false", "--timeout-ms", "1500"})
	if err != nil {
		t.Fatalf("buildMinimizeSurfaceRequest returned error: %v", err)
	}
	if req.SurfaceID != "view-42" || req.Enabled || req.WaitTimeoutMs != 1500 {
		t.Fatalf("request = %+v, want surface view-42 enabled false timeout 1500", req)
	}
}

func TestBuildMinimizeSurfaceRequestRejectsInvalidState(t *testing.T) {
	t.Parallel()

	_, err := buildMinimizeSurfaceRequest([]string{"--surface", "view-42", "--state", "maybe"})
	if err == nil {
		t.Fatal("expected invalid state error")
	}
	if !strings.Contains(err.Error(), "--state must be true or false") {
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

func TestInstallShellDefaultsCreatesLayoutAndHelloWorldWidget(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	result, err := installShellDefaults(configDir)
	if err != nil {
		t.Fatalf("installShellDefaults returned error: %v", err)
	}
	if result.ConfigDir != configDir || !result.LayoutInstalled || result.LayoutPreserved || !result.ThemeInstalled || result.ThemeID != shelldefaults.DefaultThemeID {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.WidgetsInstalled) != 1 || result.WidgetsInstalled[0] != shelldefaults.HelloWorldWidgetName {
		t.Fatalf("widgets installed = %#v", result.WidgetsInstalled)
	}
	assertFileContent(t, filepath.Join(configDir, "layout.json"), shelldefaults.LayoutJSON)
	assertFileContent(t, filepath.Join(configDir, "themes", shelldefaults.DefaultThemeID, "theme.json"), shelldefaults.DefaultThemeManifestJSON)
	assertJSONFileField(t, filepath.Join(configDir, "theme-selection.json"), "selected_theme_id", shelldefaults.DefaultThemeID)
	assertFileContent(t, filepath.Join(configDir, "widgets", "hello-world", "index.html"), shelldefaults.HelloWorldIndexHTML)
	assertFileContent(t, filepath.Join(configDir, "widgets", "hello-world", "manifest.json"), shelldefaults.HelloWorldManifestJSON)

	widgets, err := listShellWidgets(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(widgets) != 1 || widgets[0].Name != "hello-world" || !widgets[0].HasIndex || !widgets[0].HasManifest {
		t.Fatalf("widgets = %+v", widgets)
	}
}

func TestInstallShellDefaultsPreservesExistingLayout(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	layoutPath := filepath.Join(configDir, "layout.json")
	customLayout := "{\"widgets\":{\"hello-world\":{\"visible\":false}}}\n"
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layoutPath, []byte(customLayout), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := installShellDefaults(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.LayoutPreserved || result.LayoutInstalled {
		t.Fatalf("unexpected result: %+v", result)
	}
	assertFileContent(t, layoutPath, customLayout)
	assertFileContent(t, filepath.Join(configDir, "widgets", "hello-world", "manifest.json"), shelldefaults.HelloWorldManifestJSON)
}

func TestInstallShellExampleWidgetsDoesNotCreateLayout(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	if _, err := installShellExampleWidgets(configDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "layout.json")); !os.IsNotExist(err) {
		t.Fatalf("layout.json should not be created by install-example-widgets, stat err=%v", err)
	}
	assertFileContent(t, filepath.Join(configDir, "widgets", "hello-world", "index.html"), shelldefaults.HelloWorldIndexHTML)
}

func TestPackagedHelloWorldFilesMatchExampleSources(t *testing.T) {
	t.Parallel()

	assertFileContent(t, filepath.Join("..", "..", "shell", "example-widgets", "layout.json"), shelldefaults.LayoutJSON)
	assertFileContent(t, filepath.Join("..", "..", "shell", "example-widgets", "hello-world", "index.html"), shelldefaults.HelloWorldIndexHTML)
	assertFileContent(t, filepath.Join("..", "..", "shell", "example-widgets", "hello-world", "manifest.json"), shelldefaults.HelloWorldManifestJSON)
	assertFileContent(t, filepath.Join("..", "..", "shell", "desktop", "themes", shelldefaults.DefaultThemeID, "theme.json"), shelldefaults.DefaultThemeManifestJSON)
}

func assertJSONFileField(t *testing.T, path string, key string, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got[key] != want {
		t.Fatalf("%s[%s] = %v, want %q", path, key, got[key], want)
	}
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != want {
		t.Fatalf("%s content = %q, want %q", path, raw, want)
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
