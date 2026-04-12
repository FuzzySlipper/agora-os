package bus

import (
	"encoding/json"
	"net"
	"sync"
)

// Client connects to the event bus broker over a Unix socket.
// A single client can publish events, subscribe to topic patterns,
// and receive matching events. It is safe for concurrent use.
type Client struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
	mu   sync.Mutex // protects enc (writes)
}

// Dial connects to the event bus broker at the given Unix socket path.
func Dial(socketPath string) (*Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn: conn,
		enc:  json.NewEncoder(conn),
		dec:  json.NewDecoder(conn),
	}, nil
}

// Publish sends an event to the bus. All clients subscribed to a
// matching topic pattern will receive it (except the sender).
func (c *Client) Publish(topic string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(ClientMsg{Op: OpPub, Topic: topic, Body: b})
}

// Subscribe registers a topic pattern with the broker. The pattern
// may contain "*" wildcards matching a single dot-separated segment.
func (c *Client) Subscribe(pattern string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(ClientMsg{Op: OpSub, Topic: pattern})
}

// Unsubscribe removes a previously registered topic pattern.
func (c *Client) Unsubscribe(pattern string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(ClientMsg{Op: OpUnsub, Topic: pattern})
}

// Receive blocks until the next matching event arrives from the broker.
// Returns an error when the connection is closed.
func (c *Client) Receive() (Event, error) {
	var ev Event
	err := c.dec.Decode(&ev)
	return ev, err
}

// Close shuts down the connection to the broker.
func (c *Client) Close() error {
	return c.conn.Close()
}
