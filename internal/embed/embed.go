package embed

import (
	"context"
	"fmt"
	"time"
)

type Provider interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	Dimension() int
	Model() string
	Name() string
}

type Config struct {
	Provider  string
	Model     string
	Dimension int
	APIKey    string
	BaseURL   string
	Timeout   time.Duration
}

func New(cfg Config) (Provider, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.Dimension <= 0 {
		return nil, fmt.Errorf("embed: Dimension must be > 0 (got %d)", cfg.Dimension)
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("embed: Model is required")
	}
	switch cfg.Provider {
	case "ollama":
		return newOllama(cfg), nil
	case "openai":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("embed: openai requires APIKey")
		}
		return WithRetry(newOpenAI(cfg), 3), nil
	default:
		return nil, fmt.Errorf("embed: unknown provider %q", cfg.Provider)
	}
}
