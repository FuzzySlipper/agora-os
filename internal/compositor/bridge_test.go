package compositor

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

type publishedEvent struct {
	topic string
	body  any
}

type fakePublisher struct {
	events []publishedEvent
}

func (f *fakePublisher) Publish(topic string, body any) error {
	f.events = append(f.events, publishedEvent{topic: topic, body: body})
	return nil
}

func TestHandlePluginConnPublishesMappedEvent(t *testing.T) {
	pub := &fakePublisher{}
	bridge := New(pub)

	server, client := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		bridge.HandlePluginConn(server)
	}()

	dec := json.NewDecoder(client)
	var replace schema.CompositorPolicyReplace
	if err := dec.Decode(&replace); err != nil {
		t.Fatalf("decode policy replace: %v", err)
	}
	if replace.Type != schema.PluginMessagePolicyReplace {
		t.Fatalf("got sync type %q, want %q", replace.Type, schema.PluginMessagePolicyReplace)
	}

	var input schema.CompositorInputContextUpdate
	if err := dec.Decode(&input); err != nil {
		t.Fatalf("decode input context: %v", err)
	}
	if input.Type != schema.PluginMessageInputContext {
		t.Fatalf("got input type %q, want %q", input.Type, schema.PluginMessageInputContext)
	}
	if input.ActorUID != nil {
		t.Fatalf("got actor uid %v, want nil", *input.ActorUID)
	}

	enc := json.NewEncoder(client)
	event := schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-42", WayfireViewID: 42, AppID: "org.example.App", Title: "Example", Role: "toplevel"},
		Client:  schema.CompositorClientIdentity{PID: 1234, UID: 60001, GID: 60001},
	}
	if err := enc.Encode(event); err != nil {
		t.Fatalf("encode plugin event: %v", err)
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for len(pub.events) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(pub.events) != 1 {
		t.Fatalf("got %d published events, want 1", len(pub.events))
	}
	if pub.events[0].topic != schema.TopicCompositorSurfaceCreated {
		t.Fatalf("got topic %q, want %q", pub.events[0].topic, schema.TopicCompositorSurfaceCreated)
	}
	body, ok := pub.events[0].body.(schema.CompositorBusEvent)
	if !ok {
		t.Fatalf("got body type %T, want schema.CompositorBusEvent", pub.events[0].body)
	}
	if body.Client.UID != 60001 {
		t.Fatalf("got owner uid %d, want 60001", body.Client.UID)
	}

	surfaces := bridge.ListSurfaces()
	if len(surfaces) != 1 || surfaces[0].Surface.ID != "view-42" {
		t.Fatalf("got surfaces %+v, want tracked view-42", surfaces)
	}

	client.Close()
	<-done
}

func TestReconnectSendsPolicySnapshot(t *testing.T) {
	pub := &fakePublisher{}
	bridge := New(pub)

	policy := schema.CompositorSurfacePolicy{
		SurfaceID:         "view-99",
		OwnerUID:          60002,
		AllowPointerUIDs:  []uint32{0, 60002},
		AllowKeyboardUIDs: []uint32{0},
	}
	if err := bridge.UpsertSurfacePolicy(policy); err != nil {
		t.Fatalf("UpsertSurfacePolicy: %v", err)
	}
	actor := uint32(60002)
	if err := bridge.SetInputContext(&actor); err != nil {
		t.Fatalf("SetInputContext: %v", err)
	}

	server, client := net.Pipe()
	defer client.Close()
	go bridge.HandlePluginConn(server)

	dec := json.NewDecoder(client)
	var replace schema.CompositorPolicyReplace
	if err := dec.Decode(&replace); err != nil {
		t.Fatalf("decode policy replace: %v", err)
	}
	if len(replace.Surfaces) != 1 || replace.Surfaces[0].SurfaceID != "view-99" {
		t.Fatalf("got surfaces %+v, want synced view-99 policy", replace.Surfaces)
	}

	var input schema.CompositorInputContextUpdate
	if err := dec.Decode(&input); err != nil {
		t.Fatalf("decode input context: %v", err)
	}
	if input.ActorUID == nil || *input.ActorUID != 60002 {
		t.Fatalf("got actor uid %v, want 60002", input.ActorUID)
	}
}

func TestDispatchRejectsNonRootMutation(t *testing.T) {
	bridge := New(&fakePublisher{})
	body, _ := json.Marshal(schema.UpsertSurfacePolicyRequest{
		Surface: schema.CompositorSurfacePolicy{SurfaceID: "view-1", OwnerUID: 60001},
	})
	_, err := bridge.dispatch(1234, schema.Request{Method: schema.MethodUpsertSurfacePolicy, Body: body})
	if err == nil {
		t.Fatal("expected non-root mutation to be rejected")
	}
}

func TestCloseSurfacesByUIDQueuesMatchingSurfaces(t *testing.T) {
	bridge := New(&fakePublisher{})
	server, client := net.Pipe()
	defer client.Close()

	go bridge.HandlePluginConn(server)
	dec := json.NewDecoder(client)
	var discard any
	_ = dec.Decode(&discard)
	_ = dec.Decode(&discard)

	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-a", WayfireViewID: 10},
		Client:  schema.CompositorClientIdentity{UID: 60001},
	})
	bridge.handleSurfaceEvent(schema.CompositorPluginEvent{
		Type:    schema.PluginMessageSurfaceEvent,
		Event:   schema.SurfaceEventMapped,
		Surface: schema.CompositorSurface{ID: "view-b", WayfireViewID: 11},
		Client:  schema.CompositorClientIdentity{UID: 60002},
	})

	queuedCh := make(chan int, 1)
	errCh := make(chan error, 1)
	go func() {
		queued, err := bridge.CloseSurfacesByUID(60001)
		if err != nil {
			errCh <- err
			return
		}
		queuedCh <- queued
	}()

	var closeMsg schema.CompositorCloseSurfacesByUID
	if err := dec.Decode(&closeMsg); err != nil {
		t.Fatalf("decode close msg: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("CloseSurfacesByUID: %v", err)
	case queued := <-queuedCh:
		if queued != 1 {
			t.Fatalf("got queued %d, want 1", queued)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for CloseSurfacesByUID result")
	}
	if closeMsg.Type != schema.PluginMessageCloseSurfacesByUID || closeMsg.OwnerUID != 60001 {
		t.Fatalf("got close msg %+v, want owner 60001", closeMsg)
	}
}
