package audit

import (
	"testing"
	"time"
)

func TestBrokerPublishAndSubscribe(t *testing.T) {
	b := NewBroker(4)

	// Publish before any subscribers — fills ring buffer
	b.Publish([]byte("e1\n"))
	b.Publish([]byte("e2\n"))

	// Subscribe: should get backlog
	id, backlog, ch := b.Subscribe()
	defer b.Unsubscribe(id)

	if len(backlog) != 2 {
		t.Fatalf("backlog len = %d, want 2", len(backlog))
	}
	if string(backlog[0]) != "e1\n" || string(backlog[1]) != "e2\n" {
		t.Fatalf("backlog = %q, want [e1 e2]", backlog)
	}

	// Publish after subscribe — should arrive on channel
	b.Publish([]byte("e3\n"))

	select {
	case got := <-ch:
		if string(got) != "e3\n" {
			t.Fatalf("got %q, want e3", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestBrokerRingWraparound(t *testing.T) {
	b := NewBroker(3)

	// Publish 5 events into a ring of size 3
	for i := range 5 {
		b.Publish([]byte{byte('a' + i), '\n'})
	}

	_, backlog, _ := b.Subscribe()

	if len(backlog) != 3 {
		t.Fatalf("backlog len = %d, want 3", len(backlog))
	}
	// Oldest surviving events: c, d, e (indices 2, 3, 4)
	want := []string{"c\n", "d\n", "e\n"}
	for i, w := range want {
		if string(backlog[i]) != w {
			t.Errorf("backlog[%d] = %q, want %q", i, backlog[i], w)
		}
	}
}

func TestBrokerUnsubscribe(t *testing.T) {
	b := NewBroker(4)

	id, _, ch := b.Subscribe()
	b.Unsubscribe(id)

	// Publish after unsubscribe — channel should NOT receive
	b.Publish([]byte("x\n"))

	select {
	case <-ch:
		t.Fatal("received event after unsubscribe")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestBrokerSlowSubscriber(t *testing.T) {
	b := NewBroker(4)

	id, _, ch := b.Subscribe()
	defer b.Unsubscribe(id)

	// Flood more events than the subscriber channel buffer
	for range subscriberBufSize + 100 {
		b.Publish([]byte("x\n"))
	}

	// Should have subscriberBufSize events in the channel, rest dropped
	got := 0
	for {
		select {
		case <-ch:
			got++
		default:
			goto done
		}
	}
done:
	if got != subscriberBufSize {
		t.Fatalf("got %d buffered events, want %d", got, subscriberBufSize)
	}
}
