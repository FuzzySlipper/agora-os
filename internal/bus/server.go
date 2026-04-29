package bus

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"

	"github.com/patch/agora-os/internal/peercred"
)

// ServeConn handles one broker client connection until it closes. The peer uid
// is resolved from SO_PEERCRED up front and stamped onto all events published
// by this connection.
func ServeConn(conn net.Conn, broker *Broker) error {
	uid, err := peercred.PeerUID(conn)
	if err != nil {
		conn.Close()
		return fmt.Errorf("peer uid: %w", err)
	}

	id, events := broker.Register(uid)

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			broker.Unregister(id)
			conn.Close()
		})
	}
	defer cleanup()

	go func() {
		defer cleanup()
		dec := json.NewDecoder(conn)
		for {
			var msg ClientMsg
			if err := dec.Decode(&msg); err != nil {
				return
			}
			switch msg.Op {
			case OpPub:
				sender, delegated, err := effectivePublishSender(uid, msg)
				if err != nil {
					return
				}
				// Validate agent.message.* envelope (structural check).
				if err := validateAgentMessagePublish(sender.UID, msg.Topic, msg.Body); err != nil {
					return
				}
				// Validate topic-family provenance (ACL check).
				if err := validateTopicPublish(sender.UID, sender.Kind, msg.Topic); err != nil {
					return
				}
				if delegated {
					broker.PublishAs(id, sender, Event{Topic: msg.Topic, Body: msg.Body})
					continue
				}
				broker.Publish(id, Event{Topic: msg.Topic, Body: msg.Body})
			case OpSub:
				if err := validateAgentMessageSubscription(uid, msg.Topic); err != nil {
					return
				}
				if err := validateTopicSubscribe(uid, peerKindForUID(uid), msg.Topic); err != nil {
					return
				}
				broker.Subscribe(id, msg.Topic)
			case OpUnsub:
				broker.Unsubscribe(id, msg.Topic)
			}
		}
	}()

	enc := json.NewEncoder(conn)
	for ev := range events {
		if err := enc.Encode(ev); err != nil {
			return nil
		}
	}
	return nil
}

func effectivePublishSender(peerUID uint32, msg ClientMsg) (Sender, bool, error) {
	if peerUID != 0 || msg.SenderUID == nil {
		return Sender{UID: peerUID, Kind: SenderKindPeer}, false, nil
	}

	sender := Sender{UID: *msg.SenderUID, Kind: SenderKindDelegated}
	if msg.SenderKind != nil {
		if *msg.SenderKind != SenderKindDelegated {
			return Sender{}, false, fmt.Errorf("invalid sender kind override %q", *msg.SenderKind)
		}
		sender.Kind = *msg.SenderKind
	}
	return sender, true, nil
}

// peerKindForUID returns the SenderKind for a direct peer connection.
// uid 0 gets Peer (the connection is root; delegation is indicated via
// the ClientMsg fields, not by uid alone).
func peerKindForUID(uid uint32) SenderKind {
	return SenderKindPeer
}
