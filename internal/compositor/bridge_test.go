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

	capturedAt := bridge.recordCaptureReadback("view-wait")
	bridge.mu.RLock()
	capturedOnly := bridge.surfaces["view-wait"]
	bridge.mu.RUnlock()
	if capturedOnly.FrameCount != 0 || capturedOnly.LastPresentTimestamp != nil {
		t.Fatalf("capture readback must not manufacture compositor frame evidence: %+v", capturedOnly)
	}
	if capturedOnly.CaptureCount != 1 || capturedOnly.LastCaptureTimestamp == nil || !capturedOnly.LastCaptureTimestamp.Equal(capturedAt) {
		t.Fatalf("capture readback evidence not recorded: capturedAt=%s surface=%+v", capturedAt, capturedOnly)
	}
	if _, err := bridge.WaitForFrame(schema.WaitForFrameRequest{SurfaceID: "view-wait", TimeoutMs: 20}); err == nil {
		t.Fatal("expected WaitForFrame to still time out with capture-only evidence")
	} else if class, _ := classifyError(err); class != schema.ErrorFrameTimeout {
		t.Fatalf("WaitForFrame with capture-only evidence class = %q, want %q (%v)", class, schema.ErrorFrameTimeout, err)
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

func TestDispatchRaiseRoutesToPluginAndPreservesFocus(t *testing.T) {
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

	visible := true
	focusedStackIndex := 0
	raiseStackIndex := 0
	stackCount := 2
	focusedTop := true
	raiseTop := false
	workspace := &schema.SurfaceWorkspace{X: 0, Y: 0}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-focused", WayfireViewID: 50, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Workspace: workspace, IsTopInStack: &focusedTop, StackIndex: &focusedStackIndex, StackCount: &stackCount}, Client: schema.CompositorClientIdentity{UID: 60001}})
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventFocused, Surface: schema.CompositorSurface{ID: "view-focused", WayfireViewID: 50, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Workspace: workspace, IsTopInStack: &focusedTop, StackIndex: &focusedStackIndex, StackCount: &stackCount}, Client: schema.CompositorClientIdentity{UID: 60001}})
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-raise", WayfireViewID: 52, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Workspace: workspace, IsTopInStack: &raiseTop, StackIndex: &raiseStackIndex, StackCount: &stackCount}, Client: schema.CompositorClientIdentity{UID: 60001}})
	for i := 0; i < 2; i++ {
		var discard schema.CompositorPolicyUpsert
		_ = dec.Decode(&discard)
	}

	body, err := json.Marshal(schema.RaiseSurfaceRequest{SurfaceID: "view-raise", Mode: "no-focus", WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodRaiseSurface, Body: body})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var msg schema.CompositorRaiseSurface
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode raise_surface: %v", err)
	}
	if msg.Type != schema.PluginMessageRaiseSurface || msg.SurfaceID != "view-raise" || msg.RequestID == "" || msg.Mode != "no-focus" {
		t.Fatalf("unexpected raise_surface message: %+v", msg)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageRaiseResponse, RequestID: msg.RequestID, SurfaceID: "view-raise", OK: true}); err != nil {
		t.Fatalf("encode raise response: %v", err)
	}
	raiseTop = true
	raiseStackIndex = 1
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventStacked, Surface: schema.CompositorSurface{ID: "view-raise", WayfireViewID: 52, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Workspace: workspace, IsTopInStack: &raiseTop, StackIndex: &raiseStackIndex, StackCount: &stackCount}, Client: schema.CompositorClientIdentity{UID: 60001}})

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
		if action.Action != "surface.raise" || action.Decision != schema.SurfaceActionAccepted || action.FocusedSurfaceID != "view-focused" || action.ResultState == nil || action.ResultState.Stack == nil || action.ResultState.Stack.IsTopInStack == nil || !*action.ResultState.Stack.IsTopInStack {
			t.Fatalf("unexpected action response: %+v", action)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch response")
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionCompleted {
		t.Fatalf("shell action completion was not published: %+v", pub.events)
	}
}

func TestDispatchRaiseDeniesFocusChangeAfterPluginAck(t *testing.T) {
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

	visible := true
	focusedStackIndex := 1
	raiseStackIndex := 0
	stackCount := 2
	focusedTop := true
	raiseTop := false
	workspace := &schema.SurfaceWorkspace{X: 0, Y: 0}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-focused", WayfireViewID: 50, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Workspace: workspace, IsTopInStack: &focusedTop, StackIndex: &focusedStackIndex, StackCount: &stackCount}, Client: schema.CompositorClientIdentity{UID: 60001}})
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventFocused, Surface: schema.CompositorSurface{ID: "view-focused", WayfireViewID: 50, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Workspace: workspace, IsTopInStack: &focusedTop, StackIndex: &focusedStackIndex, StackCount: &stackCount}, Client: schema.CompositorClientIdentity{UID: 60001}})
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-raise", WayfireViewID: 52, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Workspace: workspace, IsTopInStack: &raiseTop, StackIndex: &raiseStackIndex, StackCount: &stackCount}, Client: schema.CompositorClientIdentity{UID: 60001}})
	for i := 0; i < 2; i++ {
		var discard schema.CompositorPolicyUpsert
		_ = dec.Decode(&discard)
	}

	body, err := json.Marshal(schema.RaiseSurfaceRequest{SurfaceID: "view-raise", Mode: "no-focus", WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodRaiseSurface, Body: body})
		errCh <- err
	}()

	var msg schema.CompositorRaiseSurface
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode raise_surface: %v", err)
	}
	if msg.Type != schema.PluginMessageRaiseSurface || msg.SurfaceID != "view-raise" || msg.RequestID == "" || msg.Mode != "no-focus" {
		t.Fatalf("unexpected raise_surface message: %+v", msg)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageRaiseResponse, RequestID: msg.RequestID, SurfaceID: "view-raise", OK: true}); err != nil {
		t.Fatalf("encode raise response: %v", err)
	}
	raiseTop = true
	raiseStackIndex = 1
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventFocused, Surface: schema.CompositorSurface{ID: "view-raise", WayfireViewID: 52, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Workspace: workspace, IsTopInStack: &raiseTop, StackIndex: &raiseStackIndex, StackCount: &stackCount}, Client: schema.CompositorClientIdentity{UID: 60001}})

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected raise focus-change denial")
		}
		class, message := classifyError(err)
		if class != schema.ErrorProtocolError {
			t.Fatalf("error class = %q, want %q (err=%v)", class, schema.ErrorProtocolError, err)
		}
		if !strings.Contains(message, "surface raise changed focus from view-focused to view-raise") {
			t.Fatalf("error message = %q", message)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for raise focus-change denial")
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
		t.Fatalf("raise denial was not published: %+v", pub.events)
	}
	action, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
	if !ok {
		t.Fatalf("unexpected denial payload type: %#v", pub.events[len(pub.events)-1].body)
	}
	if action.Action != "surface.raise" || action.Decision != schema.SurfaceActionDenied || action.SurfaceID != "view-raise" {
		t.Fatalf("unexpected action denial: %+v", action)
	}
	if action.Surface == nil || action.Surface.Surface.ID != "view-raise" || !action.Surface.Focused || action.Surface.Surface.IsTopInStack == nil || !*action.Surface.Surface.IsTopInStack {
		t.Fatalf("denial missing diagnostic readback surface: %+v", action)
	}
}

