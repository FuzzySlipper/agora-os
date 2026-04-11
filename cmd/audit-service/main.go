// Audit service composition root.
package main

import (
	"log"
	"os"

	"github.com/patch/agora-os/internal/audit"
	"github.com/patch/agora-os/internal/schema"
)

func main() {
	svc := audit.New(audit.Config{
		WatchPaths:     os.Args[1:],
		LogPath:        audit.DefaultLogPath,
		SubscriberSock: schema.AuditSocket,
		RingSize:       audit.DefaultRingSize,
		Stdout:         os.Stdout,
	})
	if err := svc.Run(); err != nil {
		log.Fatalf("audit service: %v", err)
	}
}
