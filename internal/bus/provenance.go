package bus

import "strings"

// Provenance policy for event-bus topic families.
//
// The bus socket is world-connectable (0666); publisher identity comes from
// kernel peer credentials (SO_PEERCRED) or from root-owned delegated proxy
// services. This file defines which UIDs are authorized to publish to, or
// subscribe to, each topic family.
//
// Terms:
//   privileged: topics that convey authoritative system state (lifecycle,
//     compositor, audit, escalation). Only trusted (uid 0 / delegated)
//     services may publish to these families.
//   open: topics any connected client may publish or subscribe to.
//   scoped: topics where authorization depends on the topic payload
//     (e.g., agent.message.* is scoped to the sender/recipient uid pair).

// privilegedPublishFamilies lists topic prefixes that only uid 0 (root) or
// delegated proxy services may publish to. Agents (uid >= AgentUIDBase)
// are blocked from publishing to these topics without a delegated override.
//
// The audit.* family covers the fanotify audit-service, the eBPF audit-ebpf
// daemon, and any future audit sources. All publish as root or through a
// root-owned delegation path.
var privilegedPublishFamilies = []string{
	"agent.lifecycle.",   // agent lifecycle events (spawned, terminated, etc.)
	"agent.work.",        // work assignment signals from supervisor
	"compositor.surface.", // compositor surface lifecycle
	"audit.",             // audit events (fanotify, eBPF)
	"admin.escalation.",  // admin agent escalation decisions
}

// privilegedSubscribeFamilies lists topic prefixes that only uid 0 (root) or
// delegated proxy services may subscribe to. These are topics where the
// payload contains sensitive authorization data.
var privilegedSubscribeFamilies = []string{
	"admin.escalation.", // escalation decisions contain authorization context
}

// isRootOrDelegated returns true if the uid is 0 (root) or the sender kind
// indicates a root-owned delegation path.
func isRootOrDelegated(uid uint32, kind SenderKind) bool {
	return uid == 0 || kind == SenderKindDelegated
}

// isAgentUID returns true if the uid falls in the Agora agent range
// (60000-61000). This is used as a conservative gate: any uid outside the
// agent range is treated as potentially trusted for subscribe ACLs.
func isAgentUID(uid uint32) bool {
	return uid >= 60000 && uid < 61000
}

// topicPrefixes returns the prefixes that match the given topic, ordered from
// most specific to least specific. For "audit.file.open", returns
// ["audit.file.open", "audit.file.", "audit."].
func topicPrefixes(topic string) []string {
	var prefixes []string
	for {
		prefixes = append(prefixes, topic)
		if dot := strings.LastIndexByte(topic, '.'); dot >= 0 {
			topic = topic[:dot]
		} else {
			break
		}
	}
	return prefixes
}

// validateTopicPublish checks whether the given uid (and optional delegated
// authority) may publish to the given topic. Returns nil if allowed,
// non-nil with a descriptive error if denied.
func validateTopicPublish(uid uint32, kind SenderKind, topic string) error {
	isRoot := isRootOrDelegated(uid, kind)

	for _, pf := range privilegedPublishFamilies {
		if strings.HasPrefix(topic, pf) {
			if !isRoot {
				return &TopicProvenanceError{
					Topic:   topic,
					Family:  pf,
					UID:     uid,
					Kind:    kind,
					Op:      "publish",
					Reason:  "privileged topic family requires root or delegated authority",
				}
			}
			return nil
		}
	}

	// Non-privileged topics are open for publish.
	return nil
}

// validateTopicSubscribe checks whether the given uid (and optional delegated
// authority) may subscribe to the given topic or pattern. Returns nil if
// allowed, non-nil with a descriptive error if denied.
func validateTopicSubscribe(uid uint32, kind SenderKind, pattern string) error {
	isRoot := isRootOrDelegated(uid, kind)

	for _, pf := range privilegedSubscribeFamilies {
		if strings.HasPrefix(pattern, pf) {
			if !isRoot {
				return &TopicProvenanceError{
					Topic:   pattern,
					Family:  pf,
					UID:     uid,
					Kind:    kind,
					Op:      "subscribe",
					Reason:  "privileged topic family requires root or delegated authority",
				}
			}
			return nil
		}
	}

	// Non-privileged topics are open for subscribe.
	return nil
}

// TopicProvenanceError describes an unauthorized bus operation.
type TopicProvenanceError struct {
	Topic   string
	Family  string
	UID     uint32
	Kind    SenderKind
	Op      string // "publish" or "subscribe"
	Reason  string
}

func (e *TopicProvenanceError) Error() string {
	return "denied " + e.Op + " on topic " + e.Topic + " (family " + e.Family + ")" +
		" by uid " + formatUID(e.UID) + "/" + string(e.Kind) +
		": " + e.Reason
}

func formatUID(uid uint32) string {
	// Avoid importing fmt in bus package for hot-path formatting.
	return string(append([]byte("0000000000")[:10-len(digits(uid))], digits(uid)...))
}

func digits(n uint32) []byte {
	if n == 0 {
		return []byte("0")
	}
	var buf [10]byte
	i := 10
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return buf[i:]
}
