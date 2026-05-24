package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultEndpoint = "http://192.168.1.23:13305"
	defaultModel    = "Qwen3.6-35B-A3B-GGUF"
	chatPath        = "/v1/chat/completions"
)

// Client sends chat-completion requests to an OpenAI-compatible HTTP endpoint.
type Client struct {
	endpoint     string
	model        string
	systemPrompt string
	httpClient   *http.Client
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithEndpoint sets the base URL or full completions URL.
func WithEndpoint(endpoint string) ClientOption {
	return func(c *Client) {
		c.endpoint = endpoint
	}
}

// WithModel sets the model name.
func WithModel(model string) ClientOption {
	return func(c *Client) {
		c.model = model
	}
}

// WithSystemPrompt sets a system prompt that is injected as the first message.
func WithSystemPrompt(prompt string) ClientOption {
	return func(c *Client) {
		c.systemPrompt = prompt
	}
}

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// NewClient creates a Client with optional overrides.
// Defaults are read from AGORA_LLM_ENDPOINT and AGORA_LLM_MODEL, falling
// back to the built-in defaults.
func NewClient(opts ...ClientOption) *Client {
	endpoint := os.Getenv("AGORA_LLM_ENDPOINT")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	model := os.Getenv("AGORA_LLM_MODEL")
	if model == "" {
		model = defaultModel
	}

	c := &Client{
		endpoint:   endpoint,
		model:      model,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// chatURL returns the full chat-completions URL.
// If the endpoint already ends with /v1/chat/completions, it is used as-is.
// Otherwise /v1/chat/completions is appended to the endpoint.
func (c *Client) chatURL() string {
	if strings.HasSuffix(c.endpoint, chatPath) {
		return c.endpoint
	}
	return strings.TrimRight(c.endpoint, "/") + chatPath
}

// injectSystemPrompt prepends the system prompt to messages if one is configured.
func (c *Client) injectSystemPrompt(msgs []ChatMessage) []ChatMessage {
	if c.systemPrompt == "" {
		return msgs
	}
	out := make([]ChatMessage, 0, len(msgs)+1)
	out = append(out, ChatMessage{Role: "system", Content: c.systemPrompt})
	out = append(out, msgs...)
	return out
}

// Model returns the configured model name.
func (c *Client) Model() string {
	return c.model
}

// ChatCompletion sends a non-streaming chat-completion request and returns
// the assistant's message content and the full response.
func (c *Client) ChatCompletion(ctx context.Context, messages []ChatMessage) (*ChatCompletionResponse, error) {
	reqBody := ChatCompletionRequest{
		Model:    c.model,
		Messages: c.injectSystemPrompt(messages),
		Stream:   false,
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatURL(), bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp ChatCompletionResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &chatResp, nil
}

// ChatCompletionStream sends a streaming chat-completion request and calls
// onChunk for each incremental content token in order.
func (c *Client) ChatCompletionStream(ctx context.Context, messages []ChatMessage, onChunk func(string)) error {
	reqBody := ChatCompletionRequest{
		Model:    c.model,
		Messages: c.injectSystemPrompt(messages),
		Stream:   true,
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatURL(), bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("parse stream chunk: %w", err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				onChunk(choice.Delta.Content)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	return nil
}
