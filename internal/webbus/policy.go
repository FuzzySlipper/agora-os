package webbus

import (
	"fmt"
	"strconv"
	"strings"
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
	if isOwnInboxPattern(identity.UID, pattern) {
		return true
	}
	if strings.HasPrefix(pattern, "compositor.surface.") {
		return true
	}
	return false
}

func CanPublish(identity Identity, topic string) bool {
	if identity.Role == RoleHuman {
		return true
	}
	if strings.HasPrefix(topic, TopicWebviewBroadcastPrefix) {
		return true
	}
	if isInboxTopic(topic) {
		return true
	}
	return false
}

func isOwnInboxPattern(uid uint32, pattern string) bool {
	parts := strings.Split(pattern, ".")
	if len(parts) < 4 {
		return false
	}
	if parts[0] != "webview" || parts[1] != "inbox" {
		return false
	}
	want := strconv.FormatUint(uint64(uid), 10)
	return parts[2] == want
}

func isInboxTopic(topic string) bool {
	parts := strings.Split(topic, ".")
	if len(parts) < 4 {
		return false
	}
	if parts[0] != "webview" || parts[1] != "inbox" {
		return false
	}
	_, err := strconv.ParseUint(parts[2], 10, 32)
	return err == nil
}

func DescribeIdentity(identity Identity) string {
	if identity.Role == RoleHuman {
		return "human"
	}
	return fmt.Sprintf("agent:%d", identity.UID)
}
