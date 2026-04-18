package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
	"github.com/patch/agora-os/internal/shellui"
)

type ackPayload struct {
	FromUID uint32 `json:"from_uid"`
	ToUID   uint32 `json:"to_uid"`
	Text    string `json:"text,omitempty"`
	Title   string `json:"title,omitempty"`
}

func main() {
	var cfg struct {
		BusSocket     string
		ShellBase     string
		HumanToken    string
		SenderUID     uint
		ReceiverUID   uint
		SenderTitle   string
		ReceiverTitle string
		ShellTitle    string
		Timeout       time.Duration
	}

	flag.StringVar(&cfg.BusSocket, "bus-socket", schema.BusSocket, "Unix socket path for the event bus")
	flag.StringVar(&cfg.ShellBase, "shell-base", "http://127.0.0.1:7780", "base URL for event-bus-web and shell APIs")
	flag.StringVar(&cfg.HumanToken, "human-token", "", "human shell token (required)")
	flag.UintVar(&cfg.SenderUID, "sender-uid", 0, "sender agent uid (required)")
	flag.UintVar(&cfg.ReceiverUID, "receiver-uid", 0, "receiver agent uid (required)")
	flag.StringVar(&cfg.SenderTitle, "sender-title", "", "sender surface title (required)")
	flag.StringVar(&cfg.ReceiverTitle, "receiver-title", "", "receiver surface title (required)")
	flag.StringVar(&cfg.ShellTitle, "shell-title", "", "human shell surface title (required)")
	flag.DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "overall probe timeout")
	flag.Parse()

	if cfg.HumanToken == "" || cfg.SenderUID == 0 || cfg.ReceiverUID == 0 || cfg.SenderTitle == "" || cfg.ReceiverTitle == "" || cfg.ShellTitle == "" {
		fmt.Fprintln(os.Stderr, "error: --human-token, --sender-uid, --receiver-uid, --sender-title, --receiver-title, and --shell-title are required")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	if err := waitForWebviewMessage(ctx, cfg.BusSocket, uint32(cfg.SenderUID), uint32(cfg.ReceiverUID)); err != nil {
		failf("phase3 bus message", err)
	}
	fmt.Println("ok: observed delegated sender chat and receiver acknowledgement over event-bus-web")

	if err := waitForShellState(ctx, cfg.ShellBase, cfg.HumanToken, uint32(cfg.SenderUID), uint32(cfg.ReceiverUID), cfg.SenderTitle, cfg.ReceiverTitle, cfg.ShellTitle); err != nil {
		failf("shell state", err)
	}
	fmt.Println("ok: shell state shows both agents and the expected Wayland surfaces")

	if err := waitForAuditEvents(ctx, cfg.ShellBase, cfg.HumanToken, uint32(cfg.SenderUID), uint32(cfg.ReceiverUID)); err != nil {
		failf("shell audit stream", err)
	}
	fmt.Println("ok: shell audit stream observed recent activity for both agent uids")
}

func failf(label string, err error) {
	fmt.Fprintf(os.Stderr, "error: %s: %v\n", label, err)
	os.Exit(1)
}

func waitForWebviewMessage(ctx context.Context, sock string, senderUID, receiverUID uint32) error {
	client, err := bus.Dial(sock)
	if err != nil {
		return fmt.Errorf("dial bus: %w", err)
	}
	defer client.Close()

	topics := []string{
		schema.AgentMessageTopic(senderUID, receiverUID, "chat"),
		"webview.broadcast.phase3.ack",
	}
	for _, topic := range topics {
		if err := client.Subscribe(topic); err != nil {
			return fmt.Errorf("subscribe %s: %w", topic, err)
		}
	}

	type receiveResult struct {
		event bus.Event
		err   error
	}
	results := make(chan receiveResult)
	go func() {
		for {
			ev, err := client.Receive()
			results <- receiveResult{event: ev, err: err}
			if err != nil {
				return
			}
		}
	}()

	sawChat := false
	sawAck := false
	var lastErr error

	for !sawChat || !sawAck {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("timed out after receive error %v", lastErr)
			}
			return fmt.Errorf("timed out waiting for chat=%t ack=%t", sawChat, sawAck)
		case result := <-results:
			if result.err != nil {
				lastErr = result.err
				continue
			}
			switch result.event.Topic {
			case schema.AgentMessageTopic(senderUID, receiverUID, "chat"):
				if result.event.Sender == nil || result.event.Sender.UID != senderUID || result.event.Sender.Kind != bus.SenderKindDelegated {
					return fmt.Errorf("chat sender metadata mismatch: %+v", result.event.Sender)
				}
				var envelope schema.AgentMessageEnvelope
				if err := json.Unmarshal(result.event.Body, &envelope); err != nil {
					return fmt.Errorf("decode chat envelope: %w", err)
				}
				if envelope.FromUID != senderUID || envelope.ToUID != receiverUID || envelope.Kind != "chat" {
					return fmt.Errorf("unexpected chat envelope %+v", envelope)
				}
				sawChat = true
			case "webview.broadcast.phase3.ack":
				if result.event.Sender == nil || result.event.Sender.UID != receiverUID || result.event.Sender.Kind != bus.SenderKindDelegated {
					return fmt.Errorf("ack sender metadata mismatch: %+v", result.event.Sender)
				}
				var ack ackPayload
				if err := json.Unmarshal(result.event.Body, &ack); err != nil {
					return fmt.Errorf("decode ack payload: %w", err)
				}
				if ack.FromUID != receiverUID || ack.ToUID != senderUID {
					return fmt.Errorf("unexpected ack payload %+v", ack)
				}
				sawAck = true
			}
		}
	}

	return nil
}

