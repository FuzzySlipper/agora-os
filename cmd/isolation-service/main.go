// Isolation service: manages agent Linux users, cgroups, and network rules.
// This file is the composition root — bootstrap, socket setup, and signal
// handling. Request dispatch and authorization live in internal/isolation.
package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/patch/agora-os/internal/agent"
	"github.com/patch/agora-os/internal/isolation"
	"github.com/patch/agora-os/internal/schema"
)

const socketPath = schema.IsolationSocket

func main() {
	// Set up nftables table + chains before accepting any requests.
	if err := agent.BootstrapNftables(); err != nil {
		log.Fatalf("nft bootstrap: %v", err)
	}

	mgr := agent.NewManager()
	svc := isolation.New(mgr)

	// Ensure the socket directory exists
	os.MkdirAll(schema.SocketDir, 0755)
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
		go svc.HandleConn(conn)
	}
}
