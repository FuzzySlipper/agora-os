package llm

import (
	_ "embed"
	"encoding/json"
	"os"
)

const defaultConfigEnv = "AGORA_LLM_CONFIG"

//go:embed defaults.json
var embeddedDefaultsJSON []byte

type defaultsConfig struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
}

func loadDefaultsConfig() defaultsConfig {
	if path := os.Getenv(defaultConfigEnv); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if cfg, ok := parseDefaultsConfig(data); ok {
				return cfg
			}
		}
	}
	cfg, ok := parseDefaultsConfig(embeddedDefaultsJSON)
	if !ok {
		return defaultsConfig{}
	}
	return cfg
}

func parseDefaultsConfig(data []byte) (defaultsConfig, bool) {
	var cfg defaultsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return defaultsConfig{}, false
	}
	if cfg.Endpoint == "" || cfg.Model == "" {
		return defaultsConfig{}, false
	}
	return cfg, true
}
