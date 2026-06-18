package bus

import (
	"testing"
)

func TestValidateTopicPublish_RootAllowed(t *testing.T) {
	privilegedTopics := []string{
		"agent.lifecycle.spawned",
		"agent.work.assigned",
		"compositor.surface.created",
		"audit.file.open",
		"admin.escalation.decided",
	}

	for _, topic := range privilegedTopics {
		t.Run(topic, func(t *testing.T) {
			// Root (uid 0, peer) can publish.
			if err := validateTopicPublish(0, SenderKindPeer, topic); err != nil {
				t.Errorf("root should be allowed to publish %q: %v", topic, err)
			}
			// Delegated can publish.
			if err := validateTopicPublish(60001, SenderKindDelegated, topic); err != nil {
				t.Errorf("delegated should be allowed to publish %q: %v", topic, err)
			}
		})
	}
}

func TestValidateTopicPublish_AgentDenied(t *testing.T) {
	privilegedTopics := []string{
		"agent.lifecycle.spawned",
		"agent.work.assigned",
		"compositor.surface.created",
		"audit.file.open",
		"admin.escalation.decided",
	}

	for _, topic := range privilegedTopics {
		t.Run(topic, func(t *testing.T) {
			// Agent UID (peer) is denied.
			if err := validateTopicPublish(60001, SenderKindPeer, topic); err == nil {
				t.Errorf("agent uid should be denied publishing %q", topic)
			}
			// Another agent UID is also denied.
			if err := validateTopicPublish(60042, SenderKindPeer, topic); err == nil {
				t.Errorf("agent uid should be denied publishing %q", topic)
			}
		})
	}
}

func TestValidateTopicPublish_OpenTopicsAllowed(t *testing.T) {
	openTopics := []string{
		"agent.message.60001.60002.chat",
		"conversation.turn.requested",
		"agent.spawn.requested",
		"agent.work.progress",
		"agent.work.result",
		"agent.work.cancelled",
		"agent.work.needs_3po",
		"test.phase4.ebpf",
		"shell.apply_theme",
		"shell.reset_theme",
		"shell.layout_updated",
		"shell.widget.inject",
		"shell.widget.remove",
		"shell.theme_applied",
		"widget.weather.current",
	}

	for _, topic := range openTopics {
		t.Run(topic, func(t *testing.T) {
			// Agent UID can publish to open topics.
			if err := validateTopicPublish(60001, SenderKindPeer, topic); err != nil {
				t.Errorf("agent uid should be allowed to publish %q: %v", topic, err)
			}
			// Root can also publish.
			if err := validateTopicPublish(0, SenderKindPeer, topic); err != nil {
				t.Errorf("root should be allowed to publish %q: %v", topic, err)
			}
		})
	}
}

func TestValidateTopicSubscribe_RootAllowed(t *testing.T) {
	patterns := []string{
		"admin.escalation.decided",
		"admin.escalation.*",
		"admin.*.*",
	}

	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			if err := validateTopicSubscribe(0, SenderKindPeer, pattern); err != nil {
				t.Errorf("root should be allowed to subscribe %q: %v", pattern, err)
			}
			if err := validateTopicSubscribe(60001, SenderKindDelegated, pattern); err != nil {
				t.Errorf("delegated should be allowed to subscribe %q: %v", pattern, err)
			}
		})
	}
}

func TestValidateTopicSubscribe_AgentDenied(t *testing.T) {
	bypassPatterns := []string{
		"admin.escalation.decided",
		"admin.escalation.*",
		"admin.escalation.requested",
		"admin.*.*",     // wildcard bypass — matches decided
		"*.*.*",         // global wildcard
		"*.*.requested", // suffix-targeted bypass
	}

	for _, pattern := range bypassPatterns {
		t.Run(pattern, func(t *testing.T) {
			if err := validateTopicSubscribe(60001, SenderKindPeer, pattern); err == nil {
				t.Errorf("agent uid should be denied subscribing to %q", pattern)
			}
		})
	}
}

func TestValidateTopicSubscribe_OpenTopicsAllowed(t *testing.T) {
	openPatterns := []string{
		"agent.lifecycle.*",
		"compositor.surface.*",
		"audit.*",
		"agent.message.*",
		"conversation.*",
		"shell.apply_theme",
		"shell.reset_theme",
		"shell.layout_updated",
		"shell.widget.inject",
		"shell.widget.remove",
		"shell.theme_applied",
		"widget.weather.*",
	}

	for _, pattern := range openPatterns {
		t.Run(pattern, func(t *testing.T) {
			if err := validateTopicSubscribe(60001, SenderKindPeer, pattern); err != nil {
				t.Errorf("agent uid should be allowed to subscribe %q: %v", pattern, err)
			}
		})
	}
}

func TestTopicProvenanceError_Format(t *testing.T) {
	err := &TopicProvenanceError{
		Topic:  "audit.file.open",
		Family: "audit.",
		UID:    60001,
		Kind:   SenderKindPeer,
		Op:     "publish",
		Reason: "privileged topic family requires root or delegated authority",
	}
	msg := err.Error()
	if msg == "" {
		t.Fatal("error message should not be empty")
	}
	// Should mention the topic, uid, and operation.
	if !contains(msg, "audit.file.open") || !contains(msg, "60001") || !contains(msg, "publish") {
		t.Errorf("error message missing expected content: %s", msg)
	}
}

func TestTopicPrefixes(t *testing.T) {
	tests := []struct {
		topic string
		want  []string
	}{
		{"audit.file.open", []string{"audit.file.open", "audit.file", "audit"}},
		{"a", []string{"a"}},
		{"a.b", []string{"a.b", "a"}},
	}
	for _, tc := range tests {
		got := topicPrefixes(tc.topic)
		if len(got) != len(tc.want) {
			t.Errorf("topicPrefixes(%q) len = %d, want %d", tc.topic, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("topicPrefixes(%q)[%d] = %q, want %q", tc.topic, i, got[i], tc.want[i])
			}
		}
	}
}

func TestIsRootOrDelegated(t *testing.T) {
	if !isRootOrDelegated(0, SenderKindPeer) {
		t.Error("uid 0 should be root/delegated")
	}
	if !isRootOrDelegated(60001, SenderKindDelegated) {
		t.Error("delegated should be root/delegated")
	}
	if isRootOrDelegated(60001, SenderKindPeer) {
		t.Error("agent uid with peer kind should not be root/delegated")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
