// Event bus broker: topic-based pub/sub over a Unix socket.
// This file is the composition root - socket setup, signal handling,
// and connection accept loop. Broker logic lives in internal/bus.
package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
)

const socketPath = schema.BusSocket

func main() {
	broker := bus.NewBroker()

	os.MkdirAll(schema.SocketDir, 0755)
	os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	os.Chmod(socketPath, 0666)

	log.Printf("event bus listening on %s", socketPath)

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
		go func() {
			if err := bus.ServeConn(conn, broker); err != nil {
				log.Printf("connection failed: %v", err)
			}
		}()
	}
}
