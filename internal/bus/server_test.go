package bus

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/schema"
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

	if err := subscriber.Subscribe("test.stamp.*"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// The broker processes subscribe messages asynchronously over the socket.
	// Give the subscription a brief moment to register before publishing.
	time.Sleep(50 * time.Millisecond)
	if err := publisher.Publish("test.stamp.result", map[string]any{
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
		if ev.Sender.Kind != SenderKindPeer {
			t.Fatalf("got sender kind %q, want %q", ev.Sender.Kind, SenderKindPeer)
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

func TestServeConnIgnoresSenderUIDOverrideForNonRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("requires non-root to prove sender override is ignored")
	}

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

	if err := subscriber.Subscribe("test.override.*"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	if err := publisher.PublishAs(Sender{UID: 12345, Kind: SenderKindDelegated}, "test.override.result", map[string]any{"kind": "override-attempt"}); err != nil {
		t.Fatalf("PublishAs: %v", err)
	}

	ev, err := subscriber.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if ev.Sender == nil {
		t.Fatal("got nil sender metadata, want broker-stamped sender")
	}
	wantUID := uint32(os.Getuid())
	if ev.Sender.UID != wantUID {
		t.Fatalf("got sender uid %d, want %d", ev.Sender.UID, wantUID)
	}
	if ev.Sender.Kind != SenderKindPeer {
		t.Fatalf("got sender kind %q, want %q", ev.Sender.Kind, SenderKindPeer)
	}

	ln.Close()
	<-acceptDone
}

func TestServeConnAllowsSenderUIDOverrideForRoot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root to prove sender override is honored")
	}

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
	time.Sleep(50 * time.Millisecond)

	const delegatedUID uint32 = 60042
	if err := publisher.PublishAs(Sender{UID: delegatedUID, Kind: SenderKindDelegated}, "agent.work.result", map[string]any{"kind": "delegated"}); err != nil {
		t.Fatalf("PublishAs: %v", err)
	}

	ev, err := subscriber.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if ev.Sender == nil {
		t.Fatal("got nil sender metadata, want broker-stamped sender")
	}
	if ev.Sender.UID != delegatedUID {
		t.Fatalf("got sender uid %d, want %d", ev.Sender.UID, delegatedUID)
	}
	if ev.Sender.Kind != SenderKindDelegated {
		t.Fatalf("got sender kind %q, want %q", ev.Sender.Kind, SenderKindDelegated)
	}

	ln.Close()
	<-acceptDone
}

func TestServeConnPublishesAgentMessageForValidatedSender(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("requires non-root so the publisher uid behaves like an agent uid")
	}

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

	uid := uint32(os.Getuid())
	if err := subscriber.Subscribe(schema.AgentMessageTopic(uid, uid, "chat")); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	body := schema.AgentMessageEnvelope{
		FromUID: uid,
		ToUID:   uid,
		Kind:    "chat",
		Body:    json.RawMessage(`{"text":"hello"}`),
	}
	if err := publisher.Publish(schema.AgentMessageTopic(uid, uid, "chat"), body); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	ev, err := subscriber.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if ev.Sender == nil {
		t.Fatal("got nil sender metadata, want broker-stamped sender")
	}
	if ev.Sender.UID != uid {
		t.Fatalf("got sender uid %d, want %d", ev.Sender.UID, uid)
	}
	if ev.Topic != schema.AgentMessageTopic(uid, uid, "chat") {
		t.Fatalf("got topic %q", ev.Topic)
	}

	ln.Close()
	<-acceptDone
}

