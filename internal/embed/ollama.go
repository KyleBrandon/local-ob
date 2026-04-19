package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type ollamaProvider struct {
	baseURL string
	model   string
	dim     int
	http    *http.Client
}

func newOllama(cfg Config) *ollamaProvider {
	base := cfg.BaseURL
	if base == "" {
		base = "http://localhost:11434"
	}
	return &ollamaProvider{
		baseURL: base,
		model:   cfg.Model,
		dim:     cfg.Dimension,
		http:    &http.Client{Timeout: cfg.Timeout},
	}
}

func (p *ollamaProvider) Name() string   { return "ollama" }
func (p *ollamaProvider) Model() string  { return p.model }
func (p *ollamaProvider) Dimension() int { return p.dim }

type ollamaReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaResp struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func (p *ollamaProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(ollamaReq{Model: p.model, Input: texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		msg, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("ollama embed: status %d: %s", res.StatusCode, string(msg))
	}
	var out ollamaResp
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama returned %d embeddings for %d inputs", len(out.Embeddings), len(texts))
	}
	if len(out.Embeddings) > 0 && len(out.Embeddings[0]) != p.dim {
		return nil, fmt.Errorf("ollama model %s returned dim %d, config says %d",
			p.model, len(out.Embeddings[0]), p.dim)
	}
	return out.Embeddings, nil
}

func (p *ollamaProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	out, err := p.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}
