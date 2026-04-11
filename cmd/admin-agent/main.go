// Admin agent daemon: stateless privilege escalation evaluator.
// This file is the composition root — config loading, socket setup, and
// signal handling. All request logic lives in internal/admin.
package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/patch/agora-os/internal/admin"
	"github.com/patch/agora-os/internal/schema"
)

const socketPath = schema.AdminSocket

func main() {
	promptPath := "config/admin-agent-system-prompt.md"
	if p := os.Getenv("ADMIN_AGENT_PROMPT"); p != "" {
		promptPath = p
	}

	promptBytes, err := os.ReadFile(promptPath)
	if err != nil {
		log.Fatalf("load system prompt: %v", err)
	}

	logFile, err := os.OpenFile("/var/log/agent-os/admin-agent.log",
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatalf("open log: %v", err)
	}
	defer logFile.Close()

	agent := admin.New(admin.Config{
		SystemPrompt: string(promptBytes),
		APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
		APIURL:       os.Getenv("ADMIN_AGENT_API_URL"),
		LogFile:      logFile,
	})

	os.MkdirAll(schema.SocketDir, 0755)
	os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Agents need to connect, but the socket is owned by root —
	// they can write requests but not read others' requests.
	os.Chmod(socketPath, 0666)

	log.Printf("admin agent listening on %s", socketPath)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		ln.Close()
		os.Exit(0)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go agent.HandleConn(conn)
	}
}
