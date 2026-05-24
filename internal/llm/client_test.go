package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/patch/agora-os/internal/llm"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func resetEnv() func() {
	oldEndpoint := os.Getenv("AGORA_LLM_ENDPOINT")
	oldModel := os.Getenv("AGORA_LLM_MODEL")
	return func() {
		os.Setenv("AGORA_LLM_ENDPOINT", oldEndpoint)
		os.Setenv("AGORA_LLM_MODEL", oldModel)
	}
}

func assertNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertErrContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// Env defaults / overrides
// ---------------------------------------------------------------------------

func TestNewClient_EnvDefaults(t *testing.T) {
	cleanup := resetEnv()
	defer cleanup()

	os.Unsetenv("AGORA_LLM_ENDPOINT")
	os.Unsetenv("AGORA_LLM_MODEL")

	c := llm.NewClient()
	// We cannot directly read private fields, so exercise the client via a
	// httptest server and verify the default model is sent.
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatCompletionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(llm.ChatCompletionResponse{
			Choices: []llm.Choice{{Message: llm.ChatMessage{Role: "assistant", Content: "ok"}}},
		})
	}))
	defer srv.Close()

	c = llm.NewClient(llm.WithEndpoint(srv.URL))
	_, err := c.ChatCompletion(context.Background(), nil)
	assertNoErr(t, err)
	if gotModel != "Qwen3.6-35B-A3B-GGUF" {
		t.Errorf("model = %q, want default", gotModel)
	}
}

func TestNewClient_EnvOverrides(t *testing.T) {
	cleanup := resetEnv()
	defer cleanup()

	os.Setenv("AGORA_LLM_ENDPOINT", "http://env-endpoint.example:9999")
	os.Setenv("AGORA_LLM_MODEL", "env-model")

	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatCompletionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(llm.ChatCompletionResponse{
			Choices: []llm.Choice{{Message: llm.ChatMessage{Role: "assistant", Content: "ok"}}},
		})
	}))
	defer srv.Close()

	// Override endpoint via option so we can test with httptest, but model
	// should come from env.
	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	_, err := c.ChatCompletion(context.Background(), nil)
	assertNoErr(t, err)
	if gotModel != "env-model" {
		t.Errorf("model = %q, want env-model", gotModel)
	}
}

func TestNewClient_OptionOverrides(t *testing.T) {
	cleanup := resetEnv()
	defer cleanup()

	os.Setenv("AGORA_LLM_MODEL", "env-model")
	defer cleanup()

	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatCompletionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(llm.ChatCompletionResponse{
			Choices: []llm.Choice{{Message: llm.ChatMessage{Role: "assistant", Content: "ok"}}},
		})
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL), llm.WithModel("option-model"))
	_, err := c.ChatCompletion(context.Background(), nil)
	assertNoErr(t, err)
	if gotModel != "option-model" {
		t.Errorf("model = %q, want option-model", gotModel)
	}
}

// ---------------------------------------------------------------------------
// Request shape and system-prompt injection ordering
// ---------------------------------------------------------------------------

func TestChatCompletion_RequestShape(t *testing.T) {
	var gotReq llm.ChatCompletionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(llm.ChatCompletionResponse{
			Choices: []llm.Choice{{
				Message: llm.ChatMessage{Role: "assistant", Content: "hi"},
			}},
		})
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL), llm.WithModel("test-model"))
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "hello"},
	}
	_, err := c.ChatCompletion(context.Background(), msgs)
	assertNoErr(t, err)

	if gotReq.Model != "test-model" {
		t.Errorf("model = %q, want test-model", gotReq.Model)
	}
	if gotReq.Stream {
		t.Error("stream should be false for non-streaming")
	}
	if len(gotReq.Messages) != 1 || gotReq.Messages[0].Role != "user" {
		t.Errorf("messages = %+v, want single user message", gotReq.Messages)
	}
}

