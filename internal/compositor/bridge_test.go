package compositor

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

type publishedEvent struct {
	topic string
	body  any
}

type fakePublisher struct {
	events []publishedEvent
}

func boolPtr(v bool) *bool {
	return &v
}

func (f *fakePublisher) Publish(topic string, body any) error {
	f.events = append(f.events, publishedEvent{topic: topic, body: body})
	return nil
}

func TestHandlePluginConnPublishesMappedEventAndOwnerPolicy(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	server, client, cleanup := unixSocketPair(t)
	defer cleanup()

	done := make(chan struct{})
	go func() {
		defer close(done)
		bridge.HandlePluginConn(server)
	}()

	dec := json.NewDecoder(client)
	readInitialSync(t, dec)

	event := schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-42", WayfireViewID: 42, AppID: "org.example.App", Title: "Example", Role: "toplevel"},
		Client:  schema.CompositorClientIdentity{PID: 1234, UID: 60001, GID: 60001},
	}
	if err := json.NewEncoder(client).Encode(event); err != nil {
		t.Fatalf("encode plugin event: %v", err)
	}

	var policyMsg schema.CompositorPolicyUpsert
	if err := dec.Decode(&policyMsg); err != nil {
		t.Fatalf("decode policy upsert: %v", err)
	}
	if policyMsg.Surface.SurfaceID != "view-42" || policyMsg.Surface.OwnerUID != 60001 {
		t.Fatalf("got policy %+v, want owner policy for view-42", policyMsg.Surface)
	}
	if len(policyMsg.Surface.AllowPointerUIDs) != 0 || len(policyMsg.Surface.AllowKeyboardUIDs) != 0 {
		t.Fatalf("got grant lists %+v, want owner-only policy", policyMsg.Surface)
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for len(pub.events) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(pub.events) != 1 {
		t.Fatalf("got %d published events, want 1", len(pub.events))
	}
	if pub.events[0].topic != schema.TopicCompositorSurfaceCreated {
		t.Fatalf("got topic %q, want %q", pub.events[0].topic, schema.TopicCompositorSurfaceCreated)
	}

	body, ok := pub.events[0].body.(schema.CompositorBusEvent)
	if !ok {
		t.Fatalf("got body type %T, want schema.CompositorBusEvent", pub.events[0].body)
	}
	if body.Client.UID != 60001 {
		t.Fatalf("got owner uid %d, want 60001", body.Client.UID)
	}

	if access := bridge.CheckSurfaceAccess("view-42", 60001, schema.AccessReadPixels); !access.Allowed {
		t.Fatalf("owner read_pixels access denied: %+v", access)
	}
	if access := bridge.CheckSurfaceAccess("view-42", 60002, schema.AccessReadPixels); access.Allowed {
		t.Fatalf("non-owner read_pixels access allowed without grant: %+v", access)
	}

	_ = client.Close()
	<-done
}

func TestGrantViewportRecordsAndUpdatesPolicy(t *testing.T) {
	grantLog := filepath.Join(t.TempDir(), "grants.jsonl")
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid()), GrantLogPath: grantLog})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	server, client, cleanup := unixSocketPair(t)
	defer cleanup()
	go bridge.HandlePluginConn(server)

	dec := json.NewDecoder(client)
	readInitialSync(t, dec)

	surface := schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-root", WayfireViewID: 7, Title: "Human shell", Role: "toplevel"},
		Client:  schema.CompositorClientIdentity{PID: 77, UID: 0, GID: 0},
	}
	if err := json.NewEncoder(client).Encode(surface); err != nil {
		t.Fatalf("encode mapped event: %v", err)
	}

	var ownerPolicy schema.CompositorPolicyUpsert
	if err := dec.Decode(&ownerPolicy); err != nil {
		t.Fatalf("decode owner policy: %v", err)
	}
	if ownerPolicy.Surface.OwnerUID != 0 {
		t.Fatalf("got owner uid %d, want 0", ownerPolicy.Surface.OwnerUID)
	}
	if len(ownerPolicy.Surface.AllowPointerUIDs) != 0 || len(ownerPolicy.Surface.AllowKeyboardUIDs) != 0 {
		t.Fatalf("expected owner-only policy before grant, got %+v", ownerPolicy.Surface)
	}
	if access := bridge.CheckSurfaceAccess("view-root", 60001, schema.AccessPointer); access.Allowed {
		t.Fatalf("expected pointer deny before grant, got %+v", access)
	}
	if access := bridge.CheckSurfaceAccess("view-root", 60001, schema.AccessReadPixels); access.Allowed {
		t.Fatalf("expected read_pixels deny before grant, got %+v", access)
	}

	grant, err := bridge.GrantViewport(0, schema.ViewportGrantRequest{
		SurfaceID: "view-root",
		AgentUID:  60001,
	})
	if err != nil {
		t.Fatalf("GrantViewport: %v", err)
	}
	if !grantAllows(grant, schema.AccessReadPixels) {
		t.Fatalf("grant %+v missing read_pixels", grant)
	}

	var grantPolicy schema.CompositorPolicyUpsert
	if err := dec.Decode(&grantPolicy); err != nil {
		t.Fatalf("decode grant policy: %v", err)
	}
	if grantPolicy.Surface.OwnerUID != 0 {
		t.Fatalf("got owner uid %d, want 0", grantPolicy.Surface.OwnerUID)
	}
	if !containsUID(grantPolicy.Surface.AllowPointerUIDs, 60001) || !containsUID(grantPolicy.Surface.AllowKeyboardUIDs, 60001) {
		t.Fatalf("grant policy %+v missing agent uid 60001", grantPolicy.Surface)
	}

	access := bridge.CheckSurfaceAccess("view-root", 60001, schema.AccessReadPixels)
	if !access.Allowed {
		t.Fatalf("expected read_pixels grant, got %+v", access)
	}

	data, err := os.ReadFile(grantLog)
	if err != nil {
		t.Fatalf("ReadFile(grantLog): %v", err)
	}
	if !strings.Contains(string(data), "grant") || !strings.Contains(string(data), "view-root") {
		t.Fatalf("grant log %q missing expected record", string(data))
	}
	info, err := os.Stat(grantLog)
	if err != nil {
		t.Fatalf("Stat(grantLog): %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("grant log perms = %o, want 600", got)
	}
}

func TestCheckSurfaceAccessDeniesReadPixelsWithoutGrant(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-root", WayfireViewID: 9},
		Client:  schema.CompositorClientIdentity{UID: 0},
	})

	access := bridge.CheckSurfaceAccess("view-root", 60001, schema.AccessReadPixels)
	if access.Allowed {
		t.Fatalf("expected read_pixels deny, got %+v", access)
	}
	if !strings.Contains(access.Reason, "no viewport grant") {
		t.Fatalf("got deny reason %q, want viewport-grant denial", access.Reason)
	}
}

