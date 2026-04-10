// Isolation service: manages agent Linux users, cgroups, and network rules.
// Listens on a Unix socket for spawn/terminate/list commands.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/patch/agent-os/internal/agent"
	"github.com/patch/agent-os/internal/schema"
)

const socketPath = "/run/agent-os/isolation.sock"

func main() {
	// Set up nftables table + chains before accepting any requests.
	if err := agent.BootstrapNftables(); err != nil {
		log.Fatalf("nft bootstrap: %v", err)
	}

	mgr := agent.NewManager()

	// Ensure the socket directory exists
	os.MkdirAll("/run/agent-os", 0755)
	os.Remove(socketPath) // clean up stale socket

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Allow agent uids to connect
	os.Chmod(socketPath, 0666)

	log.Printf("isolation service listening on %s", socketPath)

	// Graceful shutdown
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down")
		ln.Close()
		os.Exit(0)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			return
		}
		go handleConn(conn, mgr)
	}
}

func handleConn(conn net.Conn, mgr *agent.Manager) {
	defer conn.Close()

	var req schema.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeError(conn, fmt.Sprintf("decode: %v", err))
		return
	}

	var resp schema.Response
	switch req.Method {
	case "spawn_agent":
		var body schema.SpawnAgentRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			writeError(conn, fmt.Sprintf("bad body: %v", err))
			return
		}
		info, err := mgr.Spawn(body)
		if err != nil {
			writeError(conn, err.Error())
			return
		}
		resp = okResponse(schema.SpawnAgentResponse{Agent: *info})

	case "terminate_agent":
		var body schema.TerminateAgentRequest
		if err := json.Unmarshal(req.Body, &body); err != nil {
			writeError(conn, fmt.Sprintf("bad body: %v", err))
			return
		}
		if err := mgr.Terminate(body.UID); err != nil {
			writeError(conn, err.Error())
			return
		}
		resp = okResponse("terminated")

	case "list_agents":
		resp = okResponse(schema.ListAgentsResponse{Agents: mgr.List()})

	default:
		writeError(conn, fmt.Sprintf("unknown method: %s", req.Method))
		return
	}

	json.NewEncoder(conn).Encode(resp)
}

func okResponse(body any) schema.Response {
	b, _ := json.Marshal(body)
	return schema.Response{OK: true, Body: b}
}

func writeError(conn net.Conn, msg string) {
	b, _ := json.Marshal(msg)
	json.NewEncoder(conn).Encode(schema.Response{OK: false, Body: b})
}
