package bus

import (
	"encoding/json"
	"testing"
	"time"
)

func body(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func recv(ch <-chan Event, timeout time.Duration) (Event, bool) {
	select {
	case ev := <-ch:
		return ev, true
	case <-time.After(timeout):
		return Event{}, false
	}
}

func TestBrokerPubSub(t *testing.T) {
	b := NewBroker()

	id1, ch1 := b.Register(1001)
	id2, ch2 := b.Register(1002)

	b.Subscribe(id1, "audit.file.*")
	b.Subscribe(id2, "compositor.surface.*")

	// Publish audit event - only client 1 should receive it.
	b.Publish(-1, Event{Topic: "audit.file.modify", Body: body("test1")})

	ev, ok := recv(ch1, 100*time.Millisecond)
	if !ok {
		t.Fatal("client 1 did not receive audit event")
	}
	if ev.Topic != "audit.file.modify" {
		t.Errorf("got topic %q, want audit.file.modify", ev.Topic)
	}
	if ev.Sender != nil {
		t.Errorf("got sender %v for broker-originated publish, want nil", ev.Sender)
	}

	_, ok = recv(ch2, 50*time.Millisecond)
	if ok {
		t.Error("client 2 should not receive audit events")
	}

	// Publish compositor event - only client 2 should receive it.
	b.Publish(-1, Event{Topic: "compositor.surface.created", Body: body("test2")})

	ev, ok = recv(ch2, 100*time.Millisecond)
	if !ok {
		t.Fatal("client 2 did not receive compositor event")
	}
	if ev.Topic != "compositor.surface.created" {
		t.Errorf("got topic %q, want compositor.surface.created", ev.Topic)
	}
	if ev.Sender != nil {
		t.Errorf("got sender %v for broker-originated publish, want nil", ev.Sender)
	}

	_, ok = recv(ch1, 50*time.Millisecond)
	if ok {
		t.Error("client 1 should not receive compositor events")
	}

	b.Unregister(id1)
	b.Unregister(id2)
}

func TestBrokerSelfExclusion(t *testing.T) {
	b := NewBroker()

	id, ch := b.Register(1001)
	b.Subscribe(id, "audit.*.*")

	// Publisher is the same client - should not receive own event.
	b.Publish(id, Event{Topic: "audit.file.modify", Body: body("self")})

	_, ok := recv(ch, 50*time.Millisecond)
	if ok {
		t.Error("client should not receive its own published event")
	}

	b.Unregister(id)
}

func TestBrokerUnsubscribe(t *testing.T) {
	b := NewBroker()

	id, ch := b.Register(1001)
	b.Subscribe(id, "audit.file.*")

	b.Publish(-1, Event{Topic: "audit.file.modify", Body: body("before")})
	_, ok := recv(ch, 100*time.Millisecond)
	if !ok {
		t.Fatal("did not receive event before unsub")
	}

	b.Unsubscribe(id, "audit.file.*")

	b.Publish(-1, Event{Topic: "audit.file.modify", Body: body("after")})
	_, ok = recv(ch, 50*time.Millisecond)
	if ok {
		t.Error("received event after unsub")
	}

	b.Unregister(id)
}

func TestBrokerSlowSubscriber(t *testing.T) {
	b := NewBroker()

	id, ch := b.Register(1001)
	b.Subscribe(id, "*.*.*")

	// Flood more events than the buffer can hold.
	for i := range subscriberBufSize + 100 {
		b.Publish(-1, Event{Topic: "test.flood.event", Body: body(string(rune(i)))})
	}

	// Drain - should get exactly subscriberBufSize events (non-blocking drop).
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count != subscriberBufSize {
		t.Errorf("got %d events, want %d (buffer size)", count, subscriberBufSize)
	}

	b.Unregister(id)
}

func TestBrokerMultiplePatterns(t *testing.T) {
	b := NewBroker()

	id, ch := b.Register(1001)
	b.Subscribe(id, "audit.file.*")
	b.Subscribe(id, "compositor.surface.*")

	b.Publish(-1, Event{Topic: "audit.file.modify", Body: body("a")})
	b.Publish(-1, Event{Topic: "compositor.surface.created", Body: body("b")})
	b.Publish(-1, Event{Topic: "agent.lifecycle.spawned", Body: body("c")})

	ev1, ok := recv(ch, 100*time.Millisecond)
	if !ok || ev1.Topic != "audit.file.modify" {
		t.Errorf("expected audit.file.modify, got %v %v", ev1.Topic, ok)
	}

	ev2, ok := recv(ch, 100*time.Millisecond)
	if !ok || ev2.Topic != "compositor.surface.created" {
		t.Errorf("expected compositor.surface.created, got %v %v", ev2.Topic, ok)
	}

	// agent.lifecycle.spawned should not be received.
	_, ok = recv(ch, 50*time.Millisecond)
	if ok {
		t.Error("received unsubscribed agent event")
	}

	b.Unregister(id)
}

func TestBrokerPublishStampsSenderUID(t *testing.T) {
	b := NewBroker()

	publisherID, _ := b.Register(60001)
	subscriberID, ch := b.Register(60002)
	b.Subscribe(subscriberID, "agent.work.*")

	b.Publish(publisherID, Event{
		Topic:  "agent.work.result",
		Body:   body("claimed-role:pretend-root"),
		Sender: &Sender{UID: 12345},
	})

	ev, ok := recv(ch, 100*time.Millisecond)
	if !ok {
		t.Fatal("subscriber did not receive published event")
	}
	if ev.Sender == nil {
		t.Fatal("got nil sender metadata, want broker-stamped sender")
	}
	if ev.Sender.UID != 60001 {
		t.Fatalf("got sender uid %d, want 60001", ev.Sender.UID)
	}

	b.Unregister(publisherID)
	b.Unregister(subscriberID)
}

func TestBrokerUnregisterIdempotent(t *testing.T) {
	b := NewBroker()
	id, _ := b.Register(1001)
	b.Unregister(id)
	b.Unregister(id) // should not panic
}