func TestRevokeViewportRemovesPolicyAndAccess(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-root", WayfireViewID: 11},
		Client:  schema.CompositorClientIdentity{UID: 0},
	})
	if _, err := bridge.GrantViewport(0, schema.ViewportGrantRequest{SurfaceID: "view-root", AgentUID: 60002}); err != nil {
		t.Fatalf("GrantViewport: %v", err)
	}
	if err := bridge.RevokeViewport(0, schema.RevokeViewportGrantRequest{SurfaceID: "view-root", AgentUID: 60002}); err != nil {
		t.Fatalf("RevokeViewport: %v", err)
	}

	access := bridge.CheckSurfaceAccess("view-root", 60002, schema.AccessKeyboard)
	if access.Allowed {
		t.Fatalf("expected keyboard deny after revoke, got %+v", access)
	}

	bridge.mu.RLock()
	policy := bridge.policies["view-root"]
	bridge.mu.RUnlock()
	if containsUID(policy.AllowPointerUIDs, 60002) || containsUID(policy.AllowKeyboardUIDs, 60002) {
		t.Fatalf("policy %+v still contains revoked uid 60002", policy)
	}
}

func TestHandleSurfaceEventKeepsGrantUpdateOrderedWithPluginSync(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	session := &pluginSession{conn: server, enc: json.NewEncoder(server)}
	bridge.installPluginSession(session)
	defer bridge.clearPluginSession(session)

	eventDone := make(chan struct{})
	go func() {
		bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
			Type:    schema.PluginMessageSurfaceEvent,
			Event:   schema.SurfaceEventMapped,
			Surface: schema.CompositorSurface{ID: "view-race", WayfireViewID: 21},
			Client:  schema.CompositorClientIdentity{UID: 0},
		})
		close(eventDone)
	}()

	// net.Pipe() keeps the mapped-event send blocked until the test starts reading,
	// so a short sleep is enough to let handleSurfaceEvent reach plugin.Send().
	time.Sleep(20 * time.Millisecond)

	grantDone := make(chan error, 1)
	go func() {
		_, err := bridge.GrantViewport(0, schema.ViewportGrantRequest{SurfaceID: "view-race", AgentUID: 60002})
		grantDone <- err
	}()

	select {
	case err := <-grantDone:
		t.Fatalf("GrantViewport completed before blocked mapped sync was released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	dec := json.NewDecoder(client)
	var ownerPolicy schema.CompositorPolicyUpsert
	if err := dec.Decode(&ownerPolicy); err != nil {
		t.Fatalf("decode owner policy: %v", err)
	}
	if ownerPolicy.Surface.SurfaceID != "view-race" || ownerPolicy.Surface.OwnerUID != 0 {
		t.Fatalf("got owner policy %+v, want view-race owner 0", ownerPolicy.Surface)
	}
	if len(ownerPolicy.Surface.AllowPointerUIDs) != 0 || len(ownerPolicy.Surface.AllowKeyboardUIDs) != 0 {
		t.Fatalf("expected owner-only policy before grant, got %+v", ownerPolicy.Surface)
	}

	select {
	case <-eventDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for mapped event sync to finish")
	}

	var grantPolicy schema.CompositorPolicyUpsert
	if err := dec.Decode(&grantPolicy); err != nil {
		t.Fatalf("decode grant policy: %v", err)
	}
	if !containsUID(grantPolicy.Surface.AllowPointerUIDs, 60002) || !containsUID(grantPolicy.Surface.AllowKeyboardUIDs, 60002) {
		t.Fatalf("grant policy %+v missing agent uid 60002", grantPolicy.Surface)
	}

	select {
	case err := <-grantDone:
		if err != nil {
			t.Fatalf("GrantViewport: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for GrantViewport")
	}

	if access := bridge.CheckSurfaceAccess("view-race", 60002, schema.AccessPointer); !access.Allowed {
		t.Fatalf("expected pointer grant after ordered sync, got %+v", access)
	}
}

func TestHandlePluginConnRejectsUnauthorizedPeer(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot exercise unauthorized non-root plugin peer while running as root")
	}

	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid() + 1)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	server, client, cleanup := unixSocketPair(t)
	defer cleanup()

	done := make(chan struct{})
	go func() {
		defer close(done)
		bridge.HandlePluginConn(server)
	}()

	_ = client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var msg any
	err = json.NewDecoder(client).Decode(&msg)
	if err == nil {
		t.Fatal("expected unauthorized plugin peer to receive no sync data")
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
		var netErr net.Error
		if !errors.As(err, &netErr) {
			t.Fatalf("expected socket close/timeout, got %v", err)
		}
	}

	<-done
}

func TestLaunchAppAllowsDesktopShellCorrelationSessionWithoutToken(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	launch, err := bridge.LaunchApp(schema.LaunchAppRequest{SessionID: "desktop-shell:test-session", Command: "sleep 30"})
	if err != nil {
		t.Fatalf("LaunchApp with desktop-shell correlation session: %v", err)
	}
	if launch.SessionID != "desktop-shell:test-session" || launch.LaunchID == "" {
		t.Fatalf("unexpected launch: %+v", launch)
	}
	if _, err := bridge.GetSession("desktop-shell:test-session"); err == nil {
		t.Fatal("desktop-shell correlation id should not create a compositor session")
	}
	processes := bridge.ListProcesses("desktop-shell:test-session")
	if len(processes) != 1 || processes[0].LaunchID != launch.LaunchID {
		t.Fatalf("launch not correlated in process list: %+v", processes)
	}
	if _, err := bridge.TerminateLaunch(launch.LaunchID); err != nil {
		t.Fatalf("TerminateLaunch: %v", err)
	}
}

