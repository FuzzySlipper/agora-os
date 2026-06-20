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
	if CanSubscribe(agent, "webview.inbox.00060001.chat") {
		t.Fatal("agent should not be able to subscribe to a non-canonical inbox topic")
	}
	if CanSubscribe(agent, "webview.inbox.60002.chat") {
		t.Fatal("agent should not be able to subscribe to another inbox")
	}
	if !CanSubscribe(agent, "agent.message.*.60001.chat") {
		t.Fatal("agent should be able to subscribe to agent messages addressed to itself")
	}
	if CanSubscribe(agent, "agent.message.*.60002.chat") {
		t.Fatal("agent should not be able to subscribe to agent messages addressed to another uid")
	}
	if !CanSubscribe(agent, "compositor.surface.*") {
		t.Fatal("agent should be able to subscribe to compositor surface events")
	}
	for _, topic := range []string{"shell.apply_theme", "shell.reset_theme", "shell.layout_updated", "shell.widget.inject", "shell.widget.remove", "shell.theme_applied", "widget.weather.current", "widget.weather.*"} {
		if !CanSubscribe(agent, topic) {
			t.Fatalf("agent should be able to subscribe to open shell/widget topic %q", topic)
		}
	}
	if CanSubscribe(agent, "widget.*.*") {
		t.Fatal("agent should not be able to subscribe to wildcard widget name pattern")
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
	if !CanPublish(agent, "webview.inbox.60001.chat") {
		t.Fatal("agent should be able to publish to its own inbox")
	}
	if CanPublish(agent, "webview.inbox.60002.chat") {
		t.Fatal("agent should not be able to publish to another inbox")
	}
	if CanPublish(agent, "webview.inbox.00060001.chat") {
		t.Fatal("agent should not be able to publish to a non-canonical inbox topic")
	}
	if !CanPublish(agent, "agent.message.60001.60002.chat") {
		t.Fatal("agent should be able to publish messages from its own uid")
	}
	for _, topic := range []string{"shell.apply_theme", "shell.reset_theme", "shell.layout_updated", "shell.widget.inject", "shell.widget.remove", "shell.theme_applied", "widget.weather.current"} {
		if !CanPublish(agent, topic) {
			t.Fatalf("agent should be able to publish open shell/widget topic %q", topic)
		}
	}
	if CanPublish(agent, "widget.*.current") {
		t.Fatal("agent should not be able to publish wildcard widget-name topics")
	}
	if CanPublish(agent, "widget.bad$name.current") {
		t.Fatal("agent should not be able to publish widget topics with invalid widget names")
	}
	if CanPublish(agent, "agent.message.60002.60001.chat") {
		t.Fatal("agent should not be able to publish messages claiming another uid")
	}
	if CanPublish(agent, "agent.work.result") {
		t.Fatal("agent should not be able to publish arbitrary bus topics")
	}
	if !CanPublish(Identity{Role: RoleHuman, UID: 0}, "agent.work.result") {
		t.Fatal("human should be able to publish arbitrary topics")
	}
	if CanPublish(Identity{Role: RoleHuman, UID: 0}, "shell.action.completed") {
		t.Fatal("human websocket clients should not be able to publish authoritative shell action topics")
	}
	if CanPublish(agent, "shell.action.completed") {
		t.Fatal("agent websocket clients should not be able to publish authoritative shell action topics")
	}
}