func waitForShellState(ctx context.Context, shellBase, token string, senderUID, receiverUID uint32, senderTitle, receiverTitle, shellTitle string) error {
	deadline := time.Now().Add(250 * time.Millisecond)
	var lastErr error

	for {
		state, err := fetchShellState(ctx, shellBase, token)
		if err == nil {
			if containsAgent(state.Agents, senderUID) &&
				containsAgent(state.Agents, receiverUID) &&
				containsSurface(state.Surfaces, senderUID, senderTitle) &&
				containsSurface(state.Surfaces, receiverUID, receiverTitle) &&
				containsSurface(state.Surfaces, 0, shellTitle) {
				return nil
			}
		} else {
			lastErr = err
		}

		if err := sleepUntilNextPoll(ctx, &deadline); err != nil {
			if lastErr != nil {
				return fmt.Errorf("last fetch error: %w", lastErr)
			}
			return fmt.Errorf("timed out waiting for shell state")
		}
	}
}

func fetchShellState(ctx context.Context, shellBase, token string) (shellui.State, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(shellBase, "/")+"/api/shell/state", nil)
	if err != nil {
		return shellui.State{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return shellui.State{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return shellui.State{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var state shellui.State
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return shellui.State{}, err
	}
	return state, nil
}

func waitForAuditEvents(ctx context.Context, shellBase, token string, senderUID, receiverUID uint32) error {
	conn, err := dialShellWebSocket(ctx, shellBase, "/api/shell/audit/ws", token)
	if err != nil {
		return err
	}
	defer conn.Close()

	sawSender := false
	sawReceiver := false
	for !sawSender || !sawReceiver {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return fmt.Errorf("audit websocket closed before both events arrived")
			}
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				select {
				case <-ctx.Done():
					return fmt.Errorf("timed out waiting for audit events for %d and %d", senderUID, receiverUID)
				default:
					continue
				}
			}
			return err
		}

		var event schema.AuditEvent
		if err := json.Unmarshal(msg, &event); err != nil {
			return fmt.Errorf("decode audit event: %w", err)
		}
		if event.AgentUID == senderUID {
			sawSender = true
		}
		if event.AgentUID == receiverUID {
			sawReceiver = true
		}
	}
	return nil
}

func dialShellWebSocket(ctx context.Context, shellBase, path, token string) (*websocket.Conn, error) {
	base, err := url.Parse(shellBase)
	if err != nil {
		return nil, err
	}

	scheme := "ws"
	if strings.EqualFold(base.Scheme, "https") {
		scheme = "wss"
	}
	wsURL := url.URL{
		Scheme: scheme,
		Host:   base.Host,
		Path:   path,
	}

	dialer := websocket.Dialer{
		Subprotocols:     []string{"agora.token." + token},
		HandshakeTimeout: 2 * time.Second,
	}

	var lastErr error
	for {
		conn, _, err := dialer.DialContext(ctx, wsURL.String(), nil)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
			return nil, fmt.Errorf("dial websocket: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dial websocket: %w", lastErr)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func containsAgent(agents []schema.AgentInfo, uid uint32) bool {
	for _, agent := range agents {
		if agent.UID == uid {
			return true
		}
	}
	return false
}

func containsSurface(surfaces []schema.CompositorTrackedSurface, ownerUID uint32, title string) bool {
	for _, surface := range surfaces {
		if surface.Client.UID == ownerUID && surface.Surface.Title == title {
			return true
		}
	}
	return false
}

func sleepUntilNextPoll(ctx context.Context, next *time.Time) error {
	delay := time.Until(*next)
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		*next = time.Now().Add(250 * time.Millisecond)
		return nil
	}
}