func TestSessionLifecycleAndLaunchTracking(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	session := bridge.CreateSession(schema.CreateSessionRequest{Label: "asha-scenario-42", ProjectID: "agora-os", TaskID: 2543, ASHAScenarioID: "scenario-42"})
	if session.SessionID == "" || session.Label != "asha-scenario-42" || session.ProjectID != "agora-os" || session.TaskID != 2543 {
		t.Fatalf("unexpected session: %+v", session)
	}

	if session.SessionToken == "" {
		t.Fatalf("expected session token")
	}

	launch, err := bridge.LaunchApp(schema.LaunchAppRequest{SessionID: session.SessionID, SessionToken: session.SessionToken, Command: "sleep 30"})
	if err != nil {
		t.Fatalf("LaunchApp: %v", err)
	}
	if launch.LaunchID == "" || launch.PID == 0 || launch.SessionID != session.SessionID {
		t.Fatalf("unexpected launch: %+v", launch)
	}

	processes := bridge.ListProcesses(session.SessionID)
	if len(processes) != 1 || processes[0].LaunchID != launch.LaunchID || processes[0].Status != "running" {
		t.Fatalf("unexpected processes: %+v", processes)
	}

	detail, err := bridge.GetSession(session.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(detail.Processes) != 1 || detail.Processes[0].LaunchID != launch.LaunchID {
		t.Fatalf("session detail missing process: %+v", detail)
	}

	term, err := bridge.TerminateLaunch(launch.LaunchID)
	if err != nil {
		t.Fatalf("TerminateLaunch: %v", err)
	}
	if !term.SignalSent {
		t.Fatalf("expected signal sent, got %+v", term)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		processes = bridge.ListProcesses(session.SessionID)
		if len(processes) == 1 && processes[0].Status == "exited" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(processes) != 1 || processes[0].Status != "exited" {
		t.Fatalf("process did not exit: %+v", processes)
	}

	if err := bridge.DestroySession(session.SessionID); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}
	if _, err := bridge.GetSession(session.SessionID); err == nil {
		t.Fatal("expected destroyed session to be missing")
	}
}

func TestTerminateLaunchEscalatesIgnoredSIGTERM(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	session := bridge.CreateSession(schema.CreateSessionRequest{Label: "term-ignore"})
	launch, err := bridge.LaunchApp(schema.LaunchAppRequest{SessionID: session.SessionID, SessionToken: session.SessionToken, Command: "trap '' TERM; sleep 30"})
	if err != nil {
		t.Fatalf("LaunchApp: %v", err)
	}
	started := time.Now()
	term, err := bridge.TerminateLaunch(launch.LaunchID)
	if err != nil {
		t.Fatalf("TerminateLaunch: %v", err)
	}
	if !term.SignalSent {
		t.Fatalf("expected signal sent, got %+v", term)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("terminate did not escalate promptly; elapsed=%s", elapsed)
	}
	processes := bridge.ListProcesses(session.SessionID)
	if len(processes) != 1 || processes[0].Status != "exited" || processes[0].ExitCode == nil {
		t.Fatalf("process was not tracked as exited after escalation: %+v", processes)
	}
}

func TestTerminateLaunchReportsSurfaceCloseFailures(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().Add(-time.Second)
	bridge.mu.Lock()
	bridge.launches["launch-close-fail"] = &launchRecord{process: schema.CompositorLaunchProcess{LaunchID: "launch-close-fail", PID: 12345, Status: "exited", StartedAt: now}}
	bridge.surfaces["view-close-fail"] = schema.CompositorTrackedSurface{Surface: schema.CompositorSurface{ID: "view-close-fail"}, UpdatedAt: now}
	bridge.surfaceLaunch["view-close-fail"] = "launch-close-fail"
	bridge.mu.Unlock()

	term, err := bridge.TerminateLaunch("launch-close-fail")
	if err == nil {
		t.Fatalf("expected close failure, got response %+v", term)
	}
	if strings.Contains(strings.Join(term.ClosedSurfaces, ","), "view-close-fail") {
		t.Fatalf("surface reported closed despite plugin failure: %+v", term)
	}
	if !strings.Contains(err.Error(), "no plugin connected") {
		t.Fatalf("got error %q, want plugin close failure", err.Error())
	}
}

func TestWaitHooksRequirePresentedFrameEvidence(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().Add(-time.Second)
	bridge.mu.Lock()
	bridge.surfaces["view-wait"] = schema.CompositorTrackedSurface{
		Surface:   schema.CompositorSurface{ID: "view-wait"},
		UpdatedAt: now,
	}
	bridge.launches["launch-wait"] = &launchRecord{process: schema.CompositorLaunchProcess{LaunchID: "launch-wait", PID: 42, Status: "running", StartedAt: now}}
	bridge.surfaceLaunch["view-wait"] = "launch-wait"
	bridge.mu.Unlock()

	if _, err := bridge.WaitForFrame(schema.WaitForFrameRequest{SurfaceID: "view-wait", TimeoutMs: 20}); err == nil {
		t.Fatal("expected WaitForFrame to time out without frame evidence")
	} else if class, _ := classifyError(err); class != schema.ErrorFrameTimeout {
		t.Fatalf("WaitForFrame class = %q, want %q (%v)", class, schema.ErrorFrameTimeout, err)
	}
	if _, err := bridge.WaitForRenderIdle(schema.WaitForRenderIdleRequest{SurfaceID: "view-wait", IdleMs: 1, TimeoutMs: 20}); err == nil {
		t.Fatal("expected WaitForRenderIdle to time out without frame evidence")
	} else if class, _ := classifyError(err); class != schema.ErrorFrameTimeout {
		t.Fatalf("WaitForRenderIdle class = %q, want %q (%v)", class, schema.ErrorFrameTimeout, err)
	}
	if _, err := bridge.WaitForAppReady(schema.WaitForAppReadyRequest{LaunchID: "launch-wait", TimeoutMs: 20}); err == nil {
		t.Fatal("expected WaitForAppReady to time out without frame evidence")
	} else if class, _ := classifyError(err); class != schema.ErrorAppNotReady {
		t.Fatalf("WaitForAppReady class = %q, want %q (%v)", class, schema.ErrorAppNotReady, err)
	}

	bridge.recordFramePresented("view-wait")
	if _, err := bridge.WaitForFrame(schema.WaitForFrameRequest{SurfaceID: "view-wait", TimeoutMs: 20}); err != nil {
		t.Fatalf("WaitForFrame after presented frame: %v", err)
	}
	if _, err := bridge.WaitForAppReady(schema.WaitForAppReadyRequest{LaunchID: "launch-wait", TimeoutMs: 20}); err != nil {
		t.Fatalf("WaitForAppReady after presented frame: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := bridge.WaitForRenderIdle(schema.WaitForRenderIdleRequest{SurfaceID: "view-wait", IdleMs: 1, TimeoutMs: 50}); err != nil {
		t.Fatalf("WaitForRenderIdle after presented frame: %v", err)
	}
}

func TestCaptureOutputFailsClosedWhenNoArtifactsProduced(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := bridge.CreateOutput(schema.CreateOutputRequest{Name: "agent-empty", Width: 640, Height: 480}); err != nil {
		t.Fatalf("CreateOutput: %v", err)
	}
	resp, err := bridge.CaptureOutput(schema.CaptureOutputRequest{Name: "agent-empty"})
	if err == nil {
		t.Fatalf("expected empty output capture to fail, got %+v", resp)
	}
	class, _ := classifyError(err)
	if class != schema.ErrorCaptureDenied {
		t.Fatalf("error class = %q, want %q (%v)", class, schema.ErrorCaptureDenied, err)
	}
	if len(resp.Captures) != 0 || len(resp.Warnings) == 0 {
		t.Fatalf("expected warning-only response, got %+v", resp)
	}
}

func TestInputPluginErrorsAreClassified(t *testing.T) {
	cases := map[string]string{
		"unsupported coordinate_space":         schema.ErrorInvalidCoordinates,
		"pointer event outside surface bounds": schema.ErrorInvalidCoordinates,
		"input injection failed":               schema.ErrorInputDenied,
		"seat not available":                   schema.ErrorInputDenied,
		"surface not found":                    schema.ErrorSurfaceNotFound,
	}
	for msg, want := range cases {
		if got, _ := classifyError(classifyInputPluginError(msg)); got != want {
			t.Fatalf("classifyInputPluginError(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestSurfaceAssociationPrefersPIDAndDoesNotStealAcrossLaunches(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().Add(-time.Second)
	bridge.mu.Lock()
	bridge.sessions["session-a"] = schema.CompositorSession{SessionID: "session-a", CreatedAt: now, LastUsedAt: now}
	bridge.sessions["session-b"] = schema.CompositorSession{SessionID: "session-b", CreatedAt: now, LastUsedAt: now}
	bridge.launches["launch-a"] = &launchRecord{expectedAppID: "same-app", process: schema.CompositorLaunchProcess{LaunchID: "launch-a", SessionID: "session-a", PID: 111, Status: "running", StartedAt: now}}
	bridge.launches["launch-b"] = &launchRecord{expectedAppID: "same-app", process: schema.CompositorLaunchProcess{LaunchID: "launch-b", SessionID: "session-b", PID: 222, Status: "running", StartedAt: now}}
	bridge.mu.Unlock()

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-b", AppID: "same-app"},
		Client:  schema.CompositorClientIdentity{PID: 222, UID: 1001},
	})

	procsA := bridge.ListProcesses("session-a")
	procsB := bridge.ListProcesses("session-b")
	if len(procsA) != 1 || len(procsA[0].Surfaces) != 0 {
		t.Fatalf("session A stole surface: %+v", procsA)
	}
	if len(procsB) != 1 || len(procsB[0].Surfaces) != 1 || procsB[0].Surfaces[0] != "view-b" {
		t.Fatalf("session B missing exact-PID surface: %+v", procsB)
	}
}

func TestAmbiguousHintsDoNotBindOrStealSurfaces(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().Add(-time.Second)
	bridge.mu.Lock()
	bridge.sessions["session-a"] = schema.CompositorSession{SessionID: "session-a", CreatedAt: now, LastUsedAt: now}
	bridge.sessions["session-b"] = schema.CompositorSession{SessionID: "session-b", CreatedAt: now, LastUsedAt: now}
	bridge.launches["launch-a"] = &launchRecord{expectedAppID: "same-app", process: schema.CompositorLaunchProcess{LaunchID: "launch-a", SessionID: "session-a", PID: 111, Status: "running", StartedAt: now}}
	bridge.launches["launch-b"] = &launchRecord{expectedAppID: "same-app", process: schema.CompositorLaunchProcess{LaunchID: "launch-b", SessionID: "session-b", PID: 222, Status: "running", StartedAt: now}}
	bridge.mu.Unlock()

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-ambiguous", AppID: "same-app"},
		Client:  schema.CompositorClientIdentity{PID: 999, UID: 1001},
	})

	if procs := bridge.ListProcesses("session-a"); len(procs) != 1 || len(procs[0].Surfaces) != 0 {
		t.Fatalf("ambiguous hint bound to session A: %+v", procs)
	}
	if procs := bridge.ListProcesses("session-b"); len(procs) != 1 || len(procs[0].Surfaces) != 0 {
		t.Fatalf("ambiguous hint bound to session B: %+v", procs)
	}
}

func TestWaitReconcilesUniqueHintSurface(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().Add(-time.Second)
	presentedAt := now.Add(-launchSurfaceSettleDelay)
	bridge.mu.Lock()
	bridge.sessions["session-webview"] = schema.CompositorSession{SessionID: "session-webview", CreatedAt: now, LastUsedAt: now}
	bridge.launches["launch-webview"] = &launchRecord{expectedTitle: "ASHA Agora Conformance Evidence", process: schema.CompositorLaunchProcess{LaunchID: "launch-webview", SessionID: "session-webview", PID: 111, Status: "running", StartedAt: now}}
	bridge.surfaces["view-webview"] = schema.CompositorTrackedSurface{
		Surface:              schema.CompositorSurface{ID: "view-webview", AppID: "agora-webview-helper-123.py", Title: "ASHA Agora Conformance Evidence"},
		Client:               schema.CompositorClientIdentity{PID: 222, UID: 1001},
		UpdatedAt:            now.Add(100 * time.Millisecond),
		LastPresentTimestamp: &presentedAt,
	}
	bridge.mu.Unlock()

	surface, ok := bridge.waitForLaunchSurface("launch-webview", 50*time.Millisecond)
	if !ok {
		t.Fatal("waitForLaunchSurface did not reconcile unique hint-matched child surface")
	}
	if surface.Surface.ID != "view-webview" {
		t.Fatalf("got surface %s, want view-webview", surface.Surface.ID)
	}
	if procs := bridge.ListProcesses("session-webview"); len(procs) != 1 || len(procs[0].Surfaces) != 1 || procs[0].Surfaces[0] != "view-webview" {
		t.Fatalf("surface was not durably bound to launch: %+v", procs)
	}
}

func TestStaleExitedPIDDoesNotBindReusedPIDSurface(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().Add(-time.Second)
	bridge.mu.Lock()
	bridge.sessions["session-old"] = schema.CompositorSession{SessionID: "session-old", CreatedAt: now, LastUsedAt: now}
	bridge.launches["launch-old"] = &launchRecord{process: schema.CompositorLaunchProcess{LaunchID: "launch-old", SessionID: "session-old", PID: 444, Status: "exited", StartedAt: now}}
	bridge.mu.Unlock()

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-reused-pid", AppID: "other"},
		Client:  schema.CompositorClientIdentity{PID: 444, UID: 1001},
	})

	if procs := bridge.ListProcesses("session-old"); len(procs) != 1 || len(procs[0].Surfaces) != 0 {
		t.Fatalf("stale exited launch captured reused pid surface: %+v", procs)
	}
}

