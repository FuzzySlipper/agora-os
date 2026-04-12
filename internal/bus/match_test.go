package bus

import "testing"

func TestTopicMatch(t *testing.T) {
	tests := []struct {
		pattern string
		topic   string
		want    bool
	}{
		// Exact match.
		{"audit.file.modify", "audit.file.modify", true},
		{"audit.file.modify", "audit.file.open", false},

		// Single-segment wildcard.
		{"audit.file.*", "audit.file.modify", true},
		{"audit.file.*", "audit.file.open", true},
		{"audit.*.*", "audit.file.modify", true},
		{"*.*.*", "audit.file.modify", true},

		// Non-matching.
		{"audit.file.*", "compositor.surface.created", false},
		{"compositor.surface.*", "audit.file.modify", false},

		// Segment count mismatch.
		{"audit.file.*", "audit.file", false},
		{"audit.file", "audit.file.modify", false},
		{"audit.*", "audit.file.modify", false},

		// Single-segment topics.
		{"*", "heartbeat", true},
		{"audit", "audit", true},
		{"audit", "compositor", false},

		// Wildcard in middle.
		{"audit.*.modify", "audit.file.modify", true},
		{"audit.*.modify", "audit.net.modify", true},
		{"audit.*.modify", "audit.file.open", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"→"+tt.topic, func(t *testing.T) {
			got := TopicMatch(tt.pattern, tt.topic)
			if got != tt.want {
				t.Errorf("TopicMatch(%q, %q) = %v, want %v", tt.pattern, tt.topic, got, tt.want)
			}
		})
	}
}
