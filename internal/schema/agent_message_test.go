package schema

import "testing"

func TestAgentMessageTopicRoundTrip(t *testing.T) {
	t.Parallel()

	topic := AgentMessageTopic(60001, 60002, "chat")
	parsed, ok := ParseAgentMessageTopic(topic)
	if !ok {
		t.Fatalf("ParseAgentMessageTopic(%q) failed", topic)
	}
	if parsed.FromUID != 60001 || parsed.ToUID != 60002 || parsed.Kind != "chat" {
		t.Fatalf("got %+v", parsed)
	}
}

func TestParseAgentMessageTopicRejectsNonCanonicalUIDs(t *testing.T) {
	t.Parallel()

	if _, ok := ParseAgentMessageTopic("agent.message.00060001.60002.chat"); ok {
		t.Fatal("expected non-canonical from uid to be rejected")
	}
	if _, ok := ParseAgentMessageTopic("agent.message.60001.00060002.chat"); ok {
		t.Fatal("expected non-canonical to uid to be rejected")
	}
}

func TestParseAgentMessagePatternAllowsWildcardsButRequiresCanonicalUIDs(t *testing.T) {
	t.Parallel()

	parsed, ok := ParseAgentMessagePattern("agent.message.*.60001.*")
	if !ok {
		t.Fatal("expected wildcard pattern to parse")
	}
	if parsed.From != "*" || parsed.To != "60001" || parsed.Kind != "*" {
		t.Fatalf("got %+v", parsed)
	}

	if _, ok := ParseAgentMessagePattern("agent.message.*.00060001.*"); ok {
		t.Fatal("expected non-canonical pattern uid to be rejected")
	}
}
