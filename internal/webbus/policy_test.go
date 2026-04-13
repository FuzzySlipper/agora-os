package webbus

import "testing"

func TestCanSubscribe(t *testing.T) {
	t.Parallel()

	agent := Identity{Role: RoleAgent, UID: 60001}
	if !CanSubscribe(agent, "webview.broadcast.chat") {
		t.Fatal("agent should be able to subscribe to broadcast topics")
	}
	if !CanSubscribe(agent, "webview.inbox.60001.chat") {
		t.Fatal("agent should be able to subscribe to its own inbox")
	}
	if CanSubscribe(agent, "webview.inbox.60002.chat") {
		t.Fatal("agent should not be able to subscribe to another inbox")
	}
	if !CanSubscribe(agent, "compositor.surface.*") {
		t.Fatal("agent should be able to subscribe to compositor surface events")
	}
	if CanSubscribe(agent, "audit.file.*") {
		t.Fatal("agent should not be able to subscribe to audit feed")
	}
	if !CanSubscribe(Identity{Role: RoleHuman, UID: 0}, "audit.file.*") {
		t.Fatal("human should be able to subscribe to full feed")
	}
}

func TestCanPublish(t *testing.T) {
	t.Parallel()

	agent := Identity{Role: RoleAgent, UID: 60001}
	if !CanPublish(agent, "webview.broadcast.chat") {
		t.Fatal("agent should be able to publish broadcast topics")
	}
	if !CanPublish(agent, "webview.inbox.60002.chat") {
		t.Fatal("agent should be able to publish to another inbox")
	}
	if CanPublish(agent, "agent.work.result") {
		t.Fatal("agent should not be able to publish arbitrary bus topics")
	}
	if !CanPublish(Identity{Role: RoleHuman, UID: 0}, "agent.work.result") {
		t.Fatal("human should be able to publish arbitrary topics")
	}
}