func TestRaiseDeniesInvalidSurfaces(t *testing.T) {
	workspace := &schema.SurfaceWorkspace{X: 0, Y: 0}
	cases := []struct {
		name  string
		setup func(*Bridge)
		class string
	}{
		{name: "missing", class: schema.ErrorSurfaceNotFound},
		{name: "stale", class: schema.ErrorSurfaceStale, setup: func(bridge *Bridge) {
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-raise-denied", Role: "toplevel", WayfireViewID: 80, Visible: boolPtr(true), OutputID: "HDMI-A-1", Workspace: workspace}, Client: schema.CompositorClientIdentity{UID: 60001}})
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventUnmapped, Surface: schema.CompositorSurface{ID: "view-raise-denied", WayfireViewID: 80}, Client: schema.CompositorClientIdentity{UID: 60001}})
		}},
		{name: "layer-shell", class: schema.ErrorBackendUnsupported, setup: func(bridge *Bridge) {
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-raise-denied", SurfaceKind: schema.SurfaceKindLayerShell, Visible: boolPtr(true), OutputID: "HDMI-A-1", Workspace: workspace}, Client: schema.CompositorClientIdentity{UID: 60001}})
		}},
		{name: "minimized", class: schema.ErrorSurfaceStale, setup: func(bridge *Bridge) {
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-raise-denied", Role: "toplevel", WayfireViewID: 81, Visible: boolPtr(true), OutputID: "HDMI-A-1", Workspace: workspace, Minimized: boolPtr(true), VisibilityState: "minimized"}, Client: schema.CompositorClientIdentity{UID: 60001}})
		}},
		{name: "fullscreen", class: schema.ErrorBackendUnsupported, setup: func(bridge *Bridge) {
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-raise-denied", Role: "toplevel", WayfireViewID: 82, Visible: boolPtr(true), OutputID: "HDMI-A-1", Workspace: workspace, Fullscreen: boolPtr(true)}, Client: schema.CompositorClientIdentity{UID: 60001}})
		}},
		{name: "off-workspace", class: schema.ErrorBackendUnsupported, setup: func(bridge *Bridge) {
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-raise-denied", Role: "toplevel", WayfireViewID: 83, Visible: boolPtr(true), OutputID: "HDMI-A-1"}, Client: schema.CompositorClientIdentity{UID: 60001}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pub := &fakePublisher{}
			bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if tc.setup != nil {
				tc.setup(bridge)
			}
			body, err := json.Marshal(schema.RaiseSurfaceRequest{SurfaceID: "view-raise-denied", Mode: "no-focus", WaitTimeoutMs: 20})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			_, err = bridge.dispatch(60002, schema.Request{Method: schema.MethodRaiseSurface, Body: body})
			if err == nil {
				t.Fatal("expected raise denial")
			}
			if class, _ := classifyError(err); class != tc.class {
				t.Fatalf("error class = %q, want %q (err=%v)", class, tc.class, err)
			}
			if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
				t.Fatalf("raise denial was not published: %+v", pub.events)
			}
		})
	}
}

func TestDispatchRaiseDeniesPluginAckTimeout(t *testing.T) {
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
	workspace := &schema.SurfaceWorkspace{X: 0, Y: 0}
	stackIndex := 0
	stackCount := 2
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-raise-timeout", WayfireViewID: 84, Role: "toplevel", Visible: boolPtr(true), OutputID: "HDMI-A-1", Workspace: workspace, IsTopInStack: boolPtr(false), StackIndex: &stackIndex, StackCount: &stackCount}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)
	body, err := json.Marshal(schema.RaiseSurfaceRequest{SurfaceID: "view-raise-timeout", Mode: "no-focus", WaitTimeoutMs: 25})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodRaiseSurface, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorRaiseSurface
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode raise_surface: %v", err)
	}
	if msg.Type != schema.PluginMessageRaiseSurface || msg.SurfaceID != "view-raise-timeout" || msg.RequestID == "" || msg.Mode != "no-focus" {
		t.Fatalf("unexpected raise_surface message: %+v", msg)
	}
	select {
	case err := <-errCh:
		assertRaiseDenied(t, pub, err, schema.ErrorFrameTimeout, "surface raise plugin acknowledgement timed out")
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for raise ack timeout")
	}
}

func TestDispatchRaiseDeniesReadbackTimeoutAfterPluginAck(t *testing.T) {
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
	workspace := &schema.SurfaceWorkspace{X: 0, Y: 0}
	stackIndex := 0
	stackCount := 2
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-raise-readback-timeout", WayfireViewID: 85, Role: "toplevel", Visible: boolPtr(true), OutputID: "HDMI-A-1", Workspace: workspace, IsTopInStack: boolPtr(false), StackIndex: &stackIndex, StackCount: &stackCount}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)
	body, err := json.Marshal(schema.RaiseSurfaceRequest{SurfaceID: "view-raise-readback-timeout", Mode: "no-focus", WaitTimeoutMs: 30})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodRaiseSurface, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorRaiseSurface
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode raise_surface: %v", err)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageRaiseResponse, RequestID: msg.RequestID, SurfaceID: "view-raise-readback-timeout", OK: true}); err != nil {
		t.Fatalf("encode raise response: %v", err)
	}
	select {
	case err := <-errCh:
		assertRaiseDenied(t, pub, err, schema.ErrorFrameTimeout, "surface raise stack readback timed out after plugin ack")
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for raise readback timeout")
	}
}

func assertRaiseDenied(t *testing.T, pub *fakePublisher, err error, wantClass, wantMessage string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected raise denial")
	}
	class, message := classifyError(err)
	if class != wantClass {
		t.Fatalf("error class = %q, want %q (err=%v)", class, wantClass, err)
	}
	if wantMessage != "" && !strings.Contains(message, wantMessage) {
		t.Fatalf("error message = %q, want substring %q", message, wantMessage)
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
		t.Fatalf("raise denial was not published: %+v", pub.events)
	}
	result, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
	if !ok || result.Action != "surface.raise" || result.Decision != schema.SurfaceActionDenied {
		t.Fatalf("unexpected raise denial payload: %#v", pub.events[len(pub.events)-1].body)
	}
}

