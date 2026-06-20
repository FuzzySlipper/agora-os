package shellui

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/patch/agora-os/internal/schema"
	"github.com/patch/agora-os/internal/webbus"
)

func TestStateReturnsAgentsSurfacesAndPendingEscalations(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000100, 0).UTC()
	authNow := time.Now().UTC().Truncate(time.Second)
	isoSock := startSchemaServer(t, func(req schema.Request) schema.Response {
		if req.Method != schema.MethodListAgents {
			t.Fatalf("unexpected method %q", req.Method)
		}
		return okSchemaResponse(schema.ListAgentsResponse{
			Agents: []schema.AgentInfo{{
				Name:      "writer",
				UID:       60001,
				Status:    schema.StatusRunning,
				Slice:     "agent-60001.slice",
				CreatedAt: now,
			}},
		})
	})
	compSock := startSchemaServer(t, func(req schema.Request) schema.Response {
		if req.Method != schema.MethodListSurfaces {
			t.Fatalf("unexpected method %q", req.Method)
		}
		return okSchemaResponse(schema.ListSurfacesResponse{
			Surfaces: []schema.CompositorTrackedSurface{{
				Surface:   schema.CompositorSurface{ID: "surface-1", Title: "Writer"},
				Client:    schema.CompositorClientIdentity{PID: 123, UID: 60001, GID: 60001},
				LastEvent: schema.SurfaceEventMapped,
				UpdatedAt: now,
			}},
		})
	})
	adminLog := filepath.Join(t.TempDir(), "admin-agent.log")
	writeAdminLog(t, adminLog, loggedEscalation{
		Timestamp: now,
		Request: schema.EscalationRequest{
			AgentUID:          60001,
			TaskContext:       "scene-write",
			RequestedAction:   "write",
			RequestedResource: "/etc/hosts",
			Justification:     "need temporary access",
		},
		Response: schema.EscalationResponse{
			Decision:  schema.DecisionEscalate,
			Reasoning: "needs human review",
		},
	})

	secret := []byte("01234567890123456789012345678901")
	server := New(Config{
		Secret:           secret,
		Now:              func() time.Time { return authNow },
		IsolationSocket:  isoSock,
		CompositorSocket: compSock,
		AdminLogPath:     adminLog,
		DecisionLogPath:  filepath.Join(t.TempDir(), "decisions.jsonl"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/shell/state", nil)
	req.Header.Set("Authorization", "Bearer "+mustMintHumanToken(t, secret))
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.Code)
	}
	var state State
	if err := json.Unmarshal(resp.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Agents) != 1 || state.Agents[0].UID != 60001 {
		t.Fatalf("got agents %+v", state.Agents)
	}
	if len(state.Surfaces) != 1 || state.Surfaces[0].Surface.ID != "surface-1" {
		t.Fatalf("got surfaces %+v", state.Surfaces)
	}
	if len(state.PendingEscalations) != 1 || state.PendingEscalations[0].Request.AgentUID != 60001 {
		t.Fatalf("got pending escalations %+v", state.PendingEscalations)
	}
}

