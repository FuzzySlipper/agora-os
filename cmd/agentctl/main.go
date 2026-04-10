// agentctl is the CLI client for Agora OS services.
// It speaks the {method, body} JSON envelope over Unix sockets.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/patch/agora-os/internal/schema"
)

const (
	isolationSock = "/run/agent-os/isolation.sock"
	adminSock     = "/run/agent-os/admin-agent.sock"
)

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
	case "spawn":
		err = cmdSpawn(args[1:], *pretty)
	case "list":
		err = cmdList(*pretty)
	case "terminate":
		err = cmdTerminate(args[1:], *pretty)
	case "escalate":
		err = cmdEscalate(args[1:], *pretty)
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
	fmt.Fprintf(os.Stderr, `Usage: agentctl [--pretty] <command> [flags]

Commands:
  spawn       Create and start an agent
  list        List active agents
  terminate   Stop and remove an agent by uid
  escalate    Submit an escalation request to the admin agent

Run agentctl <command> --help for command-specific flags.
`)
}

// --- spawn ---

func cmdSpawn(args []string, pretty bool) error {
	fs := flag.NewFlagSet("spawn", flag.ExitOnError)
	name := fs.String("name", "", "agent name (required)")
	cpu := fs.String("cpu", "", "CPU quota, e.g. 50%")
	mem := fs.String("mem", "", "memory limit, e.g. 512M")
	netPolicy := fs.String("net", "", "network policy: deny, local_only, allow")
	fs.Parse(args)

	if *name == "" {
		return fmt.Errorf("--name is required")
	}

	req := schema.SpawnAgentRequest{
		Name:      *name,
		CPUQuota:  *cpu,
		MemoryMax: *mem,
		NetAccess: schema.NetPolicy(*netPolicy),
		Command:   fs.Args(), // anything after flags / "--" becomes the command
	}
	if len(req.Command) == 0 {
		req.Command = nil
	}

	resp, err := call(isolationSock, "spawn_agent", req)
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

// --- list ---

func cmdList(pretty bool) error {
	resp, err := call(isolationSock, "list_agents", nil)
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

// --- terminate ---

func cmdTerminate(args []string, pretty bool) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: agentctl terminate <uid>")
	}
	uid, err := strconv.ParseUint(args[0], 10, 32)
	if err != nil {
		return fmt.Errorf("invalid uid: %w", err)
	}

	resp, err := call(isolationSock, "terminate_agent", schema.TerminateAgentRequest{UID: uint32(uid)})
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

// --- escalate ---

func cmdEscalate(args []string, pretty bool) error {
	fs := flag.NewFlagSet("escalate", flag.ExitOnError)
	uid := fs.Uint("uid", 0, "agent UID (required)")
	action := fs.String("action", "", "requested action (required)")
	resource := fs.String("resource", "", "requested resource (required)")
	justification := fs.String("justification", "", "justification (required)")
	taskCtx := fs.String("context", "", "task context")
	fs.Parse(args)

	if *uid == 0 || *action == "" || *resource == "" || *justification == "" {
		return fmt.Errorf("--uid, --action, --resource, and --justification are all required")
	}

	req := schema.EscalationRequest{
		AgentUID:          uint32(*uid),
		TaskContext:       *taskCtx,
		RequestedAction:   *action,
		RequestedResource: *resource,
		Justification:     *justification,
	}

	resp, err := call(adminSock, "escalate", req)
	if err != nil {
		return err
	}
	return printJSON(resp, pretty)
}

// --- transport ---

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

// --- output ---

func printJSON(data json.RawMessage, pretty bool) error {
	if pretty {
		var v any
		json.Unmarshal(data, &v)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	_, err := fmt.Printf("%s\n", data)
	return err
}
