package webbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/patch/agora-os/internal/bus"
)

type Gateway struct {
	BusSocket string
	Secret    []byte
	Now       func() time.Time
	Upgrader  websocket.Upgrader
}

func NewGateway(busSocket string, secret []byte) *Gateway {
	return &Gateway{
		BusSocket: busSocket,
		Secret:    secret,
		Now:       time.Now,
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ws" {
		http.NotFound(w, r)
		return
	}
	identity, err := g.authenticate(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	conn, err := g.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	busClient, err := bus.Dial(g.BusSocket)
	if err != nil {
		_ = closeInternalError(conn, "connect event bus failed")
		log.Printf("event-bus-web %s: connect event bus: %v", DescribeIdentity(identity), err)
		return
	}
	defer busClient.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	errCh := make(chan error, 2)

	go func() {
		errCh <- g.forwardOutbound(ctx, conn, busClient)
	}()
	go func() {
		errCh <- g.forwardInbound(ctx, conn, busClient, identity)
	}()

	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		log.Printf("event-bus-web %s: %v", DescribeIdentity(identity), err)
	}
}

func (g *Gateway) authenticate(r *http.Request) (Identity, error) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		authz := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(authz, "Bearer ") {
			token = strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
		}
	}
	if token == "" {
		return Identity{}, errors.New("missing token")
	}
	claims, err := VerifyToken(g.Secret, token, g.Now())
	if err != nil {
		return Identity{}, err
	}
	return claims.Identity(), nil
}

func (g *Gateway) forwardInbound(ctx context.Context, conn *websocket.Conn, busClient *bus.Client, identity Identity) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var msg bus.ClientMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			_ = closePolicyViolation(conn, "invalid message")
			return fmt.Errorf("decode websocket message: %w", err)
		}
		switch msg.Op {
		case bus.OpSub:
			if !CanSubscribe(identity, msg.Topic) {
				_ = closePolicyViolation(conn, fmt.Sprintf("subscribe not allowed on %q", msg.Topic))
				return fmt.Errorf("subscribe not allowed for %s on %q", DescribeIdentity(identity), msg.Topic)
			}
			if err := busClient.Subscribe(msg.Topic); err != nil {
				_ = closeInternalError(conn, "subscribe failed")
				return fmt.Errorf("subscribe %q: %w", msg.Topic, err)
			}
		case bus.OpUnsub:
			if err := busClient.Unsubscribe(msg.Topic); err != nil {
				_ = closeInternalError(conn, "unsubscribe failed")
				return fmt.Errorf("unsubscribe %q: %w", msg.Topic, err)
			}
		case bus.OpPub:
			if !CanPublish(identity, msg.Topic) {
				_ = closePolicyViolation(conn, fmt.Sprintf("publish not allowed on %q", msg.Topic))
				return fmt.Errorf("publish not allowed for %s on %q", DescribeIdentity(identity), msg.Topic)
			}
			var body any
			if len(msg.Body) != 0 {
				if err := json.Unmarshal(msg.Body, &body); err != nil {
					_ = closePolicyViolation(conn, "invalid publish body")
					return fmt.Errorf("decode publish body: %w", err)
				}
			}
			if err := busClient.PublishAs(identity.UID, msg.Topic, body); err != nil {
				_ = closeInternalError(conn, "publish failed")
				return fmt.Errorf("publish %q: %w", msg.Topic, err)
			}
		default:
			_ = closePolicyViolation(conn, fmt.Sprintf("unknown op %q", msg.Op))
			return fmt.Errorf("unknown op %q", msg.Op)
		}
	}
}

func (g *Gateway) forwardOutbound(ctx context.Context, conn *websocket.Conn, busClient *bus.Client) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		ev, err := busClient.Receive()
		if err != nil {
			return err
		}
		if err := conn.WriteJSON(ev); err != nil {
			return err
		}
	}
}

func closePolicyViolation(conn *websocket.Conn, reason string) error {
	return conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason),
		time.Now().Add(time.Second),
	)
}

func closeInternalError(conn *websocket.Conn, reason string) error {
	return conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseInternalServerErr, reason),
		time.Now().Add(time.Second),
	)
}