func TestSurfaceFocusEndpointCallsCanonicalCompositorAction(t *testing.T) {
	t.Parallel()

	authNow := time.Now().UTC().Truncate(time.Second)
	compSock := startSchemaServer(t, func(req schema.Request) schema.Response {
		if req.Method != schema.MethodFocusSurface {
			t.Fatalf("unexpected method %q", req.Method)
		}
		var focusReq schema.FocusSurfaceRequest
		if err := json.Unmarshal(req.Body, &focusReq); err != nil {
			t.Fatal(err)
		}
		if focusReq.SurfaceID != "view-42" {
			t.Fatalf("surface_id = %q, want view-42", focusReq.SurfaceID)
		}
		return okSchemaResponse(schema.SurfaceActionResponse{
			Action: "surface.focus", SurfaceID: "view-42", Decision: schema.SurfaceActionAccepted,
			FocusedSurfaceID: "view-42",
		})
	})
	secret := []byte("01234567890123456789012345678901")
	server := New(Config{Secret: secret, Now: func() time.Time { return authNow }, CompositorSocket: compSock})
	body := bytes.NewReader([]byte(`{"surface_id":"view-42"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/shell/surface/focus", body)
	req.Header.Set("Authorization", "Bearer "+mustMintHumanToken(t, secret))
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("got status %d body %q, want 200", resp.Code, resp.Body.String())
	}
	var result schema.SurfaceActionResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "surface.focus" || result.FocusedSurfaceID != "view-42" || result.Decision != schema.SurfaceActionAccepted {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestSurfaceFocusEndpointReturnsStructuredDeniedResult(t *testing.T) {
	t.Parallel()

	authNow := time.Now().UTC().Truncate(time.Second)
	compSock := startSchemaServer(t, func(req schema.Request) schema.Response {
		if req.Method != schema.MethodFocusSurface {
			t.Fatalf("unexpected method %q", req.Method)
		}
		payload, _ := json.Marshal("surface view-stale is unmapped/stale")
		return schema.Response{OK: false, Body: payload, ErrorClass: schema.ErrorSurfaceStale, ErrorMessage: "surface view-stale is unmapped/stale"}
	})
	secret := []byte("01234567890123456789012345678901")
	server := New(Config{Secret: secret, Now: func() time.Time { return authNow }, CompositorSocket: compSock})
	body := bytes.NewReader([]byte(`{"surface_id":"view-stale"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/shell/surface/focus", body)
	req.Header.Set("Authorization", "Bearer "+mustMintHumanToken(t, secret))
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("got status %d body %q, want 409", resp.Code, resp.Body.String())
	}
	var result focusSurfaceHTTPError
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ErrorClass != schema.ErrorSurfaceStale || result.Result.Decision != schema.SurfaceActionDenied || result.Result.SurfaceID != "view-stale" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestEscalationDecisionAppendsDecisionLog(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000200, 0).UTC()
	authNow := time.Now().UTC().Truncate(time.Second)
	adminLog := filepath.Join(t.TempDir(), "admin-agent.log")
	decisionLog := filepath.Join(t.TempDir(), "admin-human-decisions.jsonl")
	writeAdminLog(t, adminLog, loggedEscalation{
		Timestamp: now,
		Request: schema.EscalationRequest{
			AgentUID:          60002,
			TaskContext:       "review",
			RequestedAction:   "write",
			RequestedResource: "/etc/shadow",
			Justification:     "test",
		},
		Response: schema.EscalationResponse{
			Decision:  schema.DecisionEscalate,
			Reasoning: "needs human review",
		},
	})

	secret := []byte("01234567890123456789012345678901")
	server := New(Config{
		Secret:          secret,
		Now:             func() time.Time { return authNow },
		AdminLogPath:    adminLog,
		DecisionLogPath: decisionLog,
		BusSocket:       filepath.Join(t.TempDir(), "missing-bus.sock"),
	})

	pending, _, err := server.pendingEscalationsWithIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("got pending %+v", pending)
	}

	body, err := json.Marshal(escalationDecisionRequest{
		ID:          pending[0].ID,
		Decision:    schema.DecisionApprove,
		Constraints: []string{"pointer", "keyboard"},
		Notes:       "allowed for this surface only",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/shell/escalations/decide", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mustMintHumanToken(t, secret))
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.Code)
	}
	raw, err := os.ReadFile(decisionLog)
	if err != nil {
		t.Fatal(err)
	}
	var decision schema.HumanEscalationDecision
	if err := json.Unmarshal(bytes.TrimSpace(raw), &decision); err != nil {
		t.Fatal(err)
	}
	if decision.ID != pending[0].ID || decision.Decision != schema.DecisionApprove {
		t.Fatalf("got decision %+v", decision)
	}
}

func TestStaticHandlerServesShellDistAliasAndDesktop(t *testing.T) {
	t.Parallel()

	server := New(Config{
		Assets: fstest.MapFS{
			"index.html":         {Data: []byte("operator console")},
			"desktop/index.html": {Data: []byte("desktop shell")},
		},
	})
	handler := server.StaticHandler()

	tests := []struct {
		path string
		want string
	}{
		{path: "/", want: "operator console"},
		{path: "/dist/", want: "operator console"},
		{path: "/dist/desktop/", want: "desktop shell"},
		{path: "dist/desktop/", want: "desktop shell"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			target := tt.path
			if target[0] != '/' {
				target = "/"
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.URL.Path = tt.path
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != http.StatusOK {
				t.Fatalf("got status %d, want 200", resp.Code)
			}
			if body := resp.Body.String(); body != tt.want {
				t.Fatalf("got body %q, want %q", body, tt.want)
			}
		})
	}
}

func TestStaticHandlerDevDirServesFilesystemAndPicksUpChanges(t *testing.T) {
	t.Parallel()

	devDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(devDir, "index.html"), []byte("dev console v1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(devDir, "desktop"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devDir, "desktop", "index.html"), []byte("dev desktop v1"), 0644); err != nil {
		t.Fatal(err)
	}
	server := New(Config{
		Assets: fstest.MapFS{
			"index.html": {Data: []byte("embedded console")},
		},
		DevDir: devDir,
	})
	handler := server.StaticHandler()

	assertStaticBody(t, handler, "/", "dev console v1")
	assertStaticBody(t, handler, "/dist/desktop/", "dev desktop v1")
	assertStaticBody(t, handler, "dist/desktop/", "dev desktop v1")

	if err := os.WriteFile(filepath.Join(devDir, "desktop", "index.html"), []byte("dev desktop v2"), 0644); err != nil {
		t.Fatal(err)
	}
	assertStaticBody(t, handler, "/dist/desktop/", "dev desktop v2")
}

func TestStaticHandlerWithoutDevDirUsesEmbeddedAssets(t *testing.T) {
	t.Parallel()

	devDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(devDir, "index.html"), []byte("dev console"), 0644); err != nil {
		t.Fatal(err)
	}
	server := New(Config{
		Assets: fstest.MapFS{
			"index.html": {Data: []byte("embedded console")},
		},
	})
	handler := server.StaticHandler()

	assertStaticBody(t, handler, "/", "embedded console")
}

