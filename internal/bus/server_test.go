package bus

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServeConnStampsPeerUID(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "bus.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	broker := NewBroker()
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_ = ServeConn(c, broker)
			}(conn)
		}
	}()

	subscriber, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer subscriber.Close()

	publisher, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()

	if err := subscriber.Subscribe("agent.work.*"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// The broker processes subscribe messages asynchronously over the socket.
	// Give the subscription a brief moment to register before publishing.
	time.Sleep(50 * time.Millisecond)
	if err := publisher.Publish("agent.work.result", map[string]any{
		"claimed_role": "pretend-root",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	evCh := make(chan Event, 1)
	errCh := make(chan error, 1)
	go func() {
		ev, err := subscriber.Receive()
		if err != nil {
			errCh <- err
			return
		}
		evCh <- ev
	}()

	select {
	case err := <-errCh:
		t.Fatalf("Receive: %v", err)
	case ev := <-evCh:
		if ev.Sender == nil {
			t.Fatal("got nil sender metadata, want broker-stamped sender")
		}
		wantUID := uint32(os.Getuid())
		if ev.Sender.UID != wantUID {
			t.Fatalf("got sender uid %d, want %d", ev.Sender.UID, wantUID)
		}

		var body map[string]string
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			t.Fatalf("Unmarshal body: %v", err)
		}
		if body["claimed_role"] != "pretend-root" {
			t.Fatalf("got claimed_role %q, want pretend-root", body["claimed_role"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for published event")
	}

	ln.Close()
	<-acceptDone
}
