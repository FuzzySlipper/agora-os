package bus

import "sync"

const subscriberBufSize = 256

type subscriber struct {
	patterns map[string]bool
	ch       chan Event
}

// Broker manages topic-based pub/sub fanout. It is safe for concurrent use.
//
// Each registered client gets a buffered channel. Published events are
// delivered to clients whose subscription patterns match the event topic.
// Delivery is non-blocking: if a client's channel is full, the event is
// dropped for that client (backpressure — slow subscribers don't block
// publishers).
type Broker struct {
	mu      sync.Mutex
	clients map[int]*subscriber
	nextID  int
}

// NewBroker creates an empty broker with no clients.
func NewBroker() *Broker {
	return &Broker{
		clients: make(map[int]*subscriber),
	}
}

// Register adds a new client and returns its ID and event channel.
// The caller must eventually call Unregister to release resources.
func (b *Broker) Register() (int, <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++
	ch := make(chan Event, subscriberBufSize)
	b.clients[id] = &subscriber{
		patterns: make(map[string]bool),
		ch:       ch,
	}
	return id, ch
}

// Unregister removes a client and closes its channel. Safe to call
// multiple times or after the client has already been removed.
func (b *Broker) Unregister(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sub, ok := b.clients[id]; ok {
		close(sub.ch)
		delete(b.clients, id)
	}
}

// Subscribe registers a topic pattern for a client. The pattern may
// contain "*" wildcards matching a single dot-separated segment.
func (b *Broker) Subscribe(id int, pattern string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sub, ok := b.clients[id]; ok {
		sub.patterns[pattern] = true
	}
}

// Unsubscribe removes a topic pattern from a client.
func (b *Broker) Unsubscribe(id int, pattern string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sub, ok := b.clients[id]; ok {
		delete(sub.patterns, pattern)
	}
}

// Publish sends an event to all clients with a matching subscription,
// except the sender (identified by senderID). Non-blocking: slow
// subscribers miss events rather than stalling publishers.
func (b *Broker) Publish(senderID int, ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for id, sub := range b.clients {
		if id == senderID {
			continue
		}
		if matchesAny(sub.patterns, ev.Topic) {
			select {
			case sub.ch <- ev:
			default: // subscriber too slow — drop
			}
		}
	}
}

func matchesAny(patterns map[string]bool, topic string) bool {
	for p := range patterns {
		if TopicMatch(p, topic) {
			return true
		}
	}
	return false
}
