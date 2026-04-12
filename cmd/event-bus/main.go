// Event bus broker: topic-based pub/sub over a Unix socket.
// This file is the composition root — socket setup, signal handling,
// and connection accept loop. Broker logic lives in internal/bus.
package main

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
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
		go handleConn(conn, broker)
	}
}

func handleConn(conn net.Conn, broker *bus.Broker) {
	id, events := broker.Register()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			broker.Unregister(id)
			conn.Close()
		})
	}
	defer cleanup()

	// Read: client → broker (pub/sub/unsub).
	go func() {
		defer cleanup()
		dec := json.NewDecoder(conn)
		for {
			var msg bus.ClientMsg
			if err := dec.Decode(&msg); err != nil {
				return
			}
			switch msg.Op {
			case bus.OpPub:
				broker.Publish(id, bus.Event{Topic: msg.Topic, Body: msg.Body})
			case bus.OpSub:
				broker.Subscribe(id, msg.Topic)
			case bus.OpUnsub:
				broker.Unsubscribe(id, msg.Topic)
			}
		}
	}()

	// Write: broker → client (matching events).
	enc := json.NewEncoder(conn)
	for ev := range events {
		if err := enc.Encode(ev); err != nil {
			return
		}
	}
}