func TestDispatchMinimizeRoutesToPluginRetainsSurfaceAndPublishesAction(t *testing.T) {
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

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-minimize", WayfireViewID: 73, Role: "toplevel", Visible: boolPtr(true), OutputID: "HDMI-A-1", Minimized: boolPtr(false), Restorable: boolPtr(false), VisibilityState: "visible"}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.MinimizeSurfaceRequest{SurfaceID: "view-minimize", Enabled: true, WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodMinimizeSurface, Body: body})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if msg.Type != schema.PluginMessageSetSurfaceState || msg.SurfaceID != "view-minimize" || msg.RequestID == "" || msg.Minimized == nil || !*msg.Minimized || msg.Fullscreen != nil || msg.Maximized != nil {
		t.Fatalf("unexpected set_surface_state message: %+v", msg)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceStateResponse, RequestID: msg.RequestID, SurfaceID: "view-minimize", OK: true}); err != nil {
		t.Fatalf("encode surface state response: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMinimized, Surface: schema.CompositorSurface{ID: "view-minimize", WayfireViewID: 73, Role: "toplevel", Visible: boolPtr(false), OutputID: "HDMI-A-1", Minimized: boolPtr(true), Restorable: boolPtr(true), VisibilityState: "minimized"}, Client: schema.CompositorClientIdentity{UID: 60001}})

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
		if action.Action != "surface.minimize" || action.Decision != schema.SurfaceActionAccepted || action.TargetState == nil || action.TargetState.Minimized == nil || !*action.TargetState.Minimized || action.ResultState == nil || action.ResultState.Minimized == nil || !*action.ResultState.Minimized || action.Minimized == nil || !*action.Minimized || action.Surface == nil || action.Surface.Surface.Minimized == nil || !*action.Surface.Surface.Minimized || action.Surface.Surface.Restorable == nil || !*action.Surface.Surface.Restorable || action.Surface.Surface.VisibilityState != "minimized" {
			t.Fatalf("unexpected action response: %+v", action)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch response")
	}
	if got := bridge.ListSurfaces(); len(got) != 1 || got[0].Surface.ID != "view-minimize" || got[0].Visible || got[0].Capturable || got[0].InputInjectable {
		t.Fatalf("minimized surface was not retained with disabled capture/input: %+v", got)
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionCompleted {
		t.Fatalf("shell action completion was not published: %+v", pub.events)
	}
}

func TestDispatchMinimizeRestoreRoutesToPluginAndPublishesAction(t *testing.T) {
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
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-restore", WayfireViewID: 74, Role: "toplevel", Visible: boolPtr(false), OutputID: "HDMI-A-1", Minimized: boolPtr(true), Restorable: boolPtr(true), VisibilityState: "minimized"}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)
	body, err := json.Marshal(schema.MinimizeSurfaceRequest{SurfaceID: "view-restore", Enabled: false, WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodMinimizeSurface, Body: body})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()
	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if msg.Minimized == nil || *msg.Minimized {
		t.Fatalf("unexpected set_surface_state message: %+v", msg)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceStateResponse, RequestID: msg.RequestID, SurfaceID: "view-restore", OK: true}); err != nil {
		t.Fatalf("encode surface state response: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventRestored, Surface: schema.CompositorSurface{ID: "view-restore", WayfireViewID: 74, Role: "toplevel", Visible: boolPtr(true), OutputID: "HDMI-A-1", Minimized: boolPtr(false), Restorable: boolPtr(false), VisibilityState: "visible"}, Client: schema.CompositorClientIdentity{UID: 60001}})
	select {
	case err := <-errCh:
		t.Fatalf("dispatch returned error: %v", err)
	case resp := <-respCh:
		var action schema.SurfaceActionResponse
		if err := json.Unmarshal(resp.Body, &action); err != nil || action.ResultState == nil || action.ResultState.Minimized == nil || *action.ResultState.Minimized || action.Surface == nil || !action.Surface.Visible {
			t.Fatalf("unexpected response/action err=%v action=%+v", err, action)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for restore response")
	}
}

func TestDispatchMinimizeDeniesMissingStaleAndLayerShell(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Bridge)
		class string
	}{
		{name: "missing", class: schema.ErrorSurfaceNotFound},
		{name: "stale", class: schema.ErrorSurfaceStale, setup: func(bridge *Bridge) {
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-min-denied", Role: "toplevel", WayfireViewID: 75, Visible: boolPtr(true), OutputID: "HDMI-A-1", Minimized: boolPtr(false), VisibilityState: "visible"}, Client: schema.CompositorClientIdentity{UID: 60001}})
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventUnmapped, Surface: schema.CompositorSurface{ID: "view-min-denied", WayfireViewID: 75}, Client: schema.CompositorClientIdentity{UID: 60001}})
		}},
		{name: "layer-shell", class: schema.ErrorBackendUnsupported, setup: func(bridge *Bridge) {
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-min-denied", SurfaceKind: schema.SurfaceKindLayerShell, Visible: boolPtr(true), OutputID: "HDMI-A-1", Minimized: boolPtr(false), VisibilityState: "visible"}, Client: schema.CompositorClientIdentity{UID: 60001}})
		}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			pub := &fakePublisher{}
			bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if tc.setup != nil {
				tc.setup(bridge)
			}
			body, err := json.Marshal(schema.MinimizeSurfaceRequest{SurfaceID: "view-min-denied", Enabled: true, WaitTimeoutMs: 20})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			_, err = bridge.dispatch(60002, schema.Request{Method: schema.MethodMinimizeSurface, Body: body})
			assertMinimizeDenied(t, pub, err, tc.class, "", true, nil, "")
		})
	}
}

func TestDispatchMinimizeDeniesPluginAckTimeout(t *testing.T) {
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
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-min-timeout", WayfireViewID: 76, Role: "toplevel", Visible: boolPtr(true), OutputID: "HDMI-A-1", Minimized: boolPtr(false), VisibilityState: "visible"}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)
	body, err := json.Marshal(schema.MinimizeSurfaceRequest{SurfaceID: "view-min-timeout", Enabled: true, WaitTimeoutMs: 25})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodMinimizeSurface, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if msg.Type != schema.PluginMessageSetSurfaceState || msg.SurfaceID != "view-min-timeout" || msg.RequestID == "" || msg.Minimized == nil || !*msg.Minimized || msg.Fullscreen != nil || msg.Maximized != nil {
		t.Fatalf("unexpected set_surface_state message: %+v", msg)
	}
	select {
	case err := <-errCh:
		assertMinimizeDenied(t, pub, err, schema.ErrorFrameTimeout, "minimize plugin acknowledgement timed out", true, boolPtr(false), "visible")
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for minimize ack timeout")
	}
}

func TestDispatchMinimizeDeniesReadbackTimeoutAfterPluginAck(t *testing.T) {
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
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-min-readback-timeout", WayfireViewID: 77, Role: "toplevel", Visible: boolPtr(true), OutputID: "HDMI-A-1", Minimized: boolPtr(false), VisibilityState: "visible"}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)
	body, err := json.Marshal(schema.MinimizeSurfaceRequest{SurfaceID: "view-min-readback-timeout", Enabled: true, WaitTimeoutMs: 30})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodMinimizeSurface, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceStateResponse, RequestID: msg.RequestID, SurfaceID: "view-min-readback-timeout", OK: true}); err != nil {
		t.Fatalf("encode surface state response: %v", err)
	}
	select {
	case err := <-errCh:
		assertMinimizeDenied(t, pub, err, schema.ErrorFrameTimeout, "minimize readback timed out after plugin ack", true, boolPtr(false), "visible")
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for minimize readback timeout")
	}
}

func TestDispatchMinimizeDeniesPluginNegativeAck(t *testing.T) {
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
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-min-negative-ack", WayfireViewID: 78, Role: "toplevel", Visible: boolPtr(true), OutputID: "HDMI-A-1", Minimized: boolPtr(false), VisibilityState: "visible"}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)
	body, err := json.Marshal(schema.MinimizeSurfaceRequest{SurfaceID: "view-min-negative-ack", Enabled: true, WaitTimeoutMs: 250})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodMinimizeSurface, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceStateResponse, RequestID: msg.RequestID, SurfaceID: "view-min-negative-ack", OK: false, Error: "backend refused minimize"}); err != nil {
		t.Fatalf("encode surface state response: %v", err)
	}
	select {
	case err := <-errCh:
		assertMinimizeDenied(t, pub, err, schema.ErrorProtocolError, "backend refused minimize", true, boolPtr(false), "visible")
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for minimize negative ack denial")
	}
}