func TestChatCompletion_SystemPromptInjection(t *testing.T) {
	var gotReq llm.ChatCompletionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(llm.ChatCompletionResponse{
			Choices: []llm.Choice{{
				Message: llm.ChatMessage{Role: "assistant", Content: "ok"},
			}},
		})
	}))
	defer srv.Close()

	c := llm.NewClient(
		llm.WithEndpoint(srv.URL),
		llm.WithSystemPrompt("You are a test assistant."),
	)
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "hello"},
	}
	_, err := c.ChatCompletion(context.Background(), msgs)
	assertNoErr(t, err)

	if len(gotReq.Messages) != 2 {
		t.Fatalf("messages count = %d, want 2", len(gotReq.Messages))
	}
	if gotReq.Messages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", gotReq.Messages[0].Role)
	}
	if gotReq.Messages[0].Content != "You are a test assistant." {
		t.Errorf("system prompt = %q, want injected prompt", gotReq.Messages[0].Content)
	}
	if gotReq.Messages[1].Role != "user" {
		t.Errorf("second message role = %q, want user", gotReq.Messages[1].Role)
	}
}

// ---------------------------------------------------------------------------
// Response parsing
// ---------------------------------------------------------------------------

func TestChatCompletion_ResponseParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(llm.ChatCompletionResponse{
			ID:      "resp-123",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "test-model",
			Choices: []llm.Choice{{
				Index:        0,
				Message:      llm.ChatMessage{Role: "assistant", Content: "The answer is 42."},
				FinishReason: strPtr("stop"),
			}},
			Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		})
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	resp, err := c.ChatCompletion(context.Background(), []llm.ChatMessage{{Role: "user", Content: "question"}})
	assertNoErr(t, err)

	if resp.ID != "resp-123" {
		t.Errorf("id = %q, want resp-123", resp.ID)
	}
	if resp.Choices[0].Message.Content != "The answer is 42." {
		t.Errorf("content = %q", resp.Choices[0].Message.Content)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 15 {
		t.Errorf("usage = %+v, want 15 total tokens", resp.Usage)
	}
}

// ---------------------------------------------------------------------------
// Streaming
// ---------------------------------------------------------------------------

func TestChatCompletionStream_BasicChunks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatCompletionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream {
			t.Error("stream should be true")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("cannot flush")
		}

		chunks := []string{
			`{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
			`{"id":"2","object":"chat.completion.chunk","created":2,"model":"m","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
			`{"id":"3","object":"chat.completion.chunk","created":3,"model":"m","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
			`{"id":"4","object":"chat.completion.chunk","created":4,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		}
		for _, ch := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", ch)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	var parts []string
	err := c.ChatCompletionStream(context.Background(), []llm.ChatMessage{{Role: "user", Content: "hi"}}, func(tok string) {
		parts = append(parts, tok)
	})
	assertNoErr(t, err)

	got := strings.Join(parts, "")
	if got != "Hello world" {
		t.Errorf("tokens = %q, want %q", got, "Hello world")
	}
}

func TestChatCompletionStream_SystemPromptInjection(t *testing.T) {
	var gotReq llm.ChatCompletionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", `{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`)
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := llm.NewClient(
		llm.WithEndpoint(srv.URL),
		llm.WithSystemPrompt("sys"),
	)
	err := c.ChatCompletionStream(context.Background(), []llm.ChatMessage{{Role: "user", Content: "hi"}}, func(string) {})
	assertNoErr(t, err)

	if len(gotReq.Messages) != 2 || gotReq.Messages[0].Role != "system" {
		t.Errorf("messages = %+v, want system prepended", gotReq.Messages)
	}
}

func TestChatCompletionStream_MultipleChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		chunk := `{"id":"1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"A"},"finish_reason":null},{"index":1,"delta":{"content":"B"},"finish_reason":null}]}`
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	var parts []string
	err := c.ChatCompletionStream(context.Background(), nil, func(tok string) {
		parts = append(parts, tok)
	})
	assertNoErr(t, err)

	if len(parts) != 2 {
		t.Fatalf("parts = %v, want 2", parts)
	}
	if parts[0] != "A" || parts[1] != "B" {
		t.Errorf("parts = %v, want [A B]", parts)
	}
}

func TestChatCompletionStream_EmptyDeltaContentIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", `{"id":"1","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		flusher.Flush()
		fmt.Fprintf(w, "data: %s\n\n", `{"id":"2","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}`)
		flusher.Flush()
		fmt.Fprintf(w, "data: %s\n\n", `{"id":"3","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`)
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	var parts []string
	err := c.ChatCompletionStream(context.Background(), nil, func(tok string) {
		parts = append(parts, tok)
	})
	assertNoErr(t, err)

	if len(parts) != 1 || parts[0] != "x" {
		t.Errorf("parts = %v, want [x]", parts)
	}
}

