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
	case "wait":
		err = cmdWait(args[1:], *pretty)
	case "artifacts":
		err = cmdArtifacts(args[1:], *pretty)
	case "session":
		err = cmdSession(args[1:], *pretty)
	case "launch":
		err = cmdLaunch(args[1:], *pretty)
	case "output":
		err = cmdOutput(args[1:], *pretty)
	case "a11y":
		err = cmdA11y(args[1:], *pretty)
	case "app":
		err = cmdApp(args[1:], *pretty)
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
	case "set-view-property":
		err = cmdSetViewProperty(args[1:], *pretty)
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
  wait               Wait for compositor readiness facts
  artifacts          List, get, or export structured capture artifacts
  session            Create, list, get, reset, or destroy compositor sessions
  launch             Launch a Wayland client and track its process/surfaces
  output             Manage logical output tiles for multi-agent desktops
  a11y               Query or invoke surface accessibility/semantic nodes
  app                Forward typed app testbench commands and read results
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
  set-view-property  Set a tracked surface view property such as always_on_top
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

func cmdWait(args []string, pretty bool) error {
	if len(args) == 0 {
		return fmt.Errorf("wait subcommand is required")
	}
	switch args[0] {
	case "for-surface":
		fs := flag.NewFlagSet("wait for-surface", flag.ExitOnError)
		sessionID := fs.String("session", "", "session id")
		appID := fs.String("app-id", "", "app id substring")
		title := fs.String("title", "", "title substring")
		timeout := fs.Int("timeout", 5000, "timeout milliseconds")
		fs.Parse(args[1:])
		return callAndPrint(schema.MethodWaitForSurface, schema.WaitForSurfaceRequest{SessionID: *sessionID, AppID: *appID, Title: *title, TimeoutMs: *timeout}, pretty)
	case "for-frame":
		fs := flag.NewFlagSet("wait for-frame", flag.ExitOnError)
		surfaceID := fs.String("surface", "", "surface id")
		after := fs.Uint64("after-frame", 0, "wait for frame count greater than this value")
		timeout := fs.Int("timeout", 5000, "timeout milliseconds")
		fs.Parse(args[1:])
		if *surfaceID == "" {
			return fmt.Errorf("--surface is required")
		}
		return callAndPrint(schema.MethodWaitForFrame, schema.WaitForFrameRequest{SurfaceID: *surfaceID, AfterFrame: *after, TimeoutMs: *timeout}, pretty)
	case "for-app-ready":
		fs := flag.NewFlagSet("wait for-app-ready", flag.ExitOnError)
		launchID := fs.String("launch-id", "", "launch id")
		timeout := fs.Int("timeout", 5000, "timeout milliseconds")
		fs.Parse(args[1:])
		if *launchID == "" {
			return fmt.Errorf("--launch-id is required")
		}
		return callAndPrint(schema.MethodWaitForAppReady, schema.WaitForAppReadyRequest{LaunchID: *launchID, TimeoutMs: *timeout}, pretty)
	case "for-render-idle":
		fs := flag.NewFlagSet("wait for-render-idle", flag.ExitOnError)
		surfaceID := fs.String("surface", "", "surface id")
		idle := fs.Int("idle-ms", 250, "required idle milliseconds")
		timeout := fs.Int("timeout", 5000, "timeout milliseconds")
		fs.Parse(args[1:])
		if *surfaceID == "" {
			return fmt.Errorf("--surface is required")
		}
		return callAndPrint(schema.MethodWaitForRenderIdle, schema.WaitForRenderIdleRequest{SurfaceID: *surfaceID, IdleMs: *idle, TimeoutMs: *timeout}, pretty)
	case "for-no-pending":
		fs := flag.NewFlagSet("wait for-no-pending", flag.ExitOnError)
		surfaceID := fs.String("surface", "", "surface id")
		timeout := fs.Int("timeout", 5000, "timeout milliseconds")
		fs.Parse(args[1:])
		if *surfaceID == "" {
			return fmt.Errorf("--surface is required")
		}
		return callAndPrint(schema.MethodWaitForNoPending, schema.WaitForNoPendingRequest{SurfaceID: *surfaceID, TimeoutMs: *timeout}, pretty)
	default:
		return fmt.Errorf("unknown wait subcommand: %s", args[0])
	}
}