func TestMinimizedSurfaceTerminalUnmapRemovesRetainedSurface(t *testing.T) {
	bridge, err := New(&fakePublisher{}, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMinimized, Surface: schema.CompositorSurface{ID: "view-min-unmap", WayfireViewID: 79, Role: "toplevel", Visible: boolPtr(false), OutputID: "HDMI-A-1", Minimized: boolPtr(true), Restorable: boolPtr(true), VisibilityState: "minimized"}, Client: schema.CompositorClientIdentity{UID: 60001}})
	if got := bridge.ListSurfaces(); len(got) != 1 || got[0].Surface.ID != "view-min-unmap" || got[0].Surface.Minimized == nil || !*got[0].Surface.Minimized {
		t.Fatalf("minimized surface not retained before terminal unmap: %+v", got)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventUnmapped, Surface: schema.CompositorSurface{ID: "view-min-unmap", WayfireViewID: 79}, Client: schema.CompositorClientIdentity{UID: 60001}})
	if got := bridge.ListSurfaces(); len(got) != 0 {
		t.Fatalf("terminal unmap should remove retained minimized surface: %+v", got)
	}
}

func assertMinimizeDenied(t *testing.T, pub *fakePublisher, err error, wantClass, wantMessage string, wantTarget bool, wantReadback *bool, wantVisibility string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected minimize denial error")
	}
	if class, _ := classifyError(err); class != wantClass {
		t.Fatalf("got error class %q (%v), want %q", class, err, wantClass)
	}
	if wantMessage != "" && !strings.Contains(err.Error(), wantMessage) {
		t.Fatalf("error %q does not contain %q", err.Error(), wantMessage)
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
		t.Fatalf("shell action denial was not published: %+v", pub.events)
	}
	action, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
	if !ok || action.Action != "surface.minimize" || action.Decision != schema.SurfaceActionDenied || action.TargetState == nil || action.TargetState.Minimized == nil || *action.TargetState.Minimized != wantTarget {
		t.Fatalf("unexpected denied action %+v", pub.events[len(pub.events)-1].body)
	}
	if wantReadback != nil {
		if action.Surface == nil || action.Surface.Surface.Minimized == nil || *action.Surface.Surface.Minimized != *wantReadback {
			t.Fatalf("denied action missing readback minimized=%v: %+v", *wantReadback, action)
		}
		if wantVisibility != "" && action.Surface.Surface.VisibilityState != wantVisibility {
			t.Fatalf("denied action visibility_state=%q, want %q: %+v", action.Surface.Surface.VisibilityState, wantVisibility, action)
		}
	}
}

func TestDispatchMaximizeRoutesToPluginAndPublishesAction(t *testing.T) {
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

	visible := true
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-maximize", WayfireViewID: 63, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Maximized: boolPtr(false), TiledEdges: &schema.SurfaceTiledEdges{Bits: 0}}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.MaximizeSurfaceRequest{SurfaceID: "view-maximize", Enabled: true, WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodMaximizeSurface, Body: body})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if msg.Type != schema.PluginMessageSetSurfaceState || msg.SurfaceID != "view-maximize" || msg.RequestID == "" || msg.Maximized == nil || !*msg.Maximized || msg.Fullscreen != nil {
		t.Fatalf("unexpected set_surface_state message: %+v", msg)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceStateResponse, RequestID: msg.RequestID, SurfaceID: "view-maximize", OK: true}); err != nil {
		t.Fatalf("encode surface state response: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventFocused, Surface: schema.CompositorSurface{ID: "view-maximize", WayfireViewID: 63, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Maximized: &visible, TiledEdges: &schema.SurfaceTiledEdges{Bits: 15, Edges: []string{"top", "bottom", "left", "right"}}}, Client: schema.CompositorClientIdentity{UID: 60001}})

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
		if action.Action != "surface.maximize" || action.Decision != schema.SurfaceActionAccepted || action.TargetState == nil || action.TargetState.Maximized == nil || !*action.TargetState.Maximized || action.TargetState.TiledEdges == nil || action.TargetState.TiledEdges.Bits != 15 || action.ResultState == nil || action.ResultState.Maximized == nil || !*action.ResultState.Maximized || action.Maximized == nil || !*action.Maximized || action.Surface == nil || action.Surface.Surface.Maximized == nil || !*action.Surface.Surface.Maximized {
			t.Fatalf("unexpected action response: %+v", action)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch response")
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionCompleted {
		t.Fatalf("shell action completion was not published: %+v", pub.events)
	}
}

func TestDispatchMaximizeRestoreRoutesToPluginAndPublishesAction(t *testing.T) {
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

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-maximize-restore", WayfireViewID: 64, Role: "toplevel", Visible: boolPtr(true), OutputID: "HDMI-A-1", Maximized: boolPtr(true), TiledEdges: &schema.SurfaceTiledEdges{Bits: 15, Edges: []string{"top", "bottom", "left", "right"}}}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.MaximizeSurfaceRequest{SurfaceID: "view-maximize-restore", Enabled: false, WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodMaximizeSurface, Body: body})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if msg.Type != schema.PluginMessageSetSurfaceState || msg.SurfaceID != "view-maximize-restore" || msg.RequestID == "" || msg.Maximized == nil || *msg.Maximized || msg.Fullscreen != nil {
		t.Fatalf("unexpected set_surface_state message: %+v", msg)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceStateResponse, RequestID: msg.RequestID, SurfaceID: "view-maximize-restore", OK: true}); err != nil {
		t.Fatalf("encode surface state response: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventFocused, Surface: schema.CompositorSurface{ID: "view-maximize-restore", WayfireViewID: 64, Role: "toplevel", Visible: boolPtr(true), OutputID: "HDMI-A-1", Maximized: boolPtr(false), TiledEdges: &schema.SurfaceTiledEdges{Bits: 0}}, Client: schema.CompositorClientIdentity{UID: 60001}})

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
		if action.Action != "surface.maximize" || action.Decision != schema.SurfaceActionAccepted || action.TargetState == nil || action.TargetState.Maximized == nil || *action.TargetState.Maximized || action.TargetState.TiledEdges == nil || action.TargetState.TiledEdges.Bits != 0 || action.ResultState == nil || action.ResultState.Maximized == nil || *action.ResultState.Maximized || action.ResultState.TiledEdges == nil || action.ResultState.TiledEdges.Bits != 0 || action.Maximized == nil || *action.Maximized || action.Surface == nil || action.Surface.Surface.Maximized == nil || *action.Surface.Surface.Maximized || action.Surface.Surface.TiledEdges == nil || action.Surface.Surface.TiledEdges.Bits != 0 {
			t.Fatalf("unexpected action response: %+v", action)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch response")
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionCompleted {
		t.Fatalf("shell action completion was not published: %+v", pub.events)
	}
	action, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
	if !ok || action.Action != "surface.maximize" || action.ResultState == nil || action.ResultState.Maximized == nil || *action.ResultState.Maximized || action.ResultState.TiledEdges == nil || action.ResultState.TiledEdges.Bits != 0 {
		t.Fatalf("unexpected completion event: %+v", pub.events[len(pub.events)-1].body)
	}
}

func TestDispatchMaximizeDeniesMissingStaleAndLayerShell(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Bridge)
		class string
	}{
		{name: "missing", class: schema.ErrorSurfaceNotFound},
		{name: "stale", class: schema.ErrorSurfaceStale, setup: func(bridge *Bridge) {
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-max-denied", Role: "toplevel", WayfireViewID: 65, Visible: boolPtr(true), OutputID: "HDMI-A-1", Maximized: boolPtr(false), TiledEdges: &schema.SurfaceTiledEdges{Bits: 0}}, Client: schema.CompositorClientIdentity{UID: 60001}})
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventUnmapped, Surface: schema.CompositorSurface{ID: "view-max-denied", WayfireViewID: 65}, Client: schema.CompositorClientIdentity{UID: 60001}})
		}},
		{name: "layer-shell", class: schema.ErrorBackendUnsupported, setup: func(bridge *Bridge) {
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-max-denied", SurfaceKind: schema.SurfaceKindLayerShell, Visible: boolPtr(true), OutputID: "HDMI-A-1", Maximized: boolPtr(false), TiledEdges: &schema.SurfaceTiledEdges{Bits: 0}}, Client: schema.CompositorClientIdentity{UID: 60001}})
		}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			pub := &fakePublisher{}
			bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if tc.setup != nil {
				tc.setup(bridge)
			}
			body, err := json.Marshal(schema.MaximizeSurfaceRequest{SurfaceID: "view-max-denied", Enabled: true, WaitTimeoutMs: 20})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			_, err = bridge.dispatch(60002, schema.Request{Method: schema.MethodMaximizeSurface, Body: body})
			assertMaximizeDenied(t, pub, err, tc.class, "", true, nil, nil)
		})
	}
}