// ---------------------------------------------------------------------------
// Non-2xx responses
// ---------------------------------------------------------------------------

func TestChatCompletion_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"backend failure"}`))
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	_, err := c.ChatCompletion(context.Background(), nil)
	assertErrContains(t, err, "500")
	assertErrContains(t, err, "backend failure")
}

func TestChatCompletionStream_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`overloaded`))
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	err := c.ChatCompletionStream(context.Background(), nil, func(string) {})
	assertErrContains(t, err, "503")
	assertErrContains(t, err, "overloaded")
}

// ---------------------------------------------------------------------------
// Connection failures
// ---------------------------------------------------------------------------

func TestChatCompletion_ConnectionRefused(t *testing.T) {
	c := llm.NewClient(
		llm.WithEndpoint("http://127.0.0.1:59999"),
		llm.WithHTTPClient(&http.Client{Timeout: 500 * time.Millisecond}),
	)
	_, err := c.ChatCompletion(context.Background(), nil)
	assertErrContains(t, err, "request failed")
}

func TestChatCompletionStream_ConnectionRefused(t *testing.T) {
	c := llm.NewClient(
		llm.WithEndpoint("http://127.0.0.1:59999"),
		llm.WithHTTPClient(&http.Client{Timeout: 500 * time.Millisecond}),
	)
	err := c.ChatCompletionStream(context.Background(), nil, func(string) {})
	assertErrContains(t, err, "request failed")
}

// ---------------------------------------------------------------------------
// Malformed JSON
// ---------------------------------------------------------------------------

func TestChatCompletion_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{not json`))
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	_, err := c.ChatCompletion(context.Background(), nil)
	assertErrContains(t, err, "parse response")
}

func TestChatCompletionStream_MalformedChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {bad json\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	err := c.ChatCompletionStream(context.Background(), nil, func(string) {})
	assertErrContains(t, err, "parse stream chunk")
}

// ---------------------------------------------------------------------------
// Bad / missing streaming events
// ---------------------------------------------------------------------------

func TestChatCompletionStream_NoDoneMarker(t *testing.T) {
	// Server sends one chunk and then closes without [DONE].
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", `{"id":"1","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":null}]}`)
		flusher.Flush()
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	var parts []string
	err := c.ChatCompletionStream(context.Background(), nil, func(tok string) {
		parts = append(parts, tok)
	})
	assertNoErr(t, err)
	if len(parts) != 1 || parts[0] != "done" {
		t.Errorf("parts = %v, want [done]", parts)
	}
}

func TestChatCompletionStream_IgnoresNonDataLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, ": ping\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: %s\n\n", `{"id":"1","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`)
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	var parts []string
	err := c.ChatCompletionStream(context.Background(), nil, func(tok string) {
		parts = append(parts, tok)
	})
	assertNoErr(t, err)
	if len(parts) != 1 || parts[0] != "x" {
		t.Errorf("parts = %v, want [x]", parts)
	}
}

func TestChatCompletionStream_EmptyDataLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: \n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	err := c.ChatCompletionStream(context.Background(), nil, func(string) {})
	assertNoErr(t, err)
}

// ---------------------------------------------------------------------------
// Endpoint URL handling
// ---------------------------------------------------------------------------

func TestChatCompletion_FullURLAsEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(llm.ChatCompletionResponse{
			Choices: []llm.Choice{{Message: llm.ChatMessage{Role: "assistant", Content: "ok"}}},
		})
	}))
	defer srv.Close()

	fullURL := srv.URL + "/v1/chat/completions"
	c := llm.NewClient(llm.WithEndpoint(fullURL))
	_, err := c.ChatCompletion(context.Background(), nil)
	assertNoErr(t, err)
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
}

func TestChatCompletion_BaseURLAppendsPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(llm.ChatCompletionResponse{
			Choices: []llm.Choice{{Message: llm.ChatMessage{Role: "assistant", Content: "ok"}}},
		})
	}))
	defer srv.Close()

	c := llm.NewClient(llm.WithEndpoint(srv.URL))
	_, err := c.ChatCompletion(context.Background(), nil)
	assertNoErr(t, err)
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
}

func strPtr(s string) *string {
	return &s
}
