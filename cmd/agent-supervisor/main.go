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
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/patch/agora-os/internal/schema"
	"github.com/patch/agora-os/internal/supervisor"
)

const socketPath = schema.SupervisorSocket

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
	// --- Worker profiles ---
	profiles, err := supervisor.NewProfileRegistry([]schema.WorkerProfile{
		{
			Profile:         "repo-inspector",
			Runtime:         schema.RuntimeLocalLLM,
			Tools:           []string{"fs.read", "git.diff", "ripgrep"},
			CPUQuota:        "50%",
			MemoryMax:       "2G",
			NetAccess:       schema.NetDeny,
			WatchPaths:      []string{"/repo"},
			MaxLeaseSeconds: 900,
			ReusePolicy:     schema.ReuseSession,
		},
		{
			Profile:         "patch-writer",
			Runtime:         schema.RuntimeDeterministic,
			Tools:           []string{"fs.write", "git.commit", "patch"},
			CPUQuota:        "100%",
			MemoryMax:       "4G",
			NetAccess:       schema.NetLocalOnly,
			MaxLeaseSeconds: 1800,
			ReusePolicy:     schema.ReuseSession,
		},
		{
			Profile:         "ui-observer",
			Runtime:         schema.RuntimeLocalLLM,
			Tools:           []string{"screenshot", "dom.query", "ui.read"},
			CPUQuota:        "50%",
			MemoryMax:       "4G",
			NetAccess:       schema.NetAllow,
			MaxLeaseSeconds: 600,
			ReusePolicy:     schema.ReuseLease,
		},
	})
	if err != nil {
		log.Fatalf("profile registry: %v", err)
	}

	// --- Profile grants ---
	grants := supervisor.NewGrantRegistry([]schema.ProfileGrant{
		{
			RequesterUID:         0, // root / supervisor itself
			AllowedProfiles:      []string{"repo-inspector", "patch-writer", "ui-observer"},
			MaxConcurrentWorkers: 5,
			MaxLeaseSeconds:      3600,
		},
	})

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

	// --- Graceful shutdown ---
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