func TestDispatchMaximizeDeniesPluginAckTimeout(t *testing.T) {
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

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-max-timeout", WayfireViewID: 66, Role: "toplevel", Visible: boolPtr(true), OutputID: "HDMI-A-1", Maximized: boolPtr(false), TiledEdges: &schema.SurfaceTiledEdges{Bits: 0}}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.MaximizeSurfaceRequest{SurfaceID: "view-max-timeout", Enabled: true, WaitTimeoutMs: 25})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodMaximizeSurface, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if msg.Type != schema.PluginMessageSetSurfaceState || msg.SurfaceID != "view-max-timeout" || msg.RequestID == "" || msg.Maximized == nil || !*msg.Maximized || msg.Fullscreen != nil {
		t.Fatalf("unexpected set_surface_state message: %+v", msg)
	}
	select {
	case err := <-errCh:
		assertMaximizeDenied(t, pub, err, schema.ErrorFrameTimeout, "maximize plugin acknowledgement timed out", true, boolPtr(false), &schema.SurfaceTiledEdges{Bits: 0})
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for maximize ack timeout")
	}
}

func TestDispatchMaximizeDeniesReadbackTimeoutAfterPluginAck(t *testing.T) {
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

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-max-readback-timeout", WayfireViewID: 67, Role: "toplevel", Visible: boolPtr(true), OutputID: "HDMI-A-1", Maximized: boolPtr(false), TiledEdges: &schema.SurfaceTiledEdges{Bits: 0}}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.MaximizeSurfaceRequest{SurfaceID: "view-max-readback-timeout", Enabled: true, WaitTimeoutMs: 30})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodMaximizeSurface, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceStateResponse, RequestID: msg.RequestID, SurfaceID: "view-max-readback-timeout", OK: true}); err != nil {
		t.Fatalf("encode surface state response: %v", err)
	}
	select {
	case err := <-errCh:
		assertMaximizeDenied(t, pub, err, schema.ErrorFrameTimeout, "maximize readback timed out after plugin ack", true, boolPtr(false), &schema.SurfaceTiledEdges{Bits: 0})
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for maximize readback timeout")
	}
}

func TestDispatchMaximizeDeniesPluginNegativeAck(t *testing.T) {
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

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-max-negative-ack", WayfireViewID: 68, Role: "toplevel", Visible: boolPtr(true), OutputID: "HDMI-A-1", Maximized: boolPtr(false), TiledEdges: &schema.SurfaceTiledEdges{Bits: 0}}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.MaximizeSurfaceRequest{SurfaceID: "view-max-negative-ack", Enabled: true, WaitTimeoutMs: 250})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodMaximizeSurface, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceStateResponse, RequestID: msg.RequestID, SurfaceID: "view-max-negative-ack", OK: false, Error: "backend refused maximize"}); err != nil {
		t.Fatalf("encode surface state response: %v", err)
	}
	select {
	case err := <-errCh:
		assertMaximizeDenied(t, pub, err, schema.ErrorProtocolError, "backend refused maximize", true, boolPtr(false), &schema.SurfaceTiledEdges{Bits: 0})
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for maximize negative ack denial")
	}
}

func assertMaximizeDenied(t *testing.T, pub *fakePublisher, err error, wantClass, wantMessage string, wantTarget bool, wantReadback *bool, wantEdges *schema.SurfaceTiledEdges) {
	t.Helper()
	if err == nil {
		t.Fatal("expected maximize denial error")
	}
	if class, _ := classifyError(err); class != wantClass {
		t.Fatalf("got error class %q (%v), want %q", class, err, wantClass)
	}
	if wantMessage != "" && !strings.Contains(err.Error(), wantMessage) {
		t.Fatalf("error %q does not contain %q", err.Error(), wantMessage)
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
		t.Fatalf("shell action denial was not published: %+v", pub.events)
	}
	action, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
	if !ok || action.Action != "surface.maximize" || action.Decision != schema.SurfaceActionDenied || action.TargetState == nil || action.TargetState.Maximized == nil || *action.TargetState.Maximized != wantTarget || action.TargetState.TiledEdges == nil || action.TargetState.TiledEdges.Bits != tiledEdgesForMaximized(wantTarget).Bits {
		t.Fatalf("unexpected denied action %+v", pub.events[len(pub.events)-1].body)
	}
	if wantReadback != nil {
		if action.Surface == nil || action.Surface.Surface.Maximized == nil || *action.Surface.Surface.Maximized != *wantReadback {
			t.Fatalf("denied action missing readback maximized=%v: %+v", *wantReadback, action)
		}
	}
	if wantEdges != nil {
		if action.Surface == nil || action.Surface.Surface.TiledEdges == nil || action.Surface.Surface.TiledEdges.Bits != wantEdges.Bits {
			t.Fatalf("denied action missing readback tiled_edges=%+v: %+v", wantEdges, action)
		}
	}
}

