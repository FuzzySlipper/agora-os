// Agent supervisor: deterministic root-owned daemon for R2 worker lifecycle
// management. Sits between orchestration (shell/3PO) and isolation
// (isolation-service). Manages leases, enforces profile grants and budgets,
// and coordinates reuse decisions.
//
// This file is the composition root — config, socket setup, signal handling,
// and dependency wiring. Request dispatch and authorization live in
// internal/supervisor.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/patch/agora-os/internal/schema"
	"github.com/patch/agora-os/internal/supervisor"
)

const (
	socketPath        = schema.SupervisorSocket
	defaultConfigPath = "/etc/agent-os/agent-supervisor.json"
)

// budgetAdapter wraps the standalone CheckBudget function to implement the
// BudgetChecker interface expected by supervisor.Service.
type budgetAdapter struct{}

func (budgetAdapter) CheckBudget(store supervisor.LeaseStore, grant schema.ProfileGrant, sessionID string, requesterUID uint32) error {
	return supervisor.CheckBudget(store, grant, sessionID, requesterUID)
}

// reuseAdapter wraps the standalone CanReuse function to implement the
// ReuseChecker interface expected by supervisor.Service.
type reuseAdapter struct{}

func (reuseAdapter) CanReuse(lease schema.WorkerLease, req schema.EnsureWorkerRequest, profile schema.WorkerProfile) (bool, string) {
	return supervisor.CanReuse(lease, req, profile)
}

func main() {
	configPath := os.Getenv("AGENT_SUPERVISOR_CONFIG")
	if configPath == "" {
		configPath = defaultConfigPath
	}
	flag.StringVar(&configPath, "config", configPath, "agent supervisor JSON config path")
	flag.Parse()

	cfg, err := supervisor.LoadConfig(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && configPath == defaultConfigPath && os.Getenv("AGENT_SUPERVISOR_CONFIG") == "" {
			log.Printf("supervisor config %s not found; using built-in defaults", configPath)
			cfg = supervisor.DefaultConfig()
		} else {
			log.Fatalf("load supervisor config: %v", err)
		}
	}

	// --- Worker profiles ---
	profiles, err := supervisor.NewProfileRegistry(cfg.Profiles)
	if err != nil {
		log.Fatalf("profile registry: %v", err)
	}

	// --- Profile grants ---
	grants := supervisor.NewGrantRegistry(cfg.Grants)

	// --- Dependencies ---
	store := supervisor.NewWorkerLeaseStore()
	isoClient := supervisor.NewIsolationClient(schema.IsolationSocket)
	busClient := supervisor.NewBusClient(schema.BusSocket)
	peerCreds := supervisor.NewPeerCredProvider()

	// --- Service ---
	svc := supervisor.New(
		store,
		profiles,
		grants,
		budgetAdapter{},
		reuseAdapter{},
		isoClient,
		busClient,
		peerCreds,
	)

	// --- Socket setup ---
	os.MkdirAll(schema.SocketDir, 0755)
	os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	os.Chmod(socketPath, 0666)

	log.Printf("agent supervisor listening on %s", socketPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.StartExpiryLoop(ctx, 30*time.Second)

	// --- Graceful shutdown ---
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down")
		cancel()
		ln.Close()
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
