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
  grant-viewport     Record an explicit viewport grant for an agent on a surface
  revoke-viewport    Revoke a previously granted viewport
  check-access       Ask the compositor bridge whether an agent may access a surface
  set-input-context  Mark the current compositor input stream as driven by an agent uid
  clear-input-context  Return the compositor input stream to human mode
  list-surfaces      List tracked compositor surfaces

Run compositorctl <command> --help for command-specific flags.
`)
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
