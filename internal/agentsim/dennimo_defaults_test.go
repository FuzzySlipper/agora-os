//go:build integration

// Package agentsim_test provides default configuration for den-nimo LAN LLM
// integration tests. The defaults live in test/phase4/dennimo.defaults.json so
// humans and agents can inspect/edit local endpoint/model assumptions without
// hunting through test code.
//
// All defaults can be overridden via environment variables:
//   - AGORA_LLM_BASE_URL  overrides the Ollama-compatible base URL
//   - AGORA_LLM_ENDPOINT  overrides the OpenAI-compatible base URL
//   - AGORA_LLM_MODEL     overrides the default model
package agentsim_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/patch/agora-os/internal/agentsim"
)

const denNimoDefaultsPath = "../../test/phase4/dennimo.defaults.json"

type denNimoDefaults struct {
	Endpoint       string  `json:"endpoint"`
	OpenAIEndpoint string  `json:"openai_endpoint"`
	DefaultModel   string  `json:"default_model"`
	GemmaModel     string  `json:"gemma_model"`
	QwenModel      string  `json:"qwen_model"`
	Temperature    float64 `json:"temperature"`
	Seed           int64   `json:"seed"`
}

func loadDenNimoDefaults() denNimoDefaults {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("resolve dennimo defaults caller")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(thisFile), denNimoDefaultsPath))
	data, err := os.ReadFile(path)
	if err != nil {
		panic("read " + path + ": " + err.Error())
	}
	var cfg denNimoDefaults
	if err := json.Unmarshal(data, &cfg); err != nil {
		panic("parse " + path + ": " + err.Error())
	}
	if cfg.Endpoint == "" || cfg.OpenAIEndpoint == "" || cfg.DefaultModel == "" || cfg.GemmaModel == "" || cfg.QwenModel == "" {
		panic("dennimo defaults config is missing required endpoint/model fields")
	}
	return cfg
}

func TestDenNimoDefaultsConfig(t *testing.T) {
	cfg := loadDenNimoDefaults()
	if cfg.Endpoint != "http://192.168.1.23:13305" {
		t.Fatalf("Endpoint = %q", cfg.Endpoint)
	}
	if cfg.OpenAIEndpoint != "http://192.168.1.23:13305/v1" {
		t.Fatalf("OpenAIEndpoint = %q", cfg.OpenAIEndpoint)
	}
	if cfg.GemmaModel == cfg.QwenModel {
		t.Fatal("expected distinct Gemma and Qwen model defaults")
	}
}

// Den-nimo LAN defaults for integration tests.
var denNimo = loadDenNimoDefaults()

var (
	// DefaultEndpoint is the base URL for the den-nimo LLM server on the LAN
	// (Ollama-compatible /api/chat endpoint).
	DefaultEndpoint = denNimo.Endpoint

	// DefaultOpenAIEndpoint is the OpenAI-compatible v1 endpoint on den-nimo.
	DefaultOpenAIEndpoint = denNimo.OpenAIEndpoint

	// DefaultModelGemma4 is the Gemma 4 26B model served by den-nimo.
	DefaultModelGemma4 = denNimo.GemmaModel

	// DefaultModelQwen35B is the Qwen 3.6 35B model served by den-nimo.
	DefaultModelQwen35B = denNimo.QwenModel
)

// getLLMConfig returns the Ollama brain config for integration tests, using
// environment overrides when set.
func getLLMConfig() agentsim.OllamaConfig {
	baseURL := os.Getenv("AGORA_LLM_BASE_URL")
	if baseURL == "" {
		baseURL = DefaultEndpoint
	}
	model := os.Getenv("AGORA_LLM_MODEL")
	if model == "" {
		model = denNimo.DefaultModel
	}
	temperature := denNimo.Temperature
	seed := denNimo.Seed
	return agentsim.OllamaConfig{
		BaseURL: baseURL,
		Model:   model,
		Options: &agentsim.OllamaOptions{
			Temperature: &temperature,
			Seed:        &seed,
		},
	}
}

// defaultOpenAIConfig returns an OpenAIConfig for the given model with den-nimo
// defaults. MaxTokens vary by model family (Qwen gets more room for reasoning).
func defaultOpenAIConfig(model string) agentsim.OpenAIConfig {
	baseURL := os.Getenv("AGORA_LLM_ENDPOINT")
	if baseURL == "" {
		baseURL = DefaultOpenAIEndpoint
	}
	t := denNimo.Temperature
	s := denNimo.Seed
	maxTokens := 1024
	if containsQwen(model) {
		maxTokens = 2048
	}
	return agentsim.OpenAIConfig{
		BaseURL:     baseURL,
		Model:       model,
		MaxTokens:   maxTokens,
		Temperature: &t,
		Seed:        &s,
	}
}

func containsQwen(s string) bool {
	for i := 0; i+3 < len(s); i++ {
		if (s[i] == 'Q' || s[i] == 'q') &&
			(s[i+1] == 'w' || s[i+1] == 'W') &&
			(s[i+2] == 'e' || s[i+2] == 'E') &&
			(s[i+3] == 'n' || s[i+3] == 'N') {
			return true
		}
	}
	return false
}
