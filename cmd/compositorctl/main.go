package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/patch/agora-os/internal/schema"
)

const compositorSock = schema.CompositorControlSocket

func main() {
	pretty := flag.Bool("pretty", false, "human-readable indented JSON output")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	var err error
	switch args[0] {
	case "capture":
		err = cmdCapture(args[1:], *pretty)
	case "session":
		err = cmdSession(args[1:], *pretty)
	case "launch":
		err = cmdLaunch(args[1:], *pretty)
	case "list-processes":
		err = cmdListProcesses(args[1:], *pretty)
	case "terminate":
		err = cmdTerminate(args[1:], *pretty)
	case "move":
		err = cmdMove(args[1:], *pretty)
	case "click":
		err = cmdClick(args[1:], *pretty)
	case "key":
		err = cmdKey(args[1:], *pretty)
	case "type":
		err = cmdType(args[1:], *pretty)
	case "grant-viewport":
		err = cmdGrantViewport(args[1:], *pretty)
	case "revoke-viewport":
		err = cmdRevokeViewport(args[1:], *pretty)
	case "check-access":
		err = cmdCheckAccess(args[1:], *pretty)
	case "set-input-context":
		err = cmdSetInputContext(args[1:], *pretty)
	case "clear-input-context":
		err = cmdClearInputContext(*pretty)
	case "list-surfaces":
		err = cmdListSurfaces(*pretty)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: compositorctl [--pretty] <command> [flags]

Commands:
  capture            Capture a tracked surface to a PNG artifact
  session            Create, list, get, reset, or destroy compositor sessions
  launch             Launch a Wayland client and track its process/surfaces
  list-processes     List tracked compositor-launched processes
  terminate          Terminate a tracked launch and close its surfaces
  move               Move pointer over a tracked surface
  click              Send a pointer click to a tracked surface
  key                Send a key press/release pair to a tracked surface
  type               Type ASCII text into a tracked surface
  grant-viewport     Record an explicit viewport grant for an agent on a surface
  revoke-viewport    Revoke a previously granted viewport
  check-access       Ask the compositor bridge whether an agent may access a surface
  set-input-context  Mark the current compositor input stream as driven by an agent uid
  clear-input-context  Return the compositor input stream to human mode
  list-surfaces      List tracked compositor surfaces

Run compositorctl <command> --help for command-specific flags.
`)
}

type envFlags map[string]string

func (e *envFlags) String() string {
	if e == nil {
		return ""
	}
	parts := make([]string, 0, len(*e))
	for k, v := range *e {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func (e *envFlags) Set(value string) error {
	if *e == nil {
		*e = make(map[string]string)
	}
	key, val, ok := strings.Cut(value, "=")
	if !ok || key == "" {
		return fmt.Errorf("env must be KEY=VALUE")
	}
	(*e)[key] = val
	return nil
}

func cmdSession(args []string, pretty bool) error {
	if len(args) == 0 {
		return fmt.Errorf("session subcommand is required: create, list, get, reset, destroy")
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("session create", flag.ExitOnError)
		label := fs.String("label", "", "session label")
		projectID := fs.String("project-id", "", "Den project id")
		taskID := fs.Int("task-id", 0, "Den task id")
		ashaScenario := fs.String("asha-scenario", "", "ASHA scenario id")
		repoCommit := fs.String("repo-commit", "", "repo commit")
		repoBranch := fs.String("repo-branch", "", "repo branch")
		runtimeMode := fs.String("asha-runtime-mode", "", "ASHA runtime mode")
		artifactRoot := fs.String("artifact-root", "", "artifact root path")
		auditID := fs.String("audit-correlation-id", "", "audit correlation id")
		fs.Parse(args[1:])
		return callAndPrint(schema.MethodCreateSession, schema.CreateSessionRequest{
			Label: *label, ProjectID: *projectID, TaskID: *taskID, ASHAScenarioID: *ashaScenario,
			RepoCommit: *repoCommit, RepoBranch: *repoBranch, ASHARuntimeMode: *runtimeMode,
			ArtifactRoot: *artifactRoot, AuditCorrelationID: *auditID,
		}, pretty)
	case "list":
		return callAndPrint(schema.MethodListSessions, nil, pretty)
	case "get", "reset", "destroy":
		fs := flag.NewFlagSet("session "+args[0], flag.ExitOnError)
		sessionID := fs.String("session", "", "session id (required)")
		fs.Parse(args[1:])
		if *sessionID == "" {
			return fmt.Errorf("--session is required")
		}
		method := map[string]string{"get": schema.MethodGetSession, "reset": schema.MethodResetSession, "destroy": schema.MethodDestroySession}[args[0]]
		return callAndPrint(method, schema.SessionRequest{SessionID: *sessionID}, pretty)
	default:
		return fmt.Errorf("unknown session subcommand: %s", args[0])
	}
}

func cmdLaunch(args []string, pretty bool) error {
	fs := flag.NewFlagSet("launch", flag.ExitOnError)
	cmd := fs.String("cmd", "", "command to launch (required)")
	sessionID := fs.String("session", "", "session id")
	cwd := fs.String("cwd", "", "working directory")
	runAsUID := fs.Uint("uid", 0, "run process as UID (bridge must have permission; default bridge policy may choose agent)")
	runAsGID := fs.Uint("gid", 0, "run process as GID (bridge must have permission; default bridge policy may choose agent)")
	expectedAppID := fs.String("expected-app-id", "", "expected compositor app id")
	expectedTitle := fs.String("expected-title", "", "expected surface title substring")
	waitSurface := fs.Bool("wait-surface", false, "wait for first matching surface")
	waitTimeout := fs.Int("wait-timeout-ms", 5000, "surface wait timeout in milliseconds")
	env := envFlags{}
	fs.Var(&env, "env", "environment variable KEY=VALUE; may be repeated")
	fs.Parse(args)
	if *cmd == "" {
		return fmt.Errorf("--cmd is required")
	}
	req := schema.LaunchAppRequest{
		SessionID: *sessionID, Command: *cmd, Cwd: *cwd, Env: env, ExpectedAppID: *expectedAppID,
		ExpectedTitle: *expectedTitle, WaitSurface: *waitSurface, WaitTimeoutMs: *waitTimeout,
	}
	if *runAsUID != 0 {
		uid := uint32(*runAsUID)
		req.RunAsUID = &uid
	}
	if *runAsGID != 0 {
		gid := uint32(*runAsGID)
		req.RunAsGID = &gid
	}
	return callAndPrint(schema.MethodLaunchApp, req, pretty)
}

func cmdListProcesses(args []string, pretty bool) error {
	fs := flag.NewFlagSet("list-processes", flag.ExitOnError)
	sessionID := fs.String("session", "", "session id")
	fs.Parse(args)
	return callAndPrint(schema.MethodListProcesses, schema.ListProcessesRequest{SessionID: *sessionID}, pretty)
}

func cmdTerminate(args []string, pretty bool) error {
	fs := flag.NewFlagSet("terminate", flag.ExitOnError)
	launchID := fs.String("launch-id", "", "launch id (required)")
	fs.Parse(args)
	if *launchID == "" {
		return fmt.Errorf("--launch-id is required")
	}
	return callAndPrint(schema.MethodTerminateLaunch, schema.TerminateLaunchRequest{LaunchID: *launchID}, pretty)
}

func callAndPrint(method string, body any, pretty bool) error {
	resp, err := call(compositorSock, method, body)
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

func cmdCapture(args []string, pretty bool) error {
	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "surface ID (required)")
	format := fs.String("format", "png", "capture format")
	fs.Parse(args)

	if *surfaceID == "" {
		return fmt.Errorf("--surface is required")
	}
	if *format != "png" {
		return fmt.Errorf("only --format png is supported")
	}

	resp, err := call(compositorSock, schema.MethodCaptureSurface, schema.CaptureSurfaceRequest{
		SurfaceID: *surfaceID,
		Format:    *format,
	})
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

func cmdMove(args []string, pretty bool) error {
	fs := flag.NewFlagSet("move", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "surface ID (required)")
	x := fs.Float64("x", 0, "surface-local x coordinate")
	y := fs.Float64("y", 0, "surface-local y coordinate")
	fs.Parse(args)
	if *surfaceID == "" {
		return fmt.Errorf("--surface is required")
	}
	return sendInput(*surfaceID, []schema.CompositorInputEvent{{Type: "pointer_move", X: *x, Y: *y}}, pretty)
}

func cmdClick(args []string, pretty bool) error {
	fs := flag.NewFlagSet("click", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "surface ID (required)")
	x := fs.Float64("x", 0, "surface-local x coordinate")
	y := fs.Float64("y", 0, "surface-local y coordinate")
	button := fs.Uint("button", 0x110, "linux input button code (default BTN_LEFT)")
	fs.Parse(args)
	if *surfaceID == "" {
		return fmt.Errorf("--surface is required")
	}
	events := []schema.CompositorInputEvent{
		{Type: "pointer_move", X: *x, Y: *y},
		{Type: "pointer_button", X: *x, Y: *y, Button: uint32(*button), State: "pressed"},
		{Type: "pointer_button", X: *x, Y: *y, Button: uint32(*button), State: "released"},
	}
	return sendInput(*surfaceID, events, pretty)
}

func cmdKey(args []string, pretty bool) error {
	fs := flag.NewFlagSet("key", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "surface ID (required)")
	key := fs.String("key", "", "key name or linux input keycode")
	fs.Parse(args)
	if *surfaceID == "" || *key == "" {
		return fmt.Errorf("--surface and --key are required")
	}
	keycode, err := keycodeFor(*key)
	if err != nil {
		return err
	}
	events := []schema.CompositorInputEvent{
		{Type: "key", Keycode: keycode, State: "pressed"},
		{Type: "key", Keycode: keycode, State: "released"},
	}
	return sendInput(*surfaceID, events, pretty)
}

func cmdType(args []string, pretty bool) error {
	fs := flag.NewFlagSet("type", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "surface ID (required)")
	text := fs.String("text", "", "ASCII text to type")
	fs.Parse(args)
	if *surfaceID == "" {
		return fmt.Errorf("--surface is required")
	}
	events := make([]schema.CompositorInputEvent, 0, len(*text)*2)
	for _, ch := range *text {
		keycode, err := keycodeForRune(ch)
		if err != nil {
			return err
		}
		events = append(events,
			schema.CompositorInputEvent{Type: "key", Keycode: keycode, State: "pressed"},
			schema.CompositorInputEvent{Type: "key", Keycode: keycode, State: "released"},
		)
	}
	return sendInput(*surfaceID, events, pretty)
}

func sendInput(surfaceID string, events []schema.CompositorInputEvent, pretty bool) error {
	resp, err := call(compositorSock, schema.MethodInjectInput, schema.InjectInputRequest{
		SurfaceID:       surfaceID,
		CoordinateSpace: "surface",
		Events:          events,
	})
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

func cmdGrantViewport(args []string, pretty bool) error {
	fs := flag.NewFlagSet("grant-viewport", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "surface ID (required)")
	agentUID := fs.Uint("agent-uid", 0, "agent UID (required)")
	actionsRaw := fs.String("actions", "pointer,keyboard,read_pixels", "comma-separated actions")
	fs.Parse(args)

	if *surfaceID == "" || *agentUID == 0 {
		return fmt.Errorf("--surface and --agent-uid are required")
	}

	resp, err := call(compositorSock, schema.MethodGrantViewport, schema.ViewportGrantRequest{
		SurfaceID: *surfaceID,
		AgentUID:  uint32(*agentUID),
		Actions:   parseActions(*actionsRaw),
	})
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

func cmdRevokeViewport(args []string, pretty bool) error {
	fs := flag.NewFlagSet("revoke-viewport", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "surface ID (required)")
	agentUID := fs.Uint("agent-uid", 0, "agent UID (required)")
	fs.Parse(args)

	if *surfaceID == "" || *agentUID == 0 {
		return fmt.Errorf("--surface and --agent-uid are required")
	}

	resp, err := call(compositorSock, schema.MethodRevokeViewport, schema.RevokeViewportGrantRequest{
		SurfaceID: *surfaceID,
		AgentUID:  uint32(*agentUID),
	})
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

func cmdCheckAccess(args []string, pretty bool) error {
	fs := flag.NewFlagSet("check-access", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "surface ID (required)")
	agentUID := fs.Uint("agent-uid", 0, "agent UID (required)")
	action := fs.String("action", "read_pixels", "access action: pointer, keyboard, or read_pixels")
	fs.Parse(args)

	if *surfaceID == "" || *agentUID == 0 || *action == "" {
		return fmt.Errorf("--surface, --agent-uid, and --action are required")
	}

	resp, err := call(compositorSock, schema.MethodCheckSurfaceAccess, schema.SurfaceAccessCheckRequest{
		SurfaceID: *surfaceID,
		AgentUID:  uint32(*agentUID),
		Action:    schema.CompositorAccessAction(*action),
	})
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

func cmdSetInputContext(args []string, pretty bool) error {
	fs := flag.NewFlagSet("set-input-context", flag.ExitOnError)
	agentUID := fs.Uint("agent-uid", 0, "agent UID driving input (required)")
	fs.Parse(args)

	if *agentUID == 0 {
		return fmt.Errorf("--agent-uid is required")
	}

	resp, err := call(compositorSock, schema.MethodSetInputContext, schema.SetInputContextRequest{
		ActorUID: uint32Ptr(uint32(*agentUID)),
	})
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

func cmdClearInputContext(pretty bool) error {
	resp, err := call(compositorSock, schema.MethodSetInputContext, schema.SetInputContextRequest{})
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

func cmdListSurfaces(pretty bool) error {
	resp, err := call(compositorSock, schema.MethodListSurfaces, nil)
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

func keycodeFor(raw string) (uint32, error) {
	if raw == "" {
		return 0, fmt.Errorf("empty key")
	}
	var numeric uint32
	if _, err := fmt.Sscanf(raw, "%d", &numeric); err == nil && numeric != 0 {
		return numeric, nil
	}
	lower := strings.ToLower(raw)
	if len(lower) == 1 {
		return keycodeForRune(rune(lower[0]))
	}
	keycodes := map[string]uint32{
		"return": 28, "enter": 28, "space": 57, "tab": 15, "escape": 1, "esc": 1,
		"backspace": 14, "delete": 111, "left": 105, "right": 106, "up": 103, "down": 108,
	}
	if code, ok := keycodes[lower]; ok {
		return code, nil
	}
	return 0, fmt.Errorf("unsupported key %q", raw)
}

func keycodeForRune(ch rune) (uint32, error) {
	if ch >= 'A' && ch <= 'Z' {
		ch += 'a' - 'A'
	}
	letters := map[rune]uint32{
		'a': 30, 'b': 48, 'c': 46, 'd': 32, 'e': 18, 'f': 33, 'g': 34,
		'h': 35, 'i': 23, 'j': 36, 'k': 37, 'l': 38, 'm': 50, 'n': 49,
		'o': 24, 'p': 25, 'q': 16, 'r': 19, 's': 31, 't': 20, 'u': 22,
		'v': 47, 'w': 17, 'x': 45, 'y': 21, 'z': 44,
		' ': 57, '\n': 28, '\t': 15,
		'1': 2, '2': 3, '3': 4, '4': 5, '5': 6, '6': 7, '7': 8, '8': 9, '9': 10, '0': 11,
	}
	if code, ok := letters[ch]; ok {
		return code, nil
	}
	return 0, fmt.Errorf("unsupported character %q", ch)
}

func parseActions(raw string) []schema.CompositorAccessAction {
	parts := strings.Split(raw, ",")
	actions := make([]schema.CompositorAccessAction, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		actions = append(actions, schema.CompositorAccessAction(part))
	}
	return actions
}

func call(sock, method string, body any) (json.RawMessage, error) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", sock, err)
	}
	defer conn.Close()

	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req := schema.Request{Method: method, Body: b}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	var resp schema.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("recv: %w", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("server: %s", string(resp.Body))
	}
	return resp.Body, nil
}

func printJSON(data json.RawMessage, pretty bool) error {
	if pretty {
		var v any
		_ = json.Unmarshal(data, &v)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	_, err := fmt.Printf("%s\n", data)
	return err
}

func uint32Ptr(value uint32) *uint32 {
	return &value
}
