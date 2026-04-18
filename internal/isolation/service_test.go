package isolation

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
)

type fakeManager struct {
	spawnResp  *schema.AgentInfo
	listResp   []schema.AgentInfo
	spawned    []schema.SpawnAgentRequest
	terminated []uint32
}

func (f *fakeManager) Spawn(req schema.SpawnAgentRequest) (*schema.AgentInfo, error) {
	f.spawned = append(f.spawned, req)
	resp := *f.spawnResp
	return &resp, nil
}

func (f *fakeManager) Terminate(uid uint32) error {
	f.terminated = append(f.terminated, uid)
	return nil
}

func (f *fakeManager) List() []schema.AgentInfo {
	out := make([]schema.AgentInfo, len(f.listResp))
	copy(out, f.listResp)
	return out
}

func TestHandleSpawnPublishesLifecycleEvent(t *testing.T) {
	t.Parallel()

	busSock := startTestBus(t)
	sub := mustDialBus(t, busSock)
	defer sub.Close()
	if err := sub.Subscribe("agent.lifecycle.*"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)

	manager := &fakeManager{
		spawnResp: &schema.AgentInfo{
			Name:      "shell",
			UID:       60001,
			Status:    schema.StatusRunning,
			Slice:     "agent-60001.slice",
			CreatedAt: time.Unix(1700000000, 0).UTC(),
		},
	}
	service := New(manager, busSock)

	body, err := json.Marshal(schema.SpawnAgentRequest{Name: "shell"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.handleSpawn(0, body); err != nil {
		t.Fatal(err)
	}

	ev, err := sub.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Topic != schema.TopicAgentLifecycleSpawned {
		t.Fatalf("got topic %q, want %q", ev.Topic, schema.TopicAgentLifecycleSpawned)
	}
	var lifecycle schema.AgentLifecycleEvent
	if err := json.Unmarshal(ev.Body, &lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle.Agent.UID != 60001 || lifecycle.Agent.Name != "shell" {
		t.Fatalf("got lifecycle %+v", lifecycle.Agent)
	}
}

func TestHandleTerminatePublishesLifecycleEvent(t *testing.T) {
	t.Parallel()

	busSock := startTestBus(t)
	sub := mustDialBus(t, busSock)
	defer sub.Close()
	if err := sub.Subscribe("agent.lifecycle.*"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)

	manager := &fakeManager{
		listResp: []schema.AgentInfo{{
			Name:      "shell",
			UID:       60001,
			Status:    schema.StatusRunning,
			Slice:     "agent-60001.slice",
			CreatedAt: time.Unix(1700000000, 0).UTC(),
		}},
	}
	service := New(manager, busSock)

	body, err := json.Marshal(schema.TerminateAgentRequest{UID: 60001})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.handleTerminate(0, body); err != nil {
		t.Fatal(err)
	}

	ev, err := sub.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Topic != schema.TopicAgentLifecycleTerminated {
		t.Fatalf("got topic %q, want %q", ev.Topic, schema.TopicAgentLifecycleTerminated)
	}
	var lifecycle schema.AgentLifecycleEvent
	if err := json.Unmarshal(ev.Body, &lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle.Agent.UID != 60001 || lifecycle.Agent.Status != schema.StatusStopped {
		t.Fatalf("got lifecycle %+v", lifecycle.Agent)
	}
}

func startTestBus(t *testing.T) string {
	t.Helper()

	sock := filepath.Join(t.TempDir(), "bus.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	broker := bus.NewBroker()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _ = bus.ServeConn(c, broker) }(conn)
		}
	}()

	return sock
}

func mustDialBus(t *testing.T, sock string) *bus.Client {
	t.Helper()

	client, err := bus.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