func TestServeConnRejectsForgedAgentMessagePublish(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("requires non-root so the forged sender differs from the peer uid")
	}

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

	uid := uint32(os.Getuid())
	if err := subscriber.Subscribe("agent.message.*." + strconv.FormatUint(uint64(uid), 10) + ".*"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	rawConn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer rawConn.Close()

	enc := json.NewEncoder(rawConn)
	forgedTopic := schema.AgentMessageTopic(uid+1, uid, "chat")
	forgedBody := schema.AgentMessageEnvelope{
		FromUID: uid + 1,
		ToUID:   uid,
		Kind:    "chat",
	}
	if err := enc.Encode(ClientMsg{Op: OpPub, Topic: forgedTopic, Body: mustJSONRaw(t, forgedBody)}); err != nil {
		t.Fatalf("Encode forged publish: %v", err)
	}

	rawConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg ClientMsg
	err = json.NewDecoder(rawConn).Decode(&msg)
	if err == nil {
		t.Fatal("expected forged publish connection to close")
	}
	var netErr net.Error
	if !errors.Is(err, net.ErrClosed) && !errors.As(err, &netErr) && err.Error() != "EOF" {
		t.Fatalf("got error %v, want EOF/closed connection", err)
	}

	rawConn.SetReadDeadline(time.Time{})
	select {
	case ev := <-receiveEvent(t, subscriber):
		t.Fatalf("unexpected event published despite forged sender: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}

	ln.Close()
	<-acceptDone
}

func TestServeConnRejectsCrossRecipientAgentMessageSubscription(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("requires non-root so the subscriber uid behaves like an agent uid")
	}

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

	rawConn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer rawConn.Close()

	enc := json.NewEncoder(rawConn)
	uid := uint32(os.Getuid())
	if err := enc.Encode(ClientMsg{Op: OpSub, Topic: "agent.message.*." + strconv.FormatUint(uint64(uid+1), 10) + ".*"}); err != nil {
		t.Fatalf("Encode subscribe: %v", err)
	}

	rawConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg ClientMsg
	err = json.NewDecoder(rawConn).Decode(&msg)
	if err == nil {
		t.Fatal("expected invalid subscription connection to close")
	}
	var netErr net.Error
	if !errors.Is(err, net.ErrClosed) && !errors.As(err, &netErr) && err.Error() != "EOF" {
		t.Fatalf("got error %v, want EOF/closed connection", err)
	}

	ln.Close()
	<-acceptDone
}

func receiveEvent(t *testing.T, client *Client) <-chan Event {
	t.Helper()
	ch := make(chan Event, 1)
	go func() {
		ev, err := client.Receive()
		if err == nil {
			ch <- ev
		}
	}()
	return ch
}

func mustJSONRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ── Provenance policy integration tests ────────────────────────────────────

func TestServeConnRejectsAgentPublishOnPrivilegedTopic(t *testing.T) {
	// Non-root peer publishing to a privileged topic should be rejected
	// (connection closed) by the provenance ACL.
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

	tests := []struct {
		topic string
		desc  string
	}{
		{"audit.file.open", "audit"},
		{"agent.lifecycle.spawned", "lifecycle"},
		{"compositor.surface.created", "surface"},
		{"admin.escalation.decided", "escalation"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			rawConn, err := net.Dial("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			defer rawConn.Close()

			enc := json.NewEncoder(rawConn)
			if err := enc.Encode(ClientMsg{Op: OpPub, Topic: tc.topic, Body: body("forged")}); err != nil {
				t.Fatalf("Encode publish: %v", err)
			}

			rawConn.SetReadDeadline(time.Now().Add(2 * time.Second))
			var msg ClientMsg
			err = json.NewDecoder(rawConn).Decode(&msg)
			if err == nil {
				if os.Getuid() == 0 {
					t.Log("running as root — privileged publish is allowed")
				} else {
					t.Fatalf("expected agent publish on %s to close connection", tc.topic)
				}
			}
		})
	}

	ln.Close()
	<-acceptDone
}

// TestServeConnRejectsAgentPublishOnPrivilegedTopic is now at the top of this block —
// it was renamed to the parameterized version above.

func TestServeConnRejectsAgentSubscribeOnPrivilegedTopic(t *testing.T) {
	// Non-root peer subscribing to admin.escalation.* should be rejected.
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

	rawConn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer rawConn.Close()

	enc := json.NewEncoder(rawConn)
	if err := enc.Encode(ClientMsg{Op: OpSub, Topic: "admin.escalation.*"}); err != nil {
		t.Fatalf("Encode subscribe: %v", err)
	}

	rawConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg ClientMsg
	err = json.NewDecoder(rawConn).Decode(&msg)
	if err == nil {
		if os.Getuid() == 0 {
			t.Log("running as root — privileged subscribe is allowed; connection remains open")
		} else {
			t.Fatal("expected agent subscribe on admin.escalation.* to close connection")
		}
	}

	ln.Close()
	<-acceptDone
}

func TestServeConnAllowsAgentOpenTopicPublish(t *testing.T) {
	// Non-root peer publishing to an open topic should succeed.
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

	publisher, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()

	subscriber, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer subscriber.Close()

	if err := subscriber.Subscribe("test.open.*"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	if err := publisher.Publish("test.open.topic", map[string]any{"msg": "hello"}); err != nil {
		t.Fatalf("Publish open topic: %v", err)
	}

	ev, err := subscriber.Receive()
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if ev.Topic != "test.open.topic" {
		t.Errorf("got topic %q, want test.open.topic", ev.Topic)
	}

	ln.Close()
	<-acceptDone
}