func assertStaticBody(t *testing.T, handler http.Handler, requestPath string, want string) {
	t.Helper()
	target := requestPath
	if target[0] != '/' {
		target = "/"
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.URL.Path = requestPath
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("%s: got status %d, want 200; body %q", requestPath, resp.Code, resp.Body.String())
	}
	if body := resp.Body.String(); body != want {
		t.Fatalf("%s: got body %q, want %q", requestPath, body, want)
	}
}

func TestThemeCSSFiltersUnsafeSelectorsAndProperties(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	raw := []byte(`
:root { color: #fff; display: none; --taskbar-height: 1px; }
.shell-taskbar { background: #222; position: absolute; z-index: 999; border-color: #fff; }
.shell-clock, .shell-background { opacity: 0.9; grid-template-areas: "x"; font-size: 12px; }
body { color: red; }
.shell-agent-health { filter: blur(1px); float: left; }
`)
	if err := os.WriteFile(filepath.Join(configDir, "theme.css"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	server := New(Config{ShellConfigDir: configDir})
	req := httptest.NewRequest(http.MethodGet, "/api/shell/theme.css", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.Code)
	}
	got := resp.Body.String()
	for _, want := range []string{
		":root {",
		"color: #fff;",
		".shell-taskbar {",
		"background: #222;",
		"border-color: #fff;",
		".shell-clock, .shell-background {",
		"opacity: 0.9;",
		"font-size: 12px;",
		".shell-agent-health {",
		"filter: blur(1px);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("filtered css missing %q in:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"display", "--taskbar-height", "position", "z-index", "grid-template", "body", "float"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("filtered css still contains %q in:\n%s", forbidden, got)
		}
	}
	warnings := resp.Header().Get("X-Agora-CSS-Warnings")
	for _, want := range []string{"stripped property display", "stripped property position", "stripped selector body", "stripped property grid-template-areas", "stripped property z-index"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("warnings missing %q in %q", want, warnings)
		}
	}
}

func TestThemeCSSStripsUnsafeAllowedPropertyValues(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	raw := []byte(`
.shell-background { background: url("https://example.invalid/pixel"); color: #fff; }
.shell-agent-health { filter: url("https://example.invalid/filter.svg#x"); opacity: 0.8; }
.shell-taskbar { border-image-source: url("https://example.invalid/border.svg"); border-color: #fff; }
.shell-clock { background: javascript:alert(1); font-size: 12px; }
.shell-notification-center { background: \\75\\72\\6c("https://example.invalid/escaped"); padding: 1rem; }
`)
	if err := os.WriteFile(filepath.Join(configDir, "theme.css"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	server := New(Config{ShellConfigDir: configDir})
	req := httptest.NewRequest(http.MethodGet, "/api/shell/theme.css", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.Code)
	}
	got := resp.Body.String()
	for _, forbidden := range []string{"url(", "https://example.invalid", "javascript:", "\\75"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("filtered css still contains unsafe value fragment %q in:\n%s", forbidden, got)
		}
	}
	for _, want := range []string{"color: #fff;", "opacity: 0.8;", "border-color: #fff;", "font-size: 12px;", "padding: 1rem;"} {
		if !strings.Contains(got, want) {
			t.Fatalf("filtered css missing safe declaration %q in:\n%s", want, got)
		}
	}
	warnings := resp.Header().Get("X-Agora-CSS-Warnings")
	for _, want := range []string{"stripped unsafe value for background", "stripped unsafe value for filter", "stripped unsafe value for border-image-source"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("warnings missing %q in %q", want, warnings)
		}
	}
}