func TestDispatchFullscreenRoutesToPluginAndPublishesAction(t *testing.T) {
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

	visible := true
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-fullscreen", WayfireViewID: 62, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1"}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.FullscreenSurfaceRequest{SurfaceID: "view-fullscreen", Enabled: true, WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodFullscreenSurface, Body: body})
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if msg.Type != schema.PluginMessageSetSurfaceState || msg.SurfaceID != "view-fullscreen" || msg.RequestID == "" || msg.Fullscreen == nil || !*msg.Fullscreen {
		t.Fatalf("unexpected set_surface_state message: %+v", msg)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceStateResponse, RequestID: msg.RequestID, SurfaceID: "view-fullscreen", OK: true}); err != nil {
		t.Fatalf("encode surface state response: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventFocused, Surface: schema.CompositorSurface{ID: "view-fullscreen", WayfireViewID: 62, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Fullscreen: &visible}, Client: schema.CompositorClientIdentity{UID: 60001}})

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
		if action.Action != "surface.fullscreen" || action.Decision != schema.SurfaceActionAccepted || action.TargetState == nil || action.TargetState.Fullscreen == nil || !*action.TargetState.Fullscreen || action.ResultState == nil || action.ResultState.Fullscreen == nil || !*action.ResultState.Fullscreen || action.Fullscreen == nil || !*action.Fullscreen || action.Surface == nil || action.Surface.Surface.Fullscreen == nil || !*action.Surface.Surface.Fullscreen {
			t.Fatalf("unexpected action response: %+v", action)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch response")
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionCompleted {
		t.Fatalf("shell action completion was not published: %+v", pub.events)
	}
}

func TestDispatchFullscreenDeniesPluginAckTimeout(t *testing.T) {
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

	visible := true
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-full-timeout", WayfireViewID: 62, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Fullscreen: boolPtr(false)}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.FullscreenSurfaceRequest{SurfaceID: "view-full-timeout", Enabled: true, WaitTimeoutMs: 25})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodFullscreenSurface, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if msg.Type != schema.PluginMessageSetSurfaceState || msg.SurfaceID != "view-full-timeout" || msg.RequestID == "" || msg.Fullscreen == nil || !*msg.Fullscreen {
		t.Fatalf("unexpected set_surface_state message: %+v", msg)
	}
	select {
	case err := <-errCh:
		assertFullscreenDenied(t, pub, err, schema.ErrorFrameTimeout, "fullscreen plugin acknowledgement timed out", true, false)
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for fullscreen ack timeout")
	}
}

func TestDispatchFullscreenDeniesReadbackTimeoutAfterPluginAck(t *testing.T) {
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

	visible := true
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-full-readback-timeout", WayfireViewID: 62, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Fullscreen: boolPtr(false)}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.FullscreenSurfaceRequest{SurfaceID: "view-full-readback-timeout", Enabled: true, WaitTimeoutMs: 30})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodFullscreenSurface, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceStateResponse, RequestID: msg.RequestID, SurfaceID: "view-full-readback-timeout", OK: true}); err != nil {
		t.Fatalf("encode surface state response: %v", err)
	}
	select {
	case err := <-errCh:
		assertFullscreenDenied(t, pub, err, schema.ErrorFrameTimeout, "fullscreen readback timed out after plugin ack", true, false)
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for fullscreen readback timeout")
	}
}

func TestDispatchFullscreenDeniesPluginNegativeAck(t *testing.T) {
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

	visible := true
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-full-negative-ack", WayfireViewID: 62, Role: "toplevel", Visible: &visible, OutputID: "HDMI-A-1", Fullscreen: boolPtr(false)}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.FullscreenSurfaceRequest{SurfaceID: "view-full-negative-ack", Enabled: true, WaitTimeoutMs: 250})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodFullscreenSurface, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorSetSurfaceState
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_surface_state: %v", err)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceStateResponse, RequestID: msg.RequestID, SurfaceID: "view-full-negative-ack", OK: false, Error: "backend refused fullscreen"}); err != nil {
		t.Fatalf("encode surface state response: %v", err)
	}
	select {
	case err := <-errCh:
		assertFullscreenDenied(t, pub, err, schema.ErrorProtocolError, "backend refused fullscreen", true, false)
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for fullscreen negative ack denial")
	}
}

func assertFullscreenDenied(t *testing.T, pub *fakePublisher, err error, wantClass, wantMessage string, wantTarget, wantReadback bool) {
	t.Helper()
	if err == nil {
		t.Fatal("expected fullscreen denial error")
	}
	if class, _ := classifyError(err); class != wantClass {
		t.Fatalf("got error class %q (%v), want %q", class, err, wantClass)
	}
	if !strings.Contains(err.Error(), wantMessage) {
		t.Fatalf("error %q does not contain %q", err.Error(), wantMessage)
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
		t.Fatalf("shell action denial was not published: %+v", pub.events)
	}
	action, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
	if !ok || action.Action != "surface.fullscreen" || action.Decision != schema.SurfaceActionDenied || action.TargetState == nil || action.TargetState.Fullscreen == nil || *action.TargetState.Fullscreen != wantTarget {
		t.Fatalf("unexpected denied action %+v", pub.events[len(pub.events)-1].body)
	}
	if action.Surface == nil || action.Surface.Surface.Fullscreen == nil || *action.Surface.Surface.Fullscreen != wantReadback {
		t.Fatalf("denied action missing readback fullscreen=%v: %+v", wantReadback, action)
	}
}

func TestDispatchFullscreenDeniesMissingStaleAndNoOutput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Bridge)
		class string
	}{
		{name: "missing", class: schema.ErrorSurfaceNotFound},
		{name: "stale", class: schema.ErrorSurfaceStale, setup: func(bridge *Bridge) {
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-full", Role: "toplevel", WayfireViewID: 52, Visible: boolPtr(true), OutputID: "HDMI-A-1"}, Client: schema.CompositorClientIdentity{UID: 60001}})
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventUnmapped, Surface: schema.CompositorSurface{ID: "view-full", WayfireViewID: 52}, Client: schema.CompositorClientIdentity{UID: 60001}})
		}},
		{name: "no output", class: schema.ErrorBackendUnsupported, setup: func(bridge *Bridge) {
			bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-full", Role: "toplevel", WayfireViewID: 52, Visible: boolPtr(true)}, Client: schema.CompositorClientIdentity{UID: 60001}})
		}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			pub := &fakePublisher{}
			bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if tc.setup != nil {
				tc.setup(bridge)
			}
			body, err := json.Marshal(schema.FullscreenSurfaceRequest{SurfaceID: "view-full", Enabled: true, WaitTimeoutMs: 20})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			if _, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodFullscreenSurface, Body: body}); err == nil {
				t.Fatal("expected fullscreen denial")
			} else if class, _ := classifyError(err); class != tc.class {
				t.Fatalf("got error class %q (%v), want %q", class, err, tc.class)
			}
			if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
				t.Fatalf("shell action denial was not published: %+v", pub.events)
			}
			action, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
			if !ok || action.Action != "surface.fullscreen" || action.Decision != schema.SurfaceActionDenied || action.TargetState == nil || action.TargetState.Fullscreen == nil || !*action.TargetState.Fullscreen {
				t.Fatalf("unexpected denied action %+v", pub.events[len(pub.events)-1].body)
			}
		})
	}
}

func TestDispatchFullscreenRejectsLayerShell(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "layer-full", SurfaceKind: schema.SurfaceKindLayerShell, Visible: boolPtr(true), OutputID: "HDMI-A-1"}, Client: schema.CompositorClientIdentity{UID: 60001}})
	body, err := json.Marshal(schema.FullscreenSurfaceRequest{SurfaceID: "layer-full", Enabled: true, WaitTimeoutMs: 20})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodFullscreenSurface, Body: body}); err == nil {
		t.Fatal("expected layer-shell fullscreen denial")
	} else if class, _ := classifyError(err); class != schema.ErrorBackendUnsupported {
		t.Fatalf("got error class %q (%v), want %q", class, err, schema.ErrorBackendUnsupported)
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
		t.Fatalf("shell action denial was not published: %+v", pub.events)
	}
	action, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
	if !ok || action.Action != "surface.fullscreen" || action.Decision != schema.SurfaceActionDenied || action.TargetState == nil || action.TargetState.Fullscreen == nil || !*action.TargetState.Fullscreen {
		t.Fatalf("unexpected denied action %+v", pub.events[len(pub.events)-1].body)
	}
}