func TestResetSessionClosesSurfacesForExitedLaunches(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server, client, cleanup := unixSocketPair(t)
	defer cleanup()
	go bridge.HandlePluginConn(server)
	dec := json.NewDecoder(client)
	readInitialSync(t, dec)

	now := time.Now().Add(-time.Second)
	bridge.mu.Lock()
	bridge.sessions["session-x"] = schema.CompositorSession{SessionID: "session-x", CreatedAt: now, LastUsedAt: now}
	bridge.launches["launch-x"] = &launchRecord{process: schema.CompositorLaunchProcess{LaunchID: "launch-x", SessionID: "session-x", PID: 333, Status: "exited", StartedAt: now}}
	bridge.surfaces["view-x"] = schema.CompositorTrackedSurface{Surface: schema.CompositorSurface{ID: "view-x"}, Client: schema.CompositorClientIdentity{PID: 333}, UpdatedAt: now}
	bridge.surfaceLaunch["view-x"] = "launch-x"
	bridge.mu.Unlock()

	resetDone := make(chan error, 1)
	go func() { resetDone <- bridge.ResetSession("session-x") }()
	var closeMsg schema.CompositorCloseSurface
	if err := dec.Decode(&closeMsg); err != nil {
		t.Fatalf("decode close surface: %v", err)
	}
	if closeMsg.Type != schema.PluginMessageCloseSurface || closeMsg.SurfaceID != "view-x" {
		t.Fatalf("got close msg %+v, want view-x", closeMsg)
	}
	select {
	case err := <-resetDone:
		if err != nil {
			t.Fatalf("ResetSession: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for reset")
	}
}

func TestDispatchFocusSurfaceRoutesToPluginAndPublishesAction(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server, client, cleanup := unixSocketPair(t)
	defer cleanup()

	go bridge.HandlePluginConn(server)
	dec := json.NewDecoder(client)
	readInitialSync(t, dec)

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-focus", WayfireViewID: 42, Visible: boolPtr(true)},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.FocusSurfaceRequest{SurfaceID: "view-focus", WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodFocusSurface, Body: body})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var msg schema.CompositorFocusSurface
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode focus_surface: %v", err)
	}
	if msg.Type != schema.PluginMessageFocusSurface || msg.SurfaceID != "view-focus" || msg.RequestID == "" {
		t.Fatalf("unexpected focus_surface message: %+v", msg)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorFocusPluginResponse{
		Type: schema.PluginMessageFocusResponse, RequestID: msg.RequestID, SurfaceID: "view-focus", OK: true,
	}); err != nil {
		t.Fatalf("encode focus response: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventFocused,
		Surface: schema.CompositorSurface{ID: "view-focus", WayfireViewID: 42, Visible: boolPtr(true)},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})

	select {
	case err := <-errCh:
		t.Fatalf("dispatch returned error: %v", err)
	case resp := <-respCh:
		if !resp.OK {
			t.Fatalf("dispatch response not OK: %+v", resp)
		}
		var body schema.SurfaceActionResponse
		if err := json.Unmarshal(resp.Body, &body); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if body.Action != "surface.focus" || body.Decision != schema.SurfaceActionAccepted || body.FocusedSurfaceID != "view-focus" {
			t.Fatalf("unexpected action response: %+v", body)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch response")
	}

	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionCompleted {
		t.Fatalf("shell action completion was not published: %+v", pub.events)
	}
}

func TestDispatchMoveSurfaceRoutesToPluginAndPublishesAction(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server, client, cleanup := unixSocketPair(t)
	defer cleanup()

	go bridge.HandlePluginConn(server)
	dec := json.NewDecoder(client)
	readInitialSync(t, dec)

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-move", WayfireViewID: 45, Visible: boolPtr(true), Geometry: &schema.SurfaceGeometry{X: 10, Y: 20, Width: 640, Height: 480}},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.MoveSurfaceRequest{SurfaceID: "view-move", X: 120, Y: 160, WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodMoveSurface, Body: body})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var msg schema.CompositorPlaceSurface
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode place_surface: %v", err)
	}
	if msg.Type != string(schema.PluginMessagePlaceSurface) || msg.SurfaceID != "view-move" || msg.Geometry.X != 120 || msg.Geometry.Y != 160 || msg.Geometry.Width != 640 || msg.Geometry.Height != 480 {
		t.Fatalf("unexpected place_surface message: %+v", msg)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventFocused,
		Surface: schema.CompositorSurface{ID: "view-move", WayfireViewID: 45, Visible: boolPtr(true), Geometry: &schema.SurfaceGeometry{X: 121, Y: 161, Width: 640, Height: 480}},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	if err := json.NewEncoder(client).Encode(schema.CompositorPlacePluginResponse{Type: string(schema.PluginMessagePlaceResponse), RequestID: msg.RequestID, SurfaceID: "view-move", OK: true}); err != nil {
		t.Fatalf("encode place response: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("dispatch returned error: %v", err)
	case resp := <-respCh:
		if !resp.OK {
			t.Fatalf("dispatch response not OK: %+v", resp)
		}
		var action schema.SurfaceActionResponse
		if err := json.Unmarshal(resp.Body, &action); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if action.Action != "surface.move" || action.Decision != schema.SurfaceActionAccepted || action.TargetGeometry == nil || action.TargetGeometry.X != 120 || action.ResultGeometry == nil || action.ResultGeometry.Y != 161 {
			t.Fatalf("unexpected action response: %+v", action)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch response")
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionCompleted {
		t.Fatalf("shell action completion was not published: %+v", pub.events)
	}
}

func TestDispatchMoveSurfaceRejectsLayerShell(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "layer-shell", SurfaceKind: schema.SurfaceKindLayerShell, Visible: boolPtr(true), Geometry: &schema.SurfaceGeometry{X: 0, Y: 0, Width: 100, Height: 100}},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	body, err := json.Marshal(schema.MoveSurfaceRequest{SurfaceID: "layer-shell", X: 10, Y: 20})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodMoveSurface, Body: body}); err == nil {
		t.Fatal("expected layer-shell move denial")
	} else {
		class, _ := classifyError(err)
		if class != schema.ErrorBackendUnsupported {
			t.Fatalf("got error class %q (%v), want %q", class, err, schema.ErrorBackendUnsupported)
		}
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
		t.Fatalf("shell action denial was not published: %+v", pub.events)
	}
	action, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
	if !ok {
		t.Fatalf("denial event body type = %T", pub.events[len(pub.events)-1].body)
	}
	if action.TargetGeometry == nil || action.TargetGeometry.X != 10 || action.TargetGeometry.Y != 20 || action.ResultGeometry == nil || action.ResultGeometry.Width != 100 {
		t.Fatalf("move denial missing target/result geometry: %+v", action)
	}
}

func TestDispatchTileSurfaceRoutesToPluginAndPublishesAction(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server, client, cleanup := unixSocketPair(t)
	defer cleanup()

	go bridge.HandlePluginConn(server)
	dec := json.NewDecoder(client)
	readInitialSync(t, dec)

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-tile", WayfireViewID: 47, Visible: boolPtr(true), Geometry: &schema.SurfaceGeometry{X: 100, Y: 100, Width: 800, Height: 600}},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.TileSurfaceRequest{SurfaceID: "view-tile", Rows: 2, Cols: 2, Row: 0, Col: 0, WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodTileSurface, Body: body})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var msg schema.CompositorPlaceSurface
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode place_surface: %v", err)
	}
	if msg.Type != string(schema.PluginMessagePlaceSurface) || msg.SurfaceID != "view-tile" || msg.Geometry.X != 80 || msg.Geometry.Y != 0 || msg.Geometry.Width != 800 || msg.Geometry.Height != 540 {
		t.Fatalf("unexpected place_surface message: %+v", msg)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventFocused,
		Surface: schema.CompositorSurface{ID: "view-tile", WayfireViewID: 47, Visible: boolPtr(true), Geometry: &schema.SurfaceGeometry{X: 80, Y: 0, Width: 800, Height: 540}},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	if err := json.NewEncoder(client).Encode(schema.CompositorPlacePluginResponse{Type: string(schema.PluginMessagePlaceResponse), RequestID: msg.RequestID, SurfaceID: "view-tile", OK: true}); err != nil {
		t.Fatalf("encode tile response: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("dispatch returned error: %v", err)
	case resp := <-respCh:
		if !resp.OK {
			t.Fatalf("dispatch response not OK: %+v", resp)
		}
		var action schema.SurfaceActionResponse
		if err := json.Unmarshal(resp.Body, &action); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if action.Action != "surface.tile" || action.Decision != schema.SurfaceActionAccepted || action.TargetGeometry == nil || action.TargetGeometry.X != 80 || action.ResultGeometry == nil || action.ResultGeometry.Width != 800 {
			t.Fatalf("unexpected action response: %+v", action)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch response")
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionCompleted {
		t.Fatalf("shell action completion was not published: %+v", pub.events)
	}
}

func TestDispatchTileSurfaceRejectsInvalidRegion(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body, err := json.Marshal(schema.TileSurfaceRequest{SurfaceID: "view-tile", Rows: 3, Cols: 3, Row: 1, Col: 1})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodTileSurface, Body: body}); err == nil {
		t.Fatal("expected invalid tile denial")
	} else {
		class, _ := classifyError(err)
		if class != schema.ErrorBackendUnsupported {
			t.Fatalf("got error class %q (%v), want %q", class, err, schema.ErrorBackendUnsupported)
		}
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
		t.Fatalf("shell action denial was not published: %+v", pub.events)
	}
}

func TestDispatchResizeSurfaceRoutesToPluginAndPublishesAction(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server, client, cleanup := unixSocketPair(t)
	defer cleanup()

	go bridge.HandlePluginConn(server)
	dec := json.NewDecoder(client)
	readInitialSync(t, dec)

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-resize", WayfireViewID: 46, Visible: boolPtr(true), Geometry: &schema.SurfaceGeometry{X: 10, Y: 20, Width: 640, Height: 480}},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.ResizeSurfaceRequest{SurfaceID: "view-resize", Width: 900, Height: 700, WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodResizeSurface, Body: body})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var msg schema.CompositorPlaceSurface
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode place_surface: %v", err)
	}
	if msg.Type != string(schema.PluginMessagePlaceSurface) || msg.SurfaceID != "view-resize" || msg.Geometry.X != 10 || msg.Geometry.Y != 20 || msg.Geometry.Width != 900 || msg.Geometry.Height != 700 {
		t.Fatalf("unexpected place_surface message: %+v", msg)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventFocused,
		Surface: schema.CompositorSurface{ID: "view-resize", WayfireViewID: 46, Visible: boolPtr(true), Geometry: &schema.SurfaceGeometry{X: 10, Y: 20, Width: 901, Height: 701}},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	if err := json.NewEncoder(client).Encode(schema.CompositorPlacePluginResponse{Type: string(schema.PluginMessagePlaceResponse), RequestID: msg.RequestID, SurfaceID: "view-resize", OK: true}); err != nil {
		t.Fatalf("encode place response: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("dispatch returned error: %v", err)
	case resp := <-respCh:
		if !resp.OK {
			t.Fatalf("dispatch response not OK: %+v", resp)
		}
		var action schema.SurfaceActionResponse
		if err := json.Unmarshal(resp.Body, &action); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if action.Action != "surface.resize" || action.Decision != schema.SurfaceActionAccepted || action.TargetGeometry == nil || action.TargetGeometry.Width != 900 || action.ResultGeometry == nil || action.ResultGeometry.Height != 701 {
			t.Fatalf("unexpected action response: %+v", action)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch response")
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionCompleted {
		t.Fatalf("shell action completion was not published: %+v", pub.events)
	}
}

func TestDispatchResizeSurfaceRejectsInvalidSize(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-small", Visible: boolPtr(true), Geometry: &schema.SurfaceGeometry{X: 3, Y: 4, Width: 640, Height: 480}},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	body, err := json.Marshal(schema.ResizeSurfaceRequest{SurfaceID: "view-small", Width: 40, Height: 40})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodResizeSurface, Body: body}); err == nil {
		t.Fatal("expected invalid resize denial")
	} else {
		class, _ := classifyError(err)
		if class != schema.ErrorInvalidCoordinates {
			t.Fatalf("got error class %q (%v), want %q", class, err, schema.ErrorInvalidCoordinates)
		}
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
		t.Fatalf("shell action denial was not published: %+v", pub.events)
	}
	action, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
	if !ok {
		t.Fatalf("denial event body type = %T", pub.events[len(pub.events)-1].body)
	}
	if action.Action != "surface.resize" || action.TargetGeometry == nil || action.TargetGeometry.Width != 40 || action.ResultGeometry == nil || action.ResultGeometry.Width != 640 {
		t.Fatalf("resize denial missing target/result geometry: %+v", action)
	}
}

func TestDispatchResizeSurfaceRejectsLayerShell(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "layer-resize", SurfaceKind: schema.SurfaceKindLayerShell, Visible: boolPtr(true), Geometry: &schema.SurfaceGeometry{X: 0, Y: 0, Width: 400, Height: 300}},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	body, err := json.Marshal(schema.ResizeSurfaceRequest{SurfaceID: "layer-resize", Width: 500, Height: 400})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodResizeSurface, Body: body}); err == nil {
		t.Fatal("expected layer-shell resize denial")
	} else {
		class, _ := classifyError(err)
		if class != schema.ErrorBackendUnsupported {
			t.Fatalf("got error class %q (%v), want %q", class, err, schema.ErrorBackendUnsupported)
		}
	}
}

func TestDispatchCloseSurfaceRoutesToPluginAndPublishesAction(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server, client, cleanup := unixSocketPair(t)
	defer cleanup()

	go bridge.HandlePluginConn(server)
	dec := json.NewDecoder(client)
	readInitialSync(t, dec)

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-close", WayfireViewID: 44, Visible: boolPtr(true)},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.CloseSurfaceRequest{SurfaceID: "view-close"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodCloseSurface, Body: body})
	if err != nil {
		t.Fatalf("dispatch close surface: %v", err)
	}
	if !resp.OK {
		t.Fatalf("dispatch response not OK: %+v", resp)
	}
	var msg schema.CompositorCloseSurface
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode close_surface: %v", err)
	}
	if msg.Type != schema.PluginMessageCloseSurface || msg.SurfaceID != "view-close" {
		t.Fatalf("unexpected close_surface message: %+v", msg)
	}
	var action schema.SurfaceActionResponse
	if err := json.Unmarshal(resp.Body, &action); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if action.Action != "surface.close" || action.Decision != schema.SurfaceActionAccepted || action.ClosedSurfaceID != "view-close" || !action.Queued {
		t.Fatalf("unexpected action response: %+v", action)
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionCompleted {
		t.Fatalf("shell action completion was not published: %+v", pub.events)
	}
}

func TestDispatchFocusSurfaceRejectsStaleSurface(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body, err := json.Marshal(schema.FocusSurfaceRequest{SurfaceID: "missing"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodFocusSurface, Body: body}); err == nil {
		t.Fatal("expected surface not found error")
	} else if !strings.Contains(err.Error(), "surface missing not found") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bridge.bus.(*fakePublisher).events) == 0 || bridge.bus.(*fakePublisher).events[0].topic != schema.TopicShellActionDenied {
		t.Fatalf("shell action denial was not published: %+v", bridge.bus.(*fakePublisher).events)
	}
}

