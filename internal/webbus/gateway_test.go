package webbus

import (
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/patch/agora-os/internal/bus"
)

func TestGatewayPublishesAuthenticatedUID(t *testing.T) {
	t.Parallel()

	if os.Getuid() != 0 {
		t.Skip("requires root because the gateway publishes through a trusted root-owned bus proxy")
	}

	sock := startTestBus(t)
	secret := []byte("01234567890123456789012345678901")
	now := time.Now().UTC().Truncate(time.Second)
	token, err := MintToken(secret, Claims{Role: RoleAgent, UID: 60001, Exp: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}

	gateway := NewGateway(sock, secret)
	gateway.Now = func() time.Time { return now }
	server := httptest.NewServer(gateway)
	defer server.Close()

	subscriber, err := bus.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer subscriber.Close()
	if err := subscriber.Subscribe("webview.inbox.*.*"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	conn := dialGateway(t, server.URL, token)
	defer conn.Close()

	payload := map[string]any{"hello": "world"}
	body, _ := json.Marshal(payload)
	msg := bus.ClientMsg{Op: bus.OpPub, Topic: "webview.inbox.60002.chat", Body: body}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatal(err)
	}

	ev, err := subscriber.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Sender == nil || ev.Sender.UID != 60001 {
		t.Fatalf("got sender %+v, want uid 60001", ev.Sender)
	}
	if ev.Topic != "webview.inbox.60002.chat" {
		t.Fatalf("got topic %q", ev.Topic)
	}
}

func TestGatewayRejectsUnauthorizedSubscription(t *testing.T) {
	t.Parallel()

	sock := startTestBus(t)
	secret := []byte("01234567890123456789012345678901")
	now := time.Now().UTC().Truncate(time.Second)
	token, err := MintToken(secret, Claims{Role: RoleAgent, UID: 60001, Exp: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}

	gateway := NewGateway(sock, secret)
	gateway.Now = func() time.Time { return now }
	server := httptest.NewServer(gateway)
	defer server.Close()

	conn := dialGateway(t, server.URL, token)
	defer conn.Close()

	if err := conn.WriteJSON(bus.ClientMsg{Op: bus.OpSub, Topic: "audit.file.*"}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("expected unauthorized subscription to close the websocket")
	}
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("got error %v, want websocket close error", err)
	}
	if closeErr.Code != websocket.ClosePolicyViolation {
		t.Fatalf("got close code %d, want %d", closeErr.Code, websocket.ClosePolicyViolation)
	}
}

func startTestBus(t *testing.T) string {
	t.Helper()

	sock := filepath.Join(t.TempDir(), "bus.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	broker := bus.NewBroker()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _ = bus.ServeConn(c, broker) }(conn)
		}
	}()

	return sock
}

func dialGateway(t *testing.T, serverURL, token string) *websocket.Conn {
	t.Helper()

	wsURL := "ws" + serverURL[len("http"):] + "/ws?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}
