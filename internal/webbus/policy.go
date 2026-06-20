package webbus

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/patch/agora-os/internal/schema"
)

const (
	TopicWebviewBroadcastPrefix = "webview.broadcast."
	TopicWebviewInboxPrefix     = "webview.inbox."
)

func CanSubscribe(identity Identity, pattern string) bool {
	if identity.Role == RoleHuman {
		return true
	}
	if strings.HasPrefix(pattern, TopicWebviewBroadcastPrefix) {
		return true
	}
	if isOwnInboxTarget(identity.UID, pattern) {
		return true
	}
	if isOwnAgentMessageSubscription(identity.UID, pattern) {
		return true
	}
	if strings.HasPrefix(pattern, "compositor.surface.") {
		return true
	}
	if isShellTopic(pattern) || isWidgetTopicPattern(pattern) {
		return true
	}
	return false
}

func CanPublish(identity Identity, topic string) bool {
	if strings.HasPrefix(topic, "shell.action.") {
		return false
	}
	if identity.Role == RoleHuman {
		return true
	}
	if strings.HasPrefix(topic, TopicWebviewBroadcastPrefix) {
		return true
	}
	if isOwnInboxTarget(identity.UID, topic) {
		return true
	}
	if isOwnAgentMessageTopic(identity.UID, topic) {
		return true
	}
	if isShellTopic(topic) || isWidgetTopic(topic) {
		return true
	}
	return false
}

func isShellTopic(topic string) bool {
	switch topic {
	case "shell.apply_theme", "shell.reset_theme", "shell.layout_updated", "shell.widget.inject", "shell.widget.remove", "shell.theme_applied":
		return true
	default:
		return false
	}
}

func isWidgetTopic(topic string) bool {
	parts := strings.Split(topic, ".")
	return len(parts) >= 3 && parts[0] == "widget" && validWidgetTopicName(parts[1])
}

func isWidgetTopicPattern(pattern string) bool {
	parts := strings.Split(pattern, ".")
	return len(parts) >= 3 && parts[0] == "widget" && validWidgetTopicName(parts[1])
}

func validWidgetTopicName(name string) bool {
	if name == "" || name == "*" {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func isOwnInboxTarget(uid uint32, subject string) bool {
	targetUID, ok := inboxUID(subject)
	return ok && targetUID == uid
}

func inboxUID(subject string) (uint32, bool) {
	parts := strings.Split(subject, ".")
	if len(parts) < 4 {
		return 0, false
	}
	if parts[0] != "webview" || parts[1] != "inbox" {
		return 0, false
	}
	parsed, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil {
		return 0, false
	}
	if parts[2] != strconv.FormatUint(parsed, 10) {
		return 0, false
	}
	return uint32(parsed), true
}

func isOwnAgentMessageSubscription(uid uint32, pattern string) bool {
	parsed, ok := schema.ParseAgentMessagePattern(pattern)
	if !ok {
		return false
	}
	want := strconv.FormatUint(uint64(uid), 10)
	return parsed.To == want
}

func isOwnAgentMessageTopic(uid uint32, topic string) bool {
	parsed, ok := schema.ParseAgentMessageTopic(topic)
	if !ok {
		return false
	}
	return parsed.FromUID == uid
}

func DescribeIdentity(identity Identity) string {
	if identity.Role == RoleHuman {
		return "human"
	}
	return fmt.Sprintf("agent:%d", identity.UID)
}
