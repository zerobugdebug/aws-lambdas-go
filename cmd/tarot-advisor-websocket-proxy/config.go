package main

import (
	"fmt"
	"os"
)

func LoadConfig() (Config, error) {
	cfg := Config{
		AnthropicURL:     os.Getenv(envAnthropicURL),
		AnthropicKey:     os.Getenv(envAnthropicKey),
		AnthropicModel:   os.Getenv(envAnthropicModel),
		AnthropicVersion: os.Getenv(envAnthropicVersion),
	}

	if cfg.AnthropicKey == "" {
		return cfg, fmt.Errorf("anthropic API key not found in environment variable %s", envAnthropicKey)
	}

	if cfg.AnthropicURL == "" {
		return cfg, fmt.Errorf("anthropic API URL not found in environment variable %s", envAnthropicURL)
	}

	if cfg.AnthropicModel == "" {
		cfg.AnthropicModel = defaultAnthropicModel
	}

	if cfg.AnthropicVersion == "" {
		cfg.AnthropicVersion = defaultAnthropicVersion
	}

	return cfg, nil
}