func TestThemeCSSMissingReturnsNotFound(t *testing.T) {
	t.Parallel()

	server := New(Config{ShellConfigDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/api/shell/theme.css", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404", resp.Code)
	}
}

func TestLayoutJSONServesShellConfigFile(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	layout := []byte("{\"widgets\":{\"clock\":{\"visible\":true,\"position\":\"top-right\",\"order\":1}},\"theme\":{\"properties\":{\"--taskbar-bg\":\"#222\"}}}")
	if err := os.WriteFile(filepath.Join(configDir, "layout.json"), layout, 0644); err != nil {
		t.Fatal(err)
	}
	server := New(Config{ShellConfigDir: configDir})
	req := httptest.NewRequest(http.MethodGet, "/api/shell/layout.json", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.Code)
	}
	if got := resp.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("got content-type %q, want application/json", got)
	}
	if got := bytes.TrimSpace(resp.Body.Bytes()); !bytes.Equal(got, layout) {
		t.Fatalf("got body %s, want %s", got, layout)
	}
}

func TestLayoutJSONMissingReturnsNotFound(t *testing.T) {
	t.Parallel()

	server := New(Config{ShellConfigDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/api/shell/layout.json", nil)
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404", resp.Code)
	}
}

func TestWidgetProxyServesWidgetFilesAndManifest(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	widgetDir := filepath.Join(configDir, "widgets", "weather")
	if err := os.MkdirAll(widgetDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(widgetDir, "index.html"), []byte("<h1>Weather</h1>"), 0644); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"name":"weather","title":"Weather","position":"top-right","bus_topics":["weather.current"]}`)
	if err := os.WriteFile(filepath.Join(widgetDir, "manifest.json"), manifest, 0644); err != nil {
		t.Fatal(err)
	}
	server := New(Config{ShellConfigDir: configDir})

	for _, tt := range []struct {
		path        string
		contentType string
		want        string
	}{
		{path: "/api/shell/widget-proxy/weather/index.html", contentType: "text/html", want: "<h1>Weather</h1>"},
		{path: "/api/shell/widget-proxy/weather/manifest.json", contentType: "application/json", want: string(manifest)},
	} {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			resp := httptest.NewRecorder()
			server.ServeHTTP(resp, req)
			if resp.Code != http.StatusOK {
				t.Fatalf("got status %d, want 200", resp.Code)
			}
			if got := resp.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
				t.Fatalf("got X-Frame-Options %q, want SAMEORIGIN", got)
			}
			if got := resp.Header().Get("Content-Security-Policy"); got != "sandbox allow-scripts" {
				t.Fatalf("got Content-Security-Policy %q, want sandbox allow-scripts", got)
			}
			if got := resp.Header().Get("Content-Type"); !strings.HasPrefix(got, tt.contentType) {
				t.Fatalf("got content-type %q, want prefix %q", got, tt.contentType)
			}
			if got := strings.TrimSpace(resp.Body.String()); got != tt.want {
				t.Fatalf("got body %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWidgetProxyRejectsTraversalAndInvalidNames(t *testing.T) {
	t.Parallel()

	server := New(Config{ShellConfigDir: t.TempDir()})
	for _, path := range []string{
		"/api/shell/widget-proxy/../layout.json",
		"/api/shell/widget-proxy/bad.name/index.html",
		"/api/shell/widget-proxy/weather/../layout.json",
		"/api/shell/widget-proxy/weather/.secret",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)
		if resp.Code != http.StatusNotFound {
			t.Fatalf("%s: got status %d, want 404", path, resp.Code)
		}
	}
}

func TestSessionTokenEndpointMintsLoopbackHumanToken(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	secret := []byte("01234567890123456789012345678901")
	server := New(Config{Secret: secret, Now: func() time.Time { return now }})
	req := httptest.NewRequest(http.MethodGet, "/api/shell/session-token", nil)
	req.RemoteAddr = "127.0.0.1:45555"
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("got status %d and body %q, want 200", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("got Cache-Control %q, want no-store", got)
	}
	var body sessionTokenResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Role != string(webbus.RoleHuman) || body.Use != "websocket-subprotocol" {
		t.Fatalf("got response %+v", body)
	}
	claims, err := webbus.VerifyToken(secret, body.Token, now)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if claims.Role != webbus.RoleHuman || claims.UID != 0 {
		t.Fatalf("got claims %+v, want human uid 0", claims)
	}
	if body.ExpiresAt != now.Add(shellSessionTokenTTL).Unix() || claims.Exp != body.ExpiresAt {
		t.Fatalf("got expires_at=%d claims.exp=%d", body.ExpiresAt, claims.Exp)
	}
}

func TestSessionTokenEndpointRejectsNonLoopback(t *testing.T) {
	t.Parallel()

	server := New(Config{Secret: []byte("01234567890123456789012345678901")})
	req := httptest.NewRequest(http.MethodGet, "/api/shell/session-token", nil)
	req.RemoteAddr = "203.0.113.10:45555"
	resp := httptest.NewRecorder()

	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want 403", resp.Code)
	}
}

func TestWidgetProxyRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	configDir := t.TempDir()
	widgetDir := filepath.Join(configDir, "widgets", "weather")
	if err := os.MkdirAll(widgetDir, 0755); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secretPath, []byte("outside-secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretPath, filepath.Join(widgetDir, "linked.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	server := New(Config{ShellConfigDir: configDir})
	req := httptest.NewRequest(http.MethodGet, "/api/shell/widget-proxy/weather/linked.txt", nil)
	resp := httptest.NewRecorder()
	server.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("got status %d and body %q, want 404", resp.Code, resp.Body.String())
	}
}

func TestDefaultShellConfigDirIsSharedEtcPath(t *testing.T) {
	t.Parallel()

	if got := defaultShellConfigDir(); got != DefaultShellConfigDir {
		t.Fatalf("got default shell config dir %q, want %q", got, DefaultShellConfigDir)
	}
	if DefaultShellConfigDir != "/etc/agora-shell" {
		t.Fatalf("unexpected shared shell config dir %q", DefaultShellConfigDir)
	}
}

func startSchemaServer(t *testing.T, handler func(req schema.Request) schema.Response) string {
	t.Helper()

	sock := filepath.Join(t.TempDir(), "service.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var req schema.Request
				if err := json.NewDecoder(c).Decode(&req); err != nil {
					return
				}
				_ = json.NewEncoder(c).Encode(handler(req))
			}(conn)
		}
	}()

	return sock
}

func okSchemaResponse(body any) schema.Response {
	payload, _ := json.Marshal(body)
	return schema.Response{OK: true, Body: payload}
}

func writeAdminLog(t *testing.T, path string, entry loggedEscalation) {
	t.Helper()

	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
}

func mustMintHumanToken(t *testing.T, secret []byte) string {
	t.Helper()

	token, err := webbus.MintToken(secret, webbus.Claims{
		Role: webbus.RoleHuman,
		UID:  0,
		Exp:  time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestLoadAdminEscalationsSkipsDecisionEntries(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	adminLog := filepath.Join(t.TempDir(), "admin.log")

	// Write a model escalation entry.
	writeAdminLog(t, adminLog, loggedEscalation{
		Timestamp: now,
		Request: schema.EscalationRequest{
			AgentUID:          60003,
			TaskContext:       "test",
			RequestedAction:   "read",
			RequestedResource: "/etc/passwd",
			Justification:     "test justification",
		},
		Response: schema.EscalationResponse{
			Decision:  schema.DecisionEscalate,
			Reasoning: "needs human review",
		},
	})

	// Append a human decision entry in the wrapped format the admin agent uses.
	decisionEntry := struct {
		Timestamp time.Time                      `json:"timestamp"`
		Decision  schema.HumanEscalationDecision `json:"decision"`
	}{
		Timestamp: now,
		Decision: schema.HumanEscalationDecision{
			ID:         "some-id",
			Timestamp:  now,
			ReviewedBy: 0,
			Decision:   schema.DecisionApprove,
			Request:    schema.EscalationRequest{AgentUID: 60003, TaskContext: "test", RequestedAction: "read", RequestedResource: "/etc/passwd", Justification: "test justification"},
			Response:   schema.EscalationResponse{Decision: schema.DecisionEscalate, Reasoning: "needs human review"},
		},
	}
	payload, _ := json.Marshal(decisionEntry)
	payload = append(payload, '\n')
	f, err := os.OpenFile(adminLog, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(payload); err != nil {
		t.Fatal(err)
	}
	f.Close()

	events, err := loadAdminEscalations(adminLog)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 escalation event, got %d", len(events))
	}
	if events[0].Request.AgentUID != 60003 {
		t.Errorf("expected agent_uid 60003, got %d", events[0].Request.AgentUID)
	}
}
