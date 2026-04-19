package classify

import (
	"context"
	"fmt"
	"time"
)

type Metadata struct {
	ThoughtType string   `json:"thought_type"`
	Topics      []string `json:"topics"`
	People      []string `json:"people"`
}

type Provider interface {
	Classify(ctx context.Context, content string) (Metadata, error)
	Name() string
	Model() string
}

type Config struct {
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
	Timeout  time.Duration
}

func New(cfg Config) (Provider, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("classify: Model is required")
	}
	switch cfg.Provider {
	case "ollama":
		return newOllama(cfg), nil
	// case "openai":    return newOpenAI(cfg), nil
	// case "anthropic": return newAnthropic(cfg), nil
	default:
		return nil, fmt.Errorf("classify: unknown provider %q", cfg.Provider)
	}
}
