package bus

import "strings"

// TopicMatch reports whether topic matches the given pattern.
// Both are dot-separated segment strings.  The wildcard "*" in a pattern
// matches exactly one segment.  Pattern and topic must have the same
// number of segments to match.
//
// Examples:
//
//	TopicMatch("audit.file.*", "audit.file.modify")     → true
//	TopicMatch("audit.*.*",    "audit.file.modify")     → true
//	TopicMatch("audit.file.*", "compositor.surface.created") → false
//	TopicMatch("audit.file.*", "audit.file")            → false (segment count)
func TopicMatch(pattern, topic string) bool {
	pp := strings.Split(pattern, ".")
	tp := strings.Split(topic, ".")
	if len(pp) != len(tp) {
		return false
	}
	for i, seg := range pp {
		if seg != "*" && seg != tp[i] {
			return false
		}
	}
	return true
}
