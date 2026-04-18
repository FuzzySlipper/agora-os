package bus

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/patch/agora-os/internal/schema"
)

func validateAgentMessagePublish(publisherUID uint32, topic string, body json.RawMessage) error {
	if !strings.HasPrefix(topic, schema.TopicAgentMessagePrefix) {
		return nil
	}

	address, ok := schema.ParseAgentMessageTopic(topic)
	if !ok {
		return fmt.Errorf("invalid agent.message topic %q", topic)
	}
	if len(body) == 0 {
		return fmt.Errorf("agent.message payload required for %q", topic)
	}

	var envelope schema.AgentMessageEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode agent.message payload: %w", err)
	}
	if envelope.FromUID != publisherUID {
		return fmt.Errorf("agent.message from_uid %d does not match publisher uid %d", envelope.FromUID, publisherUID)
	}
	if envelope.FromUID != address.FromUID {
		return fmt.Errorf("agent.message topic from uid %d does not match payload from uid %d", address.FromUID, envelope.FromUID)
	}
	if envelope.ToUID != address.ToUID {
		return fmt.Errorf("agent.message topic to uid %d does not match payload to uid %d", address.ToUID, envelope.ToUID)
	}
	if envelope.Kind != address.Kind {
		return fmt.Errorf("agent.message topic kind %q does not match payload kind %q", address.Kind, envelope.Kind)
	}
	return nil
}

func validateAgentMessageSubscription(peerUID uint32, pattern string) error {
	if peerUID == 0 || !strings.HasPrefix(pattern, schema.TopicAgentMessagePrefix) {
		return nil
	}

	parsed, ok := schema.ParseAgentMessagePattern(pattern)
	if !ok {
		return fmt.Errorf("invalid agent.message subscription %q", pattern)
	}

	wantUID := strconv.FormatUint(uint64(peerUID), 10)
	if parsed.To != wantUID {
		return fmt.Errorf("agent.message subscription %q is not scoped to recipient uid %d", pattern, peerUID)
	}
	return nil
}