func cmdArtifacts(args []string, pretty bool) error {
	if len(args) == 0 {
		return fmt.Errorf("artifacts subcommand is required: list, get, export")
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("artifacts list", flag.ExitOnError)
		sessionID := fs.String("session", "", "session id")
		fs.Parse(args[1:])
		return callAndPrint(schema.MethodListArtifacts, schema.ListArtifactsRequest{SessionID: *sessionID}, pretty)
	case "get":
		fs := flag.NewFlagSet("artifacts get", flag.ExitOnError)
		artifactID := fs.String("artifact", "", "artifact id")
		fs.Parse(args[1:])
		if *artifactID == "" {
			return fmt.Errorf("--artifact is required")
		}
		return callAndPrint(schema.MethodGetArtifact, schema.GetArtifactRequest{ArtifactID: *artifactID}, pretty)
	case "export":
		fs := flag.NewFlagSet("artifacts export", flag.ExitOnError)
		sessionID := fs.String("session", "", "session id")
		to := fs.String("to", "", "destination directory")
		fs.Parse(args[1:])
		if *sessionID == "" || *to == "" {
			return fmt.Errorf("--session and --to are required")
		}
		return callAndPrint(schema.MethodExportArtifacts, schema.ExportArtifactsRequest{SessionID: *sessionID, To: *to}, pretty)
	default:
		return fmt.Errorf("unknown artifacts subcommand: %s", args[0])
	}
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
		agentIdentity := fs.String("agent-identity", "", "agent identity for session audit metadata")
		ashaScenario := fs.String("asha-scenario", "", "ASHA scenario id")
		repoCommit := fs.String("repo-commit", "", "repo commit")
		repoBranch := fs.String("repo-branch", "", "repo branch")
		runtimeMode := fs.String("asha-runtime-mode", "", "ASHA runtime mode")
		artifactRoot := fs.String("artifact-root", "", "artifact root path")
		auditID := fs.String("audit-correlation-id", "", "audit correlation id")
		fs.Parse(args[1:])
		return callAndPrint(schema.MethodCreateSession, schema.CreateSessionRequest{
			Label: *label, ProjectID: *projectID, TaskID: *taskID, AgentIdentity: *agentIdentity, ASHAScenarioID: *ashaScenario,
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

func cmdApp(args []string, pretty bool) error {
	if len(args) == 0 {
		return fmt.Errorf("app subcommand is required")
	}
	switch args[0] {
	case "command":
		fs := flag.NewFlagSet("app command", flag.ExitOnError)
		surfaceID := fs.String("surface", "", "surface id")
		command := fs.String("command", "", "typed command JSON to forward")
		sessionID := fs.String("session", "", "session id")
		sessionToken := fs.String("session-token", os.Getenv("AGORA_COMPOSITOR_SESSION_TOKEN"), "session token (defaults to AGORA_COMPOSITOR_SESSION_TOKEN)")
		auditID := fs.String("audit-correlation-id", "", "audit correlation id")
		timeout := fs.Int("timeout-ms", 3000, "app command timeout milliseconds")
		fs.Parse(args[1:])
		if *surfaceID == "" || *command == "" {
			return fmt.Errorf("--surface and --command are required")
		}
		return callAndPrint(schema.MethodAppCommand, schema.AppCommandRequest{SurfaceID: *surfaceID, Command: json.RawMessage(*command), SessionID: *sessionID, SessionToken: *sessionToken, AuditCorrelationID: *auditID, TimeoutMs: *timeout}, pretty)
	case "result":
		fs := flag.NewFlagSet("app result", flag.ExitOnError)
		requestID := fs.String("request-id", "", "app command request id")
		sessionID := fs.String("session", "", "session id")
		sessionToken := fs.String("session-token", os.Getenv("AGORA_COMPOSITOR_SESSION_TOKEN"), "session token (defaults to AGORA_COMPOSITOR_SESSION_TOKEN)")
		fs.Parse(args[1:])
		if *requestID == "" {
			return fmt.Errorf("--request-id is required")
		}
		return callAndPrint(schema.MethodAppResult, schema.AppResultRequest{RequestID: *requestID, SessionID: *sessionID, SessionToken: *sessionToken}, pretty)
	default:
		return fmt.Errorf("unknown app subcommand %q", args[0])
	}
}

func cmdA11y(args []string, pretty bool) error {
	if len(args) == 0 {
		return fmt.Errorf("a11y subcommand is required")
	}
	switch args[0] {
	case "tree":
		fs := flag.NewFlagSet("a11y tree", flag.ExitOnError)
		surfaceID := fs.String("surface", "", "surface id")
		depth := fs.Int("depth", 8, "maximum tree depth")
		fs.Parse(args[1:])
		if *surfaceID == "" {
			return fmt.Errorf("--surface is required")
		}
		return callAndPrint(schema.MethodA11yTree, schema.A11yTreeRequest{SurfaceID: *surfaceID, Depth: *depth}, pretty)
	case "semantic":
		fs := flag.NewFlagSet("a11y semantic", flag.ExitOnError)
		surfaceID := fs.String("surface", "", "surface id")
		depth := fs.Int("depth", 8, "maximum tree depth")
		fs.Parse(args[1:])
		if *surfaceID == "" {
			return fmt.Errorf("--surface is required")
		}
		return callAndPrint(schema.MethodA11ySemantic, schema.A11yTreeRequest{SurfaceID: *surfaceID, Depth: *depth}, pretty)
	case "find":
		fs := flag.NewFlagSet("a11y find", flag.ExitOnError)
		surfaceID := fs.String("surface", "", "surface id")
		name := fs.String("name", "", "case-insensitive name/role/description pattern")
		depth := fs.Int("depth", 10, "maximum tree depth")
		fs.Parse(args[1:])
		if *surfaceID == "" {
			return fmt.Errorf("--surface is required")
		}
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		return callAndPrint(schema.MethodA11yFind, schema.A11yFindRequest{SurfaceID: *surfaceID, Name: *name, Depth: *depth}, pretty)
	case "click":
		fs := flag.NewFlagSet("a11y click", flag.ExitOnError)
		nodeID := fs.String("node", "", "a11y node id returned by tree/find")
		actionIndex := fs.Int("action-index", 0, "accessible action index to invoke")
		fs.Parse(args[1:])
		if *nodeID == "" {
			return fmt.Errorf("--node is required")
		}
		return callAndPrint(schema.MethodA11yClick, schema.A11yClickRequest{NodeID: *nodeID, ActionIndex: *actionIndex}, pretty)
	default:
		return fmt.Errorf("unknown a11y subcommand %q", args[0])
	}
}

func cmdOutput(args []string, pretty bool) error {
	if len(args) == 0 {
		return fmt.Errorf("output subcommand is required")
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("output create", flag.ExitOnError)
		name := fs.String("name", "", "logical output name")
		width := fs.Int("width", 1280, "logical output width")
		height := fs.Int("height", 720, "logical output height")
		scale := fs.Float64("scale", 1, "logical output scale")
		fs.Parse(args[1:])
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		return callAndPrint(schema.MethodCreateOutput, schema.CreateOutputRequest{Name: *name, Width: *width, Height: *height, Scale: *scale}, pretty)
	case "destroy":
		fs := flag.NewFlagSet("output destroy", flag.ExitOnError)
		name := fs.String("name", "", "logical output name")
		fs.Parse(args[1:])
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		return callAndPrint(schema.MethodDestroyOutput, schema.OutputRequest{Name: *name}, pretty)
	case "resize":
		fs := flag.NewFlagSet("output resize", flag.ExitOnError)
		name := fs.String("name", "", "logical output name")
		width := fs.Int("width", 0, "logical output width")
		height := fs.Int("height", 0, "logical output height")
		fs.Parse(args[1:])
		if *name == "" || *width <= 0 || *height <= 0 {
			return fmt.Errorf("--name, --width, and --height are required")
		}
		return callAndPrint(schema.MethodResizeOutput, schema.ResizeOutputRequest{Name: *name, Width: *width, Height: *height}, pretty)
	case "set-scale":
		fs := flag.NewFlagSet("output set-scale", flag.ExitOnError)
		name := fs.String("name", "", "logical output name")
		scale := fs.Float64("scale", 1, "logical output scale")
		fs.Parse(args[1:])
		if *name == "" || *scale <= 0 {
			return fmt.Errorf("--name and positive --scale are required")
		}
		return callAndPrint(schema.MethodSetOutputScale, schema.SetOutputScaleRequest{Name: *name, Scale: *scale}, pretty)
	case "list":
		return callAndPrint(schema.MethodListOutputs, map[string]string{}, pretty)
	case "move-surface":
		fs := flag.NewFlagSet("output move-surface", flag.ExitOnError)
		surfaceID := fs.String("surface", "", "surface id")
		output := fs.String("output", "", "logical output name")
		fs.Parse(args[1:])
		if *surfaceID == "" || *output == "" {
			return fmt.Errorf("--surface and --output are required")
		}
		return callAndPrint(schema.MethodMoveSurfaceToOutput, schema.MoveSurfaceToOutputRequest{SurfaceID: *surfaceID, Output: *output}, pretty)
	case "surface-list":
		fs := flag.NewFlagSet("output surface-list", flag.ExitOnError)
		name := fs.String("name", "", "logical output name")
		fs.Parse(args[1:])
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		return callAndPrint(schema.MethodListOutputSurfaces, schema.OutputRequest{Name: *name}, pretty)
	case "capture":
		fs := flag.NewFlagSet("output capture", flag.ExitOnError)
		name := fs.String("name", "", "logical output name")
		exportArtifact := fs.Bool("export", false, "write structured artifacts for captured surfaces")
		sessionID := fs.String("session", "", "session id for artifact export")
		sessionToken := fs.String("session-token", os.Getenv("AGORA_COMPOSITOR_SESSION_TOKEN"), "session token (defaults to AGORA_COMPOSITOR_SESSION_TOKEN)")
		auditID := fs.String("audit-correlation-id", "", "audit correlation id")
		evidenceClass := fs.String("evidence-class", "viewport_screenshot", "evidence class")
		seqID := fs.String("asha-command-sequence-id", "", "ASHA command sequence id")
		fs.Parse(args[1:])
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		return callAndPrint(schema.MethodCaptureOutput, schema.CaptureOutputRequest{Name: *name, Export: *exportArtifact, SessionID: *sessionID, SessionToken: *sessionToken, AuditCorrelationID: *auditID, EvidenceClass: *evidenceClass, ASHACommandSequenceID: *seqID}, pretty)
	default:
		return fmt.Errorf("unknown output subcommand: %s", args[0])
	}
}

func validLaunchRole(role string) bool {
	switch role {
	case "toplevel", "panel", "dock", "background", "overlay":
		return true
	default:
		return false
	}
}

func normalizeLaunchRole(role string) (string, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		return "toplevel", nil
	}
	if !validLaunchRole(role) {
		return "", fmt.Errorf("invalid --role %q (valid values: toplevel, panel, dock, background, overlay)", role)
	}
	return role, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func launchCommandFromFlags(command, rawURL string) (string, error) {
	command = strings.TrimSpace(command)
	rawURL = strings.TrimSpace(rawURL)
	switch {
	case command != "" && rawURL != "":
		return "", fmt.Errorf("--cmd and --url are mutually exclusive")
	case command != "":
		return command, nil
	case rawURL != "":
		return "webview-launcher --url " + shellQuote(rawURL), nil
	default:
		return "", fmt.Errorf("either --cmd or --url is required")
	}
}

func buildLaunchRequest(args []string) (schema.LaunchAppRequest, error) {
	fs := flag.NewFlagSet("launch", flag.ExitOnError)
	cmd := fs.String("cmd", "", "command to launch")
	url := fs.String("url", "", "remote URL to open with webview-launcher")
	sessionID := fs.String("session", "", "session id")
	sessionToken := fs.String("session-token", os.Getenv("AGORA_COMPOSITOR_SESSION_TOKEN"), "session token (defaults to AGORA_COMPOSITOR_SESSION_TOKEN)")
	auditID := fs.String("audit-correlation-id", "", "audit correlation id")
	cwd := fs.String("cwd", "", "working directory")
	runAsUID := fs.Uint("uid", 0, "run process as UID (bridge must have permission; default bridge policy may choose agent)")
	runAsGID := fs.Uint("gid", 0, "run process as GID (bridge must have permission; default bridge policy may choose agent)")
	expectedAppID := fs.String("expected-app-id", "", "expected compositor app id")
	expectedTitle := fs.String("expected-title", "", "expected surface title substring")
	role := fs.String("role", "toplevel", "webview role: toplevel, panel, dock, background, overlay")
	output := fs.String("output", "", "logical output name to place the launched surface into")
	waitSurface := fs.Bool("wait-surface", false, "wait for first matching surface")
	waitTimeout := fs.Int("wait-timeout-ms", 5000, "surface wait timeout in milliseconds")
	env := envFlags{}
	fs.Var(&env, "env", "environment variable KEY=VALUE; may be repeated")
	fs.Parse(args)
	launchRole, err := normalizeLaunchRole(*role)
	if err != nil {
		return schema.LaunchAppRequest{}, err
	}
	command, err := launchCommandFromFlags(*cmd, *url)
	if err != nil {
		return schema.LaunchAppRequest{}, err
	}
	req := schema.LaunchAppRequest{
		SessionID: *sessionID, SessionToken: *sessionToken, AuditCorrelationID: *auditID, Command: command, Cwd: *cwd, Env: env, ExpectedAppID: *expectedAppID,
		ExpectedTitle: *expectedTitle, Role: launchRole, Output: *output, WaitSurface: *waitSurface, WaitTimeoutMs: *waitTimeout,
	}
	if *runAsUID != 0 {
		uid := uint32(*runAsUID)
		req.RunAsUID = &uid
	}
	if *runAsGID != 0 {
		gid := uint32(*runAsGID)
		req.RunAsGID = &gid
	}
	return req, nil
}

func cmdLaunch(args []string, pretty bool) error {
	req, err := buildLaunchRequest(args)
	if err != nil {
		return err
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
	sessionToken := fs.String("session-token", os.Getenv("AGORA_COMPOSITOR_SESSION_TOKEN"), "session token (defaults to AGORA_COMPOSITOR_SESSION_TOKEN)")
	fs.Parse(args)
	if *launchID == "" {
		return fmt.Errorf("--launch-id is required")
	}
	return callAndPrint(schema.MethodTerminateLaunch, schema.TerminateLaunchRequest{LaunchID: *launchID, SessionToken: *sessionToken}, pretty)
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
	exportArtifact := fs.Bool("export", false, "write structured artifact index")
	sessionID := fs.String("session", "", "session id for artifact export")
	sessionToken := fs.String("session-token", os.Getenv("AGORA_COMPOSITOR_SESSION_TOKEN"), "session token (defaults to AGORA_COMPOSITOR_SESSION_TOKEN)")
	auditID := fs.String("audit-correlation-id", "", "audit correlation id")
	evidenceClass := fs.String("evidence-class", "", "evidence class: surface_screenshot, viewport_screenshot, or desktop_behavior")
	seqID := fs.String("asha-command-sequence-id", "", "ASHA command sequence id")
	fs.Parse(args)

	if *surfaceID == "" {
		return fmt.Errorf("--surface is required")
	}
	if *format != "png" {
		return fmt.Errorf("only --format png is supported")
	}

	resp, err := call(compositorSock, schema.MethodCaptureSurface, schema.CaptureSurfaceRequest{
		SurfaceID:             *surfaceID,
		Format:                *format,
		Export:                *exportArtifact,
		SessionID:             *sessionID,
		SessionToken:          *sessionToken,
		AuditCorrelationID:    *auditID,
		EvidenceClass:         *evidenceClass,
		ASHACommandSequenceID: *seqID,
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
	sessionID := fs.String("session", "", "session id")
	sessionToken := fs.String("session-token", os.Getenv("AGORA_COMPOSITOR_SESSION_TOKEN"), "session token (defaults to AGORA_COMPOSITOR_SESSION_TOKEN)")
	fs.Parse(args)
	if *surfaceID == "" {
		return fmt.Errorf("--surface is required")
	}
	return sendInput(*surfaceID, *sessionID, *sessionToken, []schema.CompositorInputEvent{{Type: "pointer_move", X: *x, Y: *y}}, pretty)
}

func cmdClick(args []string, pretty bool) error {
	fs := flag.NewFlagSet("click", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "surface ID (required)")
	x := fs.Float64("x", 0, "surface-local x coordinate")
	y := fs.Float64("y", 0, "surface-local y coordinate")
	button := fs.Uint("button", 0x110, "linux input button code (default BTN_LEFT)")
	sessionID := fs.String("session", "", "session id")
	sessionToken := fs.String("session-token", os.Getenv("AGORA_COMPOSITOR_SESSION_TOKEN"), "session token (defaults to AGORA_COMPOSITOR_SESSION_TOKEN)")
	fs.Parse(args)
	if *surfaceID == "" {
		return fmt.Errorf("--surface is required")
	}
	events := []schema.CompositorInputEvent{
		{Type: "pointer_move", X: *x, Y: *y},
		{Type: "pointer_button", X: *x, Y: *y, Button: uint32(*button), State: "pressed"},
		{Type: "pointer_button", X: *x, Y: *y, Button: uint32(*button), State: "released"},
	}
	return sendInput(*surfaceID, *sessionID, *sessionToken, events, pretty)
}

func cmdKey(args []string, pretty bool) error {
	fs := flag.NewFlagSet("key", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "surface ID (required)")
	key := fs.String("key", "", "key name or linux input keycode")
	sessionID := fs.String("session", "", "session id")
	sessionToken := fs.String("session-token", os.Getenv("AGORA_COMPOSITOR_SESSION_TOKEN"), "session token (defaults to AGORA_COMPOSITOR_SESSION_TOKEN)")
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
	return sendInput(*surfaceID, *sessionID, *sessionToken, events, pretty)
}

func cmdType(args []string, pretty bool) error {
	fs := flag.NewFlagSet("type", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "surface ID (required)")
	text := fs.String("text", "", "ASCII text to type")
	sessionID := fs.String("session", "", "session id")
	sessionToken := fs.String("session-token", os.Getenv("AGORA_COMPOSITOR_SESSION_TOKEN"), "session token (defaults to AGORA_COMPOSITOR_SESSION_TOKEN)")
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
	return sendInput(*surfaceID, *sessionID, *sessionToken, events, pretty)
}

func sendInput(surfaceID, sessionID, sessionToken string, events []schema.CompositorInputEvent, pretty bool) error {
	resp, err := call(compositorSock, schema.MethodInjectInput, schema.InjectInputRequest{
		SurfaceID:       surfaceID,
		SessionID:       sessionID,
		SessionToken:    sessionToken,
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

func cmdSetViewProperty(args []string, pretty bool) error {
	req, err := buildSetViewPropertyRequest(args)
	if err != nil {
		return err
	}
	resp, err := call(compositorSock, schema.MethodSetViewProperty, req)
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

func buildSetViewPropertyRequest(args []string) (schema.SetViewPropertyRequest, error) {
	fs := flag.NewFlagSet("set-view-property", flag.ExitOnError)
	surfaceID := fs.String("surface", "", "tracked surface id (required)")
	alwaysOnTop := fs.String("always-on-top", "", "set always_on_top to true or false")
	fs.Parse(args)

	if *surfaceID == "" {
		return schema.SetViewPropertyRequest{}, fmt.Errorf("--surface is required")
	}
	properties := make(map[string]any)
	if *alwaysOnTop != "" {
		switch strings.ToLower(*alwaysOnTop) {
		case "true", "1", "yes", "on":
			properties["always_on_top"] = true
		case "false", "0", "no", "off":
			properties["always_on_top"] = false
		default:
			return schema.SetViewPropertyRequest{}, fmt.Errorf("--always-on-top must be true or false")
		}
	}
	if len(properties) == 0 {
		return schema.SetViewPropertyRequest{}, fmt.Errorf("at least one property flag is required")
	}
	return schema.SetViewPropertyRequest{SurfaceID: *surfaceID, Properties: properties}, nil
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
		if resp.ErrorClass != "" {
			return nil, fmt.Errorf("server[%s]: %s", resp.ErrorClass, resp.ErrorMessage)
		}
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