func TestDispatchFocusSurfaceRejectsUnmappedAsStale(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-stale", WayfireViewID: 43, Visible: boolPtr(true)},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventUnmapped,
		Surface: schema.CompositorSurface{ID: "view-stale", WayfireViewID: 43},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})

	body, err := json.Marshal(schema.FocusSurfaceRequest{SurfaceID: "view-stale"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodFocusSurface, Body: body}); err == nil {
		t.Fatal("expected stale surface error")
	} else {
		class, _ := classifyError(err)
		if class != schema.ErrorSurfaceStale {
			t.Fatalf("got error class %q (%v), want %q", class, err, schema.ErrorSurfaceStale)
		}
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
		t.Fatalf("shell action denial was not published: %+v", pub.events)
	}
}

func TestDispatchSetViewPropertyRoutesToPlugin(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server, client, cleanup := unixSocketPair(t)
	defer cleanup()

	go bridge.HandlePluginConn(server)
	dec := json.NewDecoder(client)
	readInitialSync(t, dec)

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-prop", WayfireViewID: 42},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.SetViewPropertyRequest{
		SurfaceID: "view-prop",
		Properties: map[string]any{
			"always_on_top": true,
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(0, schema.Request{Method: schema.MethodSetViewProperty, Body: body})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var msg schema.CompositorSetViewProperty
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_view_property: %v", err)
	}
	if msg.Type != schema.PluginMessageSetViewProperty || msg.SurfaceID != "view-prop" || msg.Properties["always_on_top"] != true {
		t.Fatalf("unexpected set_view_property message: %+v", msg)
	}

	select {
	case err := <-errCh:
		t.Fatalf("dispatch returned error: %v", err)
	case resp := <-respCh:
		if !resp.OK {
			t.Fatalf("dispatch response not OK: %+v", resp)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch response")
	}
}

func TestDispatchSetViewPropertyRejectsInvalidProperties(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body, err := json.Marshal(schema.SetViewPropertyRequest{
		SurfaceID: "view-prop",
		Properties: map[string]any{
			"always_on_top": "yes",
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := bridge.dispatch(0, schema.Request{Method: schema.MethodSetViewProperty, Body: body}); err == nil {
		t.Fatal("expected invalid property type error")
	} else if !strings.Contains(err.Error(), "always_on_top must be a boolean") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDispatchSetViewPropertyRejectsUnsupportedExtraProperties(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body, err := json.Marshal(schema.SetViewPropertyRequest{
		SurfaceID: "view-prop",
		Properties: map[string]any{
			"always_on_top": true,
			"opacity":       0.5,
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := bridge.dispatch(0, schema.Request{Method: schema.MethodSetViewProperty, Body: body}); err == nil {
		t.Fatal("expected unsupported property error")
	} else if !strings.Contains(err.Error(), "unsupported view properties") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPluginLayerShellEventTracksPanelLaunch(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	launchID := "launch-panel"
	bridge.mu.Lock()
	bridge.launches[launchID] = &launchRecord{
		process: schema.CompositorLaunchProcess{
			LaunchID:  launchID,
			SessionID: "session-panel",
			PID:       os.Getpid(),
			Command:   "webview-launcher --url http://127.0.0.1:7780/shell/dist/desktop/ --role panel",
			Status:    "running",
			StartedAt: time.Now().Add(-time.Second),
		},
		expectedTitle: "Agora Desktop Shell",
	}
	bridge.mu.Unlock()

	exclusive := true
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:  schema.PluginMessageSurfaceEvent,
		Event: schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{
			ID:          "layer-shell-plugin-1",
			SurfaceKind: schema.SurfaceKindLayerShell,
			AppID:       "agora-webview",
			Title:       "agora-webview",
			Role:        "panel",
			Geometry:    &schema.SurfaceGeometry{Width: 1280, Height: 48},
			PixelSize:   &schema.SurfaceGeometry{Width: 1280, Height: 48},
			LayerShell: &schema.LayerShellSurfaceMetadata{
				Namespace:     "agora-webview",
				Layer:         "top",
				Anchors:       []string{"top"},
				ExclusiveZone: &exclusive,
			},
		},
		Client: schema.CompositorClientIdentity{PID: int32(os.Getpid()), UID: 60001, GID: 60001},
	})

	bridge.mu.Lock()
	tracked, ok := bridge.surfaces["layer-shell-plugin-1"]
	if ok {
		tracked.UpdatedAt = time.Now().Add(-launchSurfaceSettleDelay - 10*time.Millisecond)
		bridge.surfaces["layer-shell-plugin-1"] = tracked
	}
	bridge.mu.Unlock()
	if !ok {
		t.Fatal("plugin layer-shell surface was not tracked")
	}

	surface, ok := bridge.waitForLaunchSurface(launchID, 50*time.Millisecond)
	if !ok {
		t.Fatal("waitForLaunchSurface did not match plugin-observed layer-shell surface")
	}
	if surface.Surface.ID != "layer-shell-plugin-1" || surface.Surface.SurfaceKind != schema.SurfaceKindLayerShell {
		t.Fatalf("got surface %+v, want layer-shell-plugin-1 layer_shell", surface.Surface)
	}
	if surface.Capturable || surface.InputInjectable {
		t.Fatalf("layer-shell surface should be non-capturable/non-injectable, got capturable=%v injectable=%v", surface.Capturable, surface.InputInjectable)
	}
	if surface.SessionID != "session-panel" {
		t.Fatalf("got session %q, want session-panel", surface.SessionID)
	}
	if surface.Surface.LayerShell == nil || surface.Surface.LayerShell.Namespace != "agora-webview" || surface.Surface.LayerShell.Layer != "top" {
		t.Fatalf("missing layer-shell metadata: %+v", surface.Surface.LayerShell)
	}

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventUnmapped,
		Surface: schema.CompositorSurface{ID: "layer-shell-plugin-1", SurfaceKind: schema.SurfaceKindLayerShell},
		Client:  schema.CompositorClientIdentity{PID: int32(os.Getpid()), UID: 60001, GID: 60001},
	})
	bridge.mu.RLock()
	_, exists := bridge.surfaces["layer-shell-plugin-1"]
	bridge.mu.RUnlock()
	if exists {
		t.Fatal("plugin layer-shell surface was not removed on unmap")
	}
}

func TestScanLaunchStdoutIgnoresToplevelLifecycle(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bridge.scanLaunchStdout("missing-launch", strings.NewReader(`{"event":"mapped","surface_id":"webview-1","surface_kind":"xdg_view","pid":1,"role":"toplevel"}`+"\n"))
	if len(bridge.ListSurfaces()) != 0 {
		t.Fatal("xdg/toplevel stdout advisory should not create a bridge surface")
	}
}

func TestLaunchLifecycleIgnoresNonWebviewLayerShellStdout(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	launchID := "launch-arbitrary"
	bridge.mu.Lock()
	bridge.launches[launchID] = &launchRecord{process: schema.CompositorLaunchProcess{
		LaunchID:  launchID,
		PID:       4242,
		Command:   "sh -c 'printf fake-layer-shell'",
		Status:    "running",
		StartedAt: time.Now().Add(-time.Second),
	}}
	bridge.mu.Unlock()
	bridge.scanLaunchStdout(launchID, strings.NewReader(`{"event":"mapped","surface_id":"layer-shell-4242","surface_kind":"layer_shell","pid":4242,"role":"panel"}`+"\n"))
	if len(bridge.ListSurfaces()) != 0 {
		t.Fatal("non-webview launch stdout should not create a layer-shell surface")
	}
}

func TestDispatchAllowsNonRootListSurfaces(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := bridge.dispatch(1234, schema.Request{Method: schema.MethodListSurfaces})
	if err != nil {
		t.Fatalf("expected non-root list_surfaces to be allowed: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected OK response, got %+v", resp)
	}
}

func TestSyncPluginSessionDoesNotLoseConcurrentPolicyUpdate(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := bridge.UpsertSurfacePolicy(schema.CompositorSurfacePolicy{SurfaceID: "view-1", OwnerUID: 60001}); err != nil {
		t.Fatalf("seed UpsertSurfacePolicy: %v", err)
	}

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	session := &pluginSession{conn: server, enc: json.NewEncoder(server)}
	bridge.installPluginSession(session)
	defer bridge.clearPluginSession(session)

	syncDone := make(chan error, 1)
	go func() {
		syncDone <- bridge.syncPluginSession(session)
	}()

	dec := json.NewDecoder(client)
	var replace schema.CompositorPolicyReplace
	if err := dec.Decode(&replace); err != nil {
		t.Fatalf("decode policy replace: %v", err)
	}
	if len(replace.Surfaces) != 1 || replace.Surfaces[0].SurfaceID != "view-1" {
		t.Fatalf("got replace surfaces %+v, want only seeded policy", replace.Surfaces)
	}

	upsertDone := make(chan error, 1)
	go func() {
		upsertDone <- bridge.UpsertSurfacePolicy(schema.CompositorSurfacePolicy{SurfaceID: "view-2", OwnerUID: 60002})
	}()

	var input schema.CompositorInputContextUpdate
	if err := dec.Decode(&input); err != nil {
		t.Fatalf("decode input context: %v", err)
	}

	select {
	case err := <-syncDone:
		if err != nil {
			t.Fatalf("syncPluginSession: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for syncPluginSession")
	}

	var upsert schema.CompositorPolicyUpsert
	if err := dec.Decode(&upsert); err != nil {
		t.Fatalf("decode policy upsert: %v", err)
	}
	if upsert.Surface.SurfaceID != "view-2" {
		t.Fatalf("got upsert %+v, want view-2", upsert)
	}

	select {
	case err := <-upsertDone:
		if err != nil {
			t.Fatalf("UpsertSurfacePolicy: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for UpsertSurfacePolicy")
	}
}

func TestCloseSurfacesByUIDQueuesMatchingSurfaces(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server, client, cleanup := unixSocketPair(t)
	defer cleanup()

	go bridge.HandlePluginConn(server)
	dec := json.NewDecoder(client)
	readInitialSync(t, dec)

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-a", WayfireViewID: 10},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-b", WayfireViewID: 11},
		Client:  schema.CompositorClientIdentity{UID: 60002},
	})
	_ = dec.Decode(&discard)

	queuedCh := make(chan int, 1)
	errCh := make(chan error, 1)
	go func() {
		queued, err := bridge.CloseSurfacesByUID(60001)
		if err != nil {
			errCh <- err
			return
		}
		queuedCh <- queued
	}()

	var closeMsg schema.CompositorCloseSurfacesByUID
	if err := dec.Decode(&closeMsg); err != nil {
		t.Fatalf("decode close msg: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("CloseSurfacesByUID: %v", err)
	case queued := <-queuedCh:
		if queued != 1 {
			t.Fatalf("got queued %d, want 1", queued)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for CloseSurfacesByUID result")
	}
	if closeMsg.Type != schema.PluginMessageCloseSurfacesByUID || closeMsg.OwnerUID != 60001 {
		t.Fatalf("got close msg %+v, want owner 60001", closeMsg)
	}
}

func readInitialSync(t *testing.T, dec *json.Decoder) {
	t.Helper()
	var replace schema.CompositorPolicyReplace
	if err := dec.Decode(&replace); err != nil {
		t.Fatalf("decode policy replace: %v", err)
	}
	if replace.Type != schema.PluginMessagePolicyReplace {
		t.Fatalf("got sync type %q, want %q", replace.Type, schema.PluginMessagePolicyReplace)
	}

	var input schema.CompositorInputContextUpdate
	if err := dec.Decode(&input); err != nil {
		t.Fatalf("decode input context: %v", err)
	}
	if input.Type != schema.PluginMessageInputContext {
		t.Fatalf("got input type %q, want %q", input.Type, schema.PluginMessageInputContext)
	}
}

func containsUID(values []uint32, want uint32) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func unixSocketPair(t *testing.T) (server net.Conn, client net.Conn, cleanup func()) {
	t.Helper()

	sock := filepath.Join(t.TempDir(), "bridge.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}

	acceptedCh := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		acceptedCh <- conn
		_ = ln.Close()
	}()

	client, err = net.Dial("unix", sock)
	if err != nil {
		_ = ln.Close()
		t.Fatalf("dial unix socket: %v", err)
	}

	select {
	case server = <-acceptedCh:
	case err := <-errCh:
		_ = client.Close()
		_ = ln.Close()
		t.Fatalf("accept unix socket: %v", err)
	case <-time.After(time.Second):
		_ = client.Close()
		_ = ln.Close()
		t.Fatal("timed out accepting unix socket")
	}

	cleanup = func() {
		if client != nil {
			_ = client.Close()
		}
		if server != nil {
			_ = server.Close()
		}
		_ = ln.Close()
	}
	return server, client, cleanup
}

func TestLaunchCommandAppendsNonDefaultRole(t *testing.T) {
	t.Parallel()

	command := launchCommand(schema.LaunchAppRequest{Command: "webview-launcher --url http://example.test", Role: "panel"})
	if command != "webview-launcher --url http://example.test --role panel" {
		t.Fatalf("got command %q", command)
	}
}

func TestLaunchCommandDoesNotAppendDefaultOrDuplicateRole(t *testing.T) {
	t.Parallel()

	if got := launchCommand(schema.LaunchAppRequest{Command: "sleep 30", Role: "toplevel"}); got != "sleep 30" {
		t.Fatalf("default role changed command: %q", got)
	}
	already := "webview-launcher --url http://example.test --role dock"
	if got := launchCommand(schema.LaunchAppRequest{Command: already, Role: "panel"}); got != already {
		t.Fatalf("duplicate role rewrite = %q, want %q", got, already)
	}
}

func TestLaunchCredentialOverrideRequiresRootPeer(t *testing.T) {
	uid := uint32(0)
	gid := uint32(0)
	if _, err := launchCredential(60001, schema.LaunchAppRequest{RunAsUID: &uid, RunAsGID: &gid}); err == nil {
		t.Fatal("expected non-root peer credential override to be rejected")
	} else if !strings.Contains(err.Error(), "require root peer") {
		t.Fatalf("unexpected error: %v", err)
	}

	cred, err := launchCredential(0, schema.LaunchAppRequest{RunAsUID: &uid, RunAsGID: &gid})
	if err != nil {
		t.Fatalf("root peer override returned error: %v", err)
	}
	if cred == nil || cred.Uid != 0 || cred.Gid != 0 {
		t.Fatalf("root peer credential = %+v, want uid/gid 0", cred)
	}
}

func TestDispatchLaunchRejectsNonRootCredentialOverride(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	uid := uint32(0)
	body, err := json.Marshal(schema.LaunchAppRequest{Command: "true", RunAsUID: &uid})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := bridge.dispatch(60001, schema.Request{Method: schema.MethodLaunchApp, Body: body}); err == nil {
		t.Fatal("expected non-root dispatch launch override to be rejected")
	} else if !strings.Contains(err.Error(), "require root peer") {
		t.Fatalf("unexpected error: %v", err)
	}
}