func TestDispatchAlwaysOnTopRoutesToPluginAndPublishesAction(t *testing.T) {
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

	visible := true
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-pin", WayfireViewID: 52, Visible: &visible}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.AlwaysOnTopRequest{SurfaceID: "view-pin", Enabled: true, WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodAlwaysOnTop, Body: body})
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
	if msg.Type != schema.PluginMessageSetViewProperty || msg.SurfaceID != "view-pin" || msg.RequestID == "" || msg.Properties["always_on_top"] != true {
		t.Fatalf("unexpected set_view_property message: %+v", msg)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPluginEvent{Type: schema.PluginMessagePropertyResponse, RequestID: msg.RequestID, SurfaceID: "view-pin", OK: true}); err != nil {
		t.Fatalf("encode property response: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventFocused, Surface: schema.CompositorSurface{ID: "view-pin", WayfireViewID: 52, Visible: &visible, AlwaysOnTop: &visible}, Client: schema.CompositorClientIdentity{UID: 60001}})

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
		if action.Action != "surface.always_on_top" || action.Decision != schema.SurfaceActionAccepted || action.TargetState == nil || action.TargetState.AlwaysOnTop == nil || !*action.TargetState.AlwaysOnTop || action.ResultState == nil || action.ResultState.AlwaysOnTop == nil || !*action.ResultState.AlwaysOnTop || action.AlwaysOnTop == nil || !*action.AlwaysOnTop || action.Surface == nil || action.Surface.Surface.AlwaysOnTop == nil || !*action.Surface.Surface.AlwaysOnTop {
			t.Fatalf("unexpected action response: %+v", action)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch response")
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionCompleted {
		t.Fatalf("shell action completion was not published: %+v", pub.events)
	}
}

func TestDispatchAlwaysOnTopDeniesMissingAndStaleSurfaces(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stale bool
		class string
	}{
		{name: "missing", class: schema.ErrorSurfaceNotFound},
		{name: "stale", stale: true, class: schema.ErrorSurfaceStale},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			pub := &fakePublisher{}
			bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if tc.stale {
				bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-pin", WayfireViewID: 52, Visible: boolPtr(true)}, Client: schema.CompositorClientIdentity{UID: 60001}})
				bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventUnmapped, Surface: schema.CompositorSurface{ID: "view-pin", WayfireViewID: 52}, Client: schema.CompositorClientIdentity{UID: 60001}})
			}
			body, err := json.Marshal(schema.AlwaysOnTopRequest{SurfaceID: "view-pin", Enabled: true, WaitTimeoutMs: 20})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			if _, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodAlwaysOnTop, Body: body}); err == nil {
				t.Fatal("expected always_on_top denial")
			} else if class, _ := classifyError(err); class != tc.class {
				t.Fatalf("got error class %q (%v), want %q", class, err, tc.class)
			}
			if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
				t.Fatalf("shell action denial was not published: %+v", pub.events)
			}
			action, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
			if !ok {
				t.Fatalf("denied event body type %T", pub.events[len(pub.events)-1].body)
			}
			if action.Action != "surface.always_on_top" || action.Decision != schema.SurfaceActionDenied || action.TargetState == nil || action.TargetState.AlwaysOnTop == nil || !*action.TargetState.AlwaysOnTop {
				t.Fatalf("unexpected denied action %+v", action)
			}
		})
	}
}

func TestDispatchAlwaysOnTopRejectsLayerShell(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "layer-pin", SurfaceKind: schema.SurfaceKindLayerShell, Visible: boolPtr(true)}, Client: schema.CompositorClientIdentity{UID: 60001}})
	body, err := json.Marshal(schema.AlwaysOnTopRequest{SurfaceID: "layer-pin", Enabled: true, WaitTimeoutMs: 20})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodAlwaysOnTop, Body: body}); err == nil {
		t.Fatal("expected layer-shell always_on_top denial")
	} else if class, _ := classifyError(err); class != schema.ErrorBackendUnsupported {
		t.Fatalf("got error class %q (%v), want %q", class, err, schema.ErrorBackendUnsupported)
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
		t.Fatalf("shell action denial was not published: %+v", pub.events)
	}
	action, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
	if !ok || action.Action != "surface.always_on_top" || action.Decision != schema.SurfaceActionDenied || action.Surface == nil || action.Surface.Surface.SurfaceKind != schema.SurfaceKindLayerShell {
		t.Fatalf("unexpected denied action %+v", pub.events[len(pub.events)-1].body)
	}
}

