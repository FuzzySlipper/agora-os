package schema

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const TopicAgentMessagePrefix = "agent.message."

// AgentMessageEnvelope is the structured payload carried on
// agent.message.<from-uid>.<to-uid>.* topics.
type AgentMessageEnvelope struct {
	MessageID  string          `json:"message_id,omitempty"`
	FromUID    uint32          `json:"from_uid"`
	ToUID      uint32          `json:"to_uid"`
	Kind       string          `json:"kind"`
	Body       json.RawMessage `json:"body,omitempty"`
	ReplyTopic string          `json:"reply_topic,omitempty"`
}

type AgentMessageAddress struct {
	FromUID uint32
	ToUID   uint32
	Kind    string
}

type AgentMessagePattern struct {
	From string
	To   string
	Kind string
}

func AgentMessageTopic(fromUID, toUID uint32, kind string) string {
	return fmt.Sprintf("%s%d.%d.%s", TopicAgentMessagePrefix, fromUID, toUID, kind)
}

func ParseAgentMessageTopic(topic string) (AgentMessageAddress, bool) {
	parts := strings.Split(topic, ".")
	if len(parts) != 5 {
		return AgentMessageAddress{}, false
	}
	if parts[0] != "agent" || parts[1] != "message" || parts[4] == "" || parts[4] == "*" {
		return AgentMessageAddress{}, false
	}

	fromUID, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil || parts[2] != strconv.FormatUint(fromUID, 10) {
		return AgentMessageAddress{}, false
	}
	toUID, err := strconv.ParseUint(parts[3], 10, 32)
	if err != nil || parts[3] != strconv.FormatUint(toUID, 10) {
		return AgentMessageAddress{}, false
	}

	return AgentMessageAddress{
		FromUID: uint32(fromUID),
		ToUID:   uint32(toUID),
		Kind:    parts[4],
	}, true
}

func ParseAgentMessagePattern(pattern string) (AgentMessagePattern, bool) {
	parts := strings.Split(pattern, ".")
	if len(parts) != 5 {
		return AgentMessagePattern{}, false
	}
	if parts[0] != "agent" || parts[1] != "message" || parts[4] == "" {
		return AgentMessagePattern{}, false
	}
	if !isWildcardOrCanonicalUint(parts[2]) || !isWildcardOrCanonicalUint(parts[3]) {
		return AgentMessagePattern{}, false
	}
	return AgentMessagePattern{
		From: parts[2],
		To:   parts[3],
		Kind: parts[4],
	}, true
}

func isWildcardOrCanonicalUint(segment string) bool {
	if segment == "*" {
		return true
	}
	parsed, err := strconv.ParseUint(segment, 10, 32)
	if err != nil {
		return false
	}
	return segment == strconv.FormatUint(parsed, 10)
}
