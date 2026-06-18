package shellui

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