func TestDispatchAlwaysOnTopPublishesDeniedOnPluginAckTimeout(t *testing.T) {
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
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-timeout", WayfireViewID: 55, Visible: boolPtr(true)}, Client: schema.CompositorClientIdentity{UID: 60001}})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)
	body, err := json.Marshal(schema.AlwaysOnTopRequest{SurfaceID: "view-timeout", Enabled: true, WaitTimeoutMs: 30})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodAlwaysOnTop, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorSetViewProperty
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode set_view_property: %v", err)
	}
	if msg.Type != schema.PluginMessageSetViewProperty || msg.SurfaceID != "view-timeout" || msg.RequestID == "" {
		t.Fatalf("unexpected set_view_property message: %+v", msg)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if class, _ := classifyError(err); class != schema.ErrorFrameTimeout {
			t.Fatalf("got error class %q (%v), want %q", class, err, schema.ErrorFrameTimeout)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for always_on_top dispatch timeout")
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellActionDenied {
		t.Fatalf("shell action denial was not published: %+v", pub.events)
	}
	action, ok := pub.events[len(pub.events)-1].body.(schema.SurfaceActionResponse)
	if !ok || action.Action != "surface.always_on_top" || action.Decision != schema.SurfaceActionDenied || action.TargetState == nil || action.TargetState.AlwaysOnTop == nil || !*action.TargetState.AlwaysOnTop || action.Surface == nil || action.Surface.Surface.ID != "view-timeout" {
		t.Fatalf("unexpected denied action %+v", pub.events[len(pub.events)-1].body)
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

func TestDispatchAssignSurfaceTagPlacesSurfaceAndDecoratesReadback(t *testing.T) {
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
		Surface: schema.CompositorSurface{ID: "view-layout", WayfireViewID: 60, Visible: boolPtr(true), Geometry: &schema.SurfaceGeometry{X: 40, Y: 50, Width: 200, Height: 100}},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)

	body, err := json.Marshal(schema.AssignSurfaceTagRequest{SurfaceID: "view-layout", TagID: schema.DefaultLayoutTagID, LayoutID: schema.BuiltinDevStandardLayoutID, ZoneID: "terminal", WaitTimeoutMs: 500})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	respCh := make(chan schema.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodAssignSurfaceTag, Body: body})
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
	if msg.Type != string(schema.PluginMessagePlaceSurface) || msg.SurfaceID != "view-layout" || msg.Geometry.X != 1455 || msg.Geometry.Y != 771 || msg.Geometry.Width != 200 || msg.Geometry.Height != 100 {
		t.Fatalf("unexpected place_surface message: %+v", msg)
	}
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventFocused,
		Surface: schema.CompositorSurface{ID: "view-layout", WayfireViewID: 60, Visible: boolPtr(true), Geometry: &schema.SurfaceGeometry{X: 1455, Y: 771, Width: 200, Height: 100}},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	if err := json.NewEncoder(client).Encode(schema.CompositorPlacePluginResponse{Type: string(schema.PluginMessagePlaceResponse), RequestID: msg.RequestID, SurfaceID: "view-layout", OK: true}); err != nil {
		t.Fatalf("encode place response: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("dispatch returned error: %v", err)
	case resp := <-respCh:
		if !resp.OK {
			t.Fatalf("dispatch response not OK: %+v", resp)
		}
		var result schema.PlacementResult
		if err := json.Unmarshal(resp.Body, &result); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if result.Decision != schema.SurfaceActionAccepted || len(result.Placements) != 1 || result.Placements[0].ZoneID != "terminal" || result.Placements[0].TargetGeometry == nil || result.Placements[0].TargetGeometry.Width != 200 || result.Placements[0].ResultGeometry == nil || result.Placements[0].ResultGeometry.Width != 200 {
			t.Fatalf("unexpected placement result: %+v", result)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch response")
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellLayoutApplied {
		t.Fatalf("shell layout applied event was not published: %+v", pub.events)
	}
	surfaces := bridge.ListSurfaces()
	if len(surfaces) != 1 || surfaces[0].Surface.ManagementState != schema.SurfaceManaged || surfaces[0].Surface.Placement == nil || surfaces[0].Surface.Placement.ManagementState != schema.SurfaceManaged || surfaces[0].Surface.Placement.ZoneID != "terminal" {
		t.Fatalf("surface readback missing managed placement: %+v", surfaces)
	}
}

func TestDispatchAssignSurfaceTagRejectsUnsupportedMode(t *testing.T) {
	pub := &fakePublisher{}
	bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body, err := json.Marshal(schema.AssignSurfaceTagRequest{SurfaceID: "view-layout", TagID: schema.DefaultLayoutTagID, Mode: schema.LayoutModeGrid})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodAssignSurfaceTag, Body: body}); err == nil {
		t.Fatal("expected unsupported mode denial")
	} else {
		class, _ := classifyError(err)
		if class != schema.ErrorUnsupportedLayoutMode {
			t.Fatalf("got error class %q (%v), want %q", class, err, schema.ErrorUnsupportedLayoutMode)
		}
	}
	if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellLayoutDenied {
		t.Fatalf("shell layout denial was not published: %+v", pub.events)
	}
}

func TestDispatchListLayoutZonesReturnsResolvedBuiltin(t *testing.T) {
	bridge, err := New(nil, Config{AllowedPluginUID: uint32(os.Getuid())})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodListLayoutZones, Body: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("dispatch list zones: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response not OK: %+v", resp)
	}
	var body schema.ListLayoutZonesResponse
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Layout.LayoutID != schema.BuiltinDevStandardLayoutID || len(body.Zones) != 3 || body.Zones[0].ResolvedGeometry == nil {
		t.Fatalf("unexpected zones response: %+v", body)
	}
}

func TestDispatchAssignSurfaceTagDenialClasses(t *testing.T) {
	tests := []struct {
		name      string
		request   schema.AssignSurfaceTagRequest
		setup     func(*Bridge)
		wantClass string
	}{
		{name: "surface not found", request: schema.AssignSurfaceTagRequest{SurfaceID: "missing", TagID: schema.DefaultLayoutTagID, LayoutID: schema.BuiltinDevStandardLayoutID}, wantClass: schema.ErrorSurfaceNotFound},
		{name: "tag not found", request: schema.AssignSurfaceTagRequest{SurfaceID: "view-layout", TagID: "missing-tag", LayoutID: schema.BuiltinDevStandardLayoutID}, setup: addVisibleLayoutSurface, wantClass: schema.ErrorLayoutTagNotFound},
		{name: "zone not found", request: schema.AssignSurfaceTagRequest{SurfaceID: "view-layout", TagID: schema.DefaultLayoutTagID, LayoutID: schema.BuiltinDevStandardLayoutID, ZoneID: "missing-zone"}, setup: addVisibleLayoutSurface, wantClass: schema.ErrorLayoutZoneNotFound},
		{name: "unsupported mode", request: schema.AssignSurfaceTagRequest{SurfaceID: "view-layout", TagID: schema.DefaultLayoutTagID, LayoutID: schema.BuiltinDevStandardLayoutID, Mode: schema.LayoutModeGrid}, setup: addVisibleLayoutSurface, wantClass: schema.ErrorUnsupportedLayoutMode},
		{name: "invalid geometry", request: schema.AssignSurfaceTagRequest{SurfaceID: "view-layout", TagID: schema.DefaultLayoutTagID, LayoutID: "invalid-layout", ZoneID: "bad"}, setup: func(bridge *Bridge) {
			addVisibleLayoutSurface(bridge)
			bridge.layoutDefinitions["invalid-layout"] = schema.LayoutDefinition{LayoutID: "invalid-layout", Name: "Invalid", Mode: schema.LayoutModeManual, Region: schema.LayoutRegion{RegionID: "main"}, Zones: []schema.LayoutZone{{ZoneID: "bad", Name: "Bad", RelativeGeometry: schema.NormalizedRect{X: 0.9, Y: 0, Width: 0.2, Height: 1}}}}
		}, wantClass: schema.ErrorInvalidLayoutGeometry},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pub := &fakePublisher{}
			bridge, err := New(pub, Config{AllowedPluginUID: uint32(os.Getuid())})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if tc.setup != nil {
				tc.setup(bridge)
			}
			body, err := json.Marshal(tc.request)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			_, err = bridge.dispatch(60002, schema.Request{Method: schema.MethodAssignSurfaceTag, Body: body})
			if err == nil {
				t.Fatalf("expected %s", tc.wantClass)
			}
			class, _ := classifyError(err)
			if class != tc.wantClass {
				t.Fatalf("got class %q (%v), want %q", class, err, tc.wantClass)
			}
			if len(pub.events) == 0 || pub.events[len(pub.events)-1].topic != schema.TopicShellLayoutDenied {
				t.Fatalf("shell layout denial was not published: %+v", pub.events)
			}
		})
	}
}

func TestDispatchAssignSurfaceTagReadbackTimeoutClass(t *testing.T) {
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
	addVisibleLayoutSurface(bridge)
	var discard schema.CompositorPolicyUpsert
	_ = dec.Decode(&discard)
	body, err := json.Marshal(schema.AssignSurfaceTagRequest{SurfaceID: "view-layout", TagID: schema.DefaultLayoutTagID, LayoutID: schema.BuiltinDevStandardLayoutID, ZoneID: "editor", WaitTimeoutMs: 20})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := bridge.dispatch(60002, schema.Request{Method: schema.MethodAssignSurfaceTag, Body: body})
		errCh <- err
	}()
	var msg schema.CompositorPlaceSurface
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode place_surface: %v", err)
	}
	if err := json.NewEncoder(client).Encode(schema.CompositorPlacePluginResponse{Type: string(schema.PluginMessagePlaceResponse), RequestID: msg.RequestID, SurfaceID: "view-layout", OK: true}); err != nil {
		t.Fatalf("encode place response: %v", err)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected readback timeout")
		}
		class, _ := classifyError(err)
		if class != schema.ErrorLayoutReadbackTimeout {
			t.Fatalf("got class %q (%v), want %q", class, err, schema.ErrorLayoutReadbackTimeout)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out waiting for dispatch error")
	}
}

func addVisibleLayoutSurface(bridge *Bridge) {
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{Type: schema.PluginMessageSurfaceEvent, Event: schema.SurfaceEventMapped, Surface: schema.CompositorSurface{ID: "view-layout", WayfireViewID: 60, Visible: boolPtr(true), Geometry: &schema.SurfaceGeometry{X: 40, Y: 50, Width: 800, Height: 600}}, Client: schema.CompositorClientIdentity{UID: 60001}})
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
