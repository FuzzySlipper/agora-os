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
				broker.Publish(id, Event{Topic: msg.Topic, Body: msg.Body})
			case OpSub:
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
