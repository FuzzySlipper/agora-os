package bus

import (
	"encoding/json"
	"testing"

	"github.com/patch/agora-os/internal/schema"
)

func TestValidateAgentMessagePublishAllowsMatchingSender(t *testing.T) {
	t.Parallel()

	topic := schema.AgentMessageTopic(60001, 60002, "chat")
	body, err := json.Marshal(schema.AgentMessageEnvelope{
		FromUID: 60001,
		ToUID:   60002,
		Kind:    "chat",
		Body:    json.RawMessage(`{"text":"hello"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := validateAgentMessagePublish(60001, topic, body); err != nil {
		t.Fatalf("validateAgentMessagePublish: %v", err)
	}
}

func TestValidateAgentMessagePublishRejectsForgedFromUID(t *testing.T) {
	t.Parallel()

	topic := schema.AgentMessageTopic(60002, 60001, "chat")
	body, err := json.Marshal(schema.AgentMessageEnvelope{
		FromUID: 60002,
		ToUID:   60001,
		Kind:    "chat",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := validateAgentMessagePublish(60001, topic, body); err == nil {
		t.Fatal("expected forged from_uid to be rejected")
	}
}

func TestValidateAgentMessagePublishRejectsTopicEnvelopeMismatch(t *testing.T) {
	t.Parallel()

	topic := schema.AgentMessageTopic(60001, 60002, "chat")
	body, err := json.Marshal(schema.AgentMessageEnvelope{
		FromUID: 60001,
		ToUID:   60003,
		Kind:    "chat",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := validateAgentMessagePublish(60001, topic, body); err == nil {
		t.Fatal("expected mismatched topic/body recipient to be rejected")
	}
}

func TestValidateAgentMessageSubscriptionAllowsOwnRecipientPattern(t *testing.T) {
	t.Parallel()

	if err := validateAgentMessageSubscription(60001, "agent.message.*.60001.*"); err != nil {
		t.Fatalf("validateAgentMessageSubscription: %v", err)
	}
}

func TestValidateAgentMessageSubscriptionRejectsOtherRecipientPattern(t *testing.T) {
	t.Parallel()

	if err := validateAgentMessageSubscription(60001, "agent.message.*.60002.*"); err == nil {
		t.Fatal("expected subscription for another recipient uid to be rejected")
	}
}

func TestValidateAgentMessageSubscriptionRejectsWildcardRecipient(t *testing.T) {
	t.Parallel()

	if err := validateAgentMessageSubscription(60001, "agent.message.*.*.*"); err == nil {
		t.Fatal("expected wildcard recipient subscription to be rejected")
	}
}
