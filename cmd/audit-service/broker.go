package main

import (
	"io"
	"log"
	"net"
	"os"
	"sync"
)

const subscriberBufSize = 256

// Broker fans out serialized audit events to live subscribers and keeps
// a ring buffer so late subscribers can catch up.
type Broker struct {
	mu     sync.Mutex
	ring   [][]byte
	pos    int // next write index
	count  int // how many slots are occupied (≤ len(ring))
	subs   map[int]chan []byte
	nextID int
}

func NewBroker(ringSize int) *Broker {
	return &Broker{
		ring: make([][]byte, ringSize),
		subs: make(map[int]chan []byte),
	}
}

// Publish writes an event to the ring buffer and fans it out to all
// live subscribers. Non-blocking: slow subscribers miss events.
func (b *Broker) Publish(line []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Copy so the caller can reuse the slice.
	cp := make([]byte, len(line))
	copy(cp, line)

	b.ring[b.pos] = cp
	b.pos = (b.pos + 1) % len(b.ring)
	if b.count < len(b.ring) {
		b.count++
	}

	for _, ch := range b.subs {
		select {
		case ch <- cp:
		default: // subscriber too slow — drop
		}
	}
}

// Subscribe returns a subscriber ID, the current ring buffer contents
// (oldest first), and a channel that receives future events.
func (b *Broker) Subscribe() (int, [][]byte, <-chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++
	ch := make(chan []byte, subscriberBufSize)
	b.subs[id] = ch

	var backlog [][]byte
	if b.count > 0 {
		start := 0
		n := b.count
		if b.count >= len(b.ring) {
			start = b.pos // oldest entry
			n = len(b.ring)
		}
		backlog = make([][]byte, n)
		for i := range n {
			backlog[i] = b.ring[(start+i)%len(b.ring)]
		}
	}

	return id, backlog, ch
}

// Unsubscribe removes a subscriber. Safe to call after the subscriber
// has already been removed (idempotent).
func (b *Broker) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs, id)
}

// serveSubscribers listens on a Unix socket and streams JSON-lines to
// each connection: first the ring buffer backlog, then live events.
func serveSubscribers(sockPath string, broker *Broker) {
	os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		log.Fatalf("audit subscriber listen: %v", err)
	}
	os.Chmod(sockPath, 0666)
	log.Printf("subscriber endpoint: %s", sockPath)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("subscriber accept: %v", err)
			return
		}
		go handleSubscriber(conn, broker)
	}
}

func handleSubscriber(conn net.Conn, broker *Broker) {
	defer conn.Close()

	id, backlog, ch := broker.Subscribe()
	defer broker.Unsubscribe(id)

	// Detect peer disconnect via a read goroutine.
	done := make(chan struct{})
	go func() {
		io.Copy(io.Discard, conn)
		close(done)
	}()

	// Send backlog
	for _, line := range backlog {
		if _, err := conn.Write(line); err != nil {
			return
		}
	}

	// Stream live events until disconnect or write error
	for {
		select {
		case line := <-ch:
			if _, err := conn.Write(line); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}
