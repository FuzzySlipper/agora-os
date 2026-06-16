package compositor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/patch/agora-os/internal/schema"
)

const (
	appCommandEndpointEnv = "AGORA_APP_COMMAND_ENDPOINT"
	appCommandPortEnv     = "AGORA_APP_COMMAND_PORT"
)

func (b *Bridge) AppCommand(req schema.AppCommandRequest) (schema.AppCommandResponse, error) {
	if req.SurfaceID == "" {
		return schema.AppCommandResponse{}, fmt.Errorf("surface_id is required")
	}
	if len(bytes.TrimSpace(req.Command)) == 0 || !json.Valid(req.Command) {
		return schema.AppCommandResponse{}, fmt.Errorf("command must be valid JSON")
	}
	before, err := b.GetSurface(req.SurfaceID)
	if err != nil {
		return schema.AppCommandResponse{}, err
	}
	sessionID := req.SessionID
	if req.SessionID != "" && before.SessionID != "" && req.SessionID != before.SessionID {
		return schema.AppCommandResponse{}, compositorError(schema.ErrorSessionTokenRequired, "surface %s belongs to session %s, not %s", req.SurfaceID, before.SessionID, req.SessionID)
	}
	if sessionID == "" {
		sessionID = before.SessionID
	}
	if sessionID != "" {
		if err := b.requireSessionToken(sessionID, req.SessionToken); err != nil {
			return schema.AppCommandResponse{}, err
		}
	}
	auditID := b.sessionAuditCorrelation(sessionID, req.AuditCorrelationID)
	endpoint, err := discoverAppCommandEndpoint(int(before.Client.PID))
	if err != nil {
		return schema.AppCommandResponse{}, err
	}
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	b.mu.Lock()
	b.appCommandSeq++
	requestID := fmt.Sprintf("app-command-%d-%d", time.Now().UnixNano(), b.appCommandSeq)
	b.mu.Unlock()

	started := time.Now()
	statusCode, result, err := postAppCommand(endpoint, req.Command, requestID, timeout)
	completed := time.Now()
	if err != nil {
		return schema.AppCommandResponse{}, err
	}
	after, _ := b.GetSurface(req.SurfaceID)
	resp := schema.AppCommandResponse{
		RequestID:          requestID,
		SurfaceID:          req.SurfaceID,
		Endpoint:           endpoint,
		StatusCode:         statusCode,
		Result:             result,
		StartedAt:          started,
		CompletedAt:        completed,
		Before:             &before,
		After:              &after,
		SessionID:          sessionID,
		AuditCorrelationID: auditID,
	}
	b.mu.Lock()
	b.appCommandResults[requestID] = resp
	b.mu.Unlock()
	b.touchSession(sessionID)
	return resp, nil
}

func (b *Bridge) AppResult(req schema.AppResultRequest) (schema.AppResultResponse, error) {
	if req.RequestID == "" {
		return schema.AppResultResponse{}, fmt.Errorf("request_id is required")
	}
	b.mu.RLock()
	resp, ok := b.appCommandResults[req.RequestID]
	b.mu.RUnlock()
	if !ok {
		return schema.AppResultResponse{}, fmt.Errorf("app command result %s not found", req.RequestID)
	}
	if resp.SessionID != "" {
		if req.SessionID != "" && req.SessionID != resp.SessionID {
			return schema.AppResultResponse{}, compositorError(schema.ErrorSessionTokenRequired, "result %s belongs to session %s, not %s", req.RequestID, resp.SessionID, req.SessionID)
		}
		if err := b.requireSessionToken(resp.SessionID, req.SessionToken); err != nil {
			return schema.AppResultResponse{}, err
		}
	}
	return schema.AppResultResponse{Command: resp}, nil
}

func discoverAppCommandEndpoint(pid int) (string, error) {
	env, err := procEnv(pid)
	if err != nil {
		return "", compositorError(schema.ErrorAppCommandUnavailable, "read app command environment for pid %d: %v", pid, err)
	}
	if endpoint := strings.TrimSpace(env[appCommandEndpointEnv]); endpoint != "" {
		if err := validateLoopbackHTTP(endpoint); err != nil {
			return "", err
		}
		return endpoint, nil
	}
	if port := strings.TrimSpace(env[appCommandPortEnv]); port != "" {
		if n, err := strconv.Atoi(port); err == nil && n > 0 && n < 65536 {
			return "http://127.0.0.1:" + port + "/command", nil
		}
		return "", compositorError(schema.ErrorAppCommandUnavailable, "%s must be a TCP port, got %q", appCommandPortEnv, port)
	}
	return "", compositorError(schema.ErrorAppCommandUnavailable, "pid %d did not expose %s or %s", pid, appCommandEndpointEnv, appCommandPortEnv)
}

func validateLoopbackHTTP(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return compositorError(schema.ErrorAppCommandUnavailable, "parse %s: %v", appCommandEndpointEnv, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return compositorError(schema.ErrorAppCommandUnavailable, "%s must be http(s), got %q", appCommandEndpointEnv, parsed.Scheme)
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return compositorError(schema.ErrorAppCommandUnavailable, "%s must target a loopback host, got %q", appCommandEndpointEnv, host)
	}
	return nil
}

func procEnv(pid int) (map[string]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, part := range bytes.Split(data, []byte{0}) {
		if len(part) == 0 {
			continue
		}
		key, val, ok := strings.Cut(string(part), "=")
		if ok {
			out[key] = val
		}
	}
	return out, nil
}

func postAppCommand(endpoint string, command json.RawMessage, requestID string, timeout time.Duration) (int, json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	payload := map[string]json.RawMessage{
		"command":    command,
		"request_id": json.RawMessage(strconv.Quote(requestID)),
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, compositorError(schema.ErrorAppCommandUnavailable, "forward app command to %s: %v", endpoint, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, nil, compositorError(schema.ErrorAppCommandUnavailable, "app command endpoint returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return resp.StatusCode, json.RawMessage(`null`), nil
	}
	if !json.Valid(data) {
		data, _ = json.Marshal(map[string]string{"text": string(data)})
	}
	return resp.StatusCode, json.RawMessage(data), nil
}
