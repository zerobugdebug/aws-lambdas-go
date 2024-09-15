package main

import (
	"fmt"
	"os"
)

type Config struct {
	AnthropicURL     string
	AnthropicKey     string
	AnthropicModel   string
	AnthropicVersion string
}

const (
	defaultAnthropicModel   = "claude-3-5-sonnet-20240620"
	defaultAnthropicVersion = "2023-06-01"
	envAnthropicURL         = "ANTHROPIC_URL"
	envAnthropicKey         = "ANTHROPIC_KEY"
	envAnthropicModel       = "ANTHROPIC_MODEL"
	envAnthropicVersion     = "ANTHROPIC_VERSION"
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

	if cfg.AnthropicModel == "" {
		cfg.AnthropicModel = defaultAnthropicModel
	}

	if cfg.AnthropicVersion == "" {
		cfg.AnthropicVersion = defaultAnthropicVersion
	}

	if cfg.AnthropicURL == "" {
		return cfg, fmt.Errorf("anthropic API URL not found in environment variable %s", envAnthropicURL)
	}

	return cfg, nil
}
