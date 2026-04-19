package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type openaiProvider struct {
	baseURL string
	model   string
	apiKey  string
	dim     int
	http    *http.Client
}

func newOpenAI(cfg Config) *openaiProvider {
	base := cfg.BaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return &openaiProvider{
		baseURL: base,
		model:   cfg.Model,
		apiKey:  cfg.APIKey,
		dim:     cfg.Dimension,
		http:    &http.Client{Timeout: cfg.Timeout},
	}
}

func (p *openaiProvider) Name() string   { return "openai" }
func (p *openaiProvider) Model() string  { return p.model }
func (p *openaiProvider) Dimension() int { return p.dim }

type openaiReq struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type openaiResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func (p *openaiProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(openaiReq{Model: p.model, Input: texts, Dimensions: p.dim})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")

	res, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	var out openaiResp
	_ = json.NewDecoder(res.Body).Decode(&out)

	if res.StatusCode != 200 {
		msg := res.Status
		if out.Error != nil {
			msg = out.Error.Message
		}
		// 4xx (except 429) is a config mistake — don't burn retries on it.
		if res.StatusCode >= 400 && res.StatusCode < 500 && res.StatusCode != 429 {
			return nil, &NonRetryableError{Err: fmt.Errorf("openai embed %d: %s", res.StatusCode, msg)}
		}
		return nil, fmt.Errorf("openai embed %d: %s", res.StatusCode, msg)
	}

	// OpenAI may return data out of order; index into result slice.
	result := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index >= 0 && d.Index < len(texts) {
			result[d.Index] = d.Embedding
		}
	}
	if len(result) > 0 && len(result[0]) != p.dim {
		return nil, fmt.Errorf("openai returned dim %d, config says %d", len(result[0]), p.dim)
	}
	return result, nil
}

func (p *openaiProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	out, err := p.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}
