package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const prompt = `Analyze this thought. Return ONLY a JSON object with these fields:
- thought_type: one of "idea", "fact", "task", "question", "decision", "observation"
- topics: 1-4 short lowercase hyphenated tags
- people: names of specific people mentioned (empty array if none)

Thought: %q

JSON only.`

type ollamaProvider struct {
	baseURL string
	model   string
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
		http:    &http.Client{Timeout: cfg.Timeout},
	}
}

func (p *ollamaProvider) Name() string  { return "ollama" }
func (p *ollamaProvider) Model() string { return p.model }

type chatReq struct {
	Model    string         `json:"model"`
	Messages []chatMsg      `json:"messages"`
	Stream   bool           `json:"stream"`
	Format   string         `json:"format,omitempty"`
	Options  map[string]any `json:"options,omitempty"`
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResp struct {
	Message chatMsg `json:"message"`
}

// Classify returns zero-value Metadata on any failure — this step is best-effort
// and must never block capture. Embedding quality is the real signal for search.
func (p *ollamaProvider) Classify(ctx context.Context, content string) (Metadata, error) {
	body, _ := json.Marshal(chatReq{
		Model:    p.model,
		Stream:   false,
		Format:   "json",
		Messages: []chatMsg{{Role: "user", Content: fmt.Sprintf(prompt, content)}},
		Options:  map[string]any{"temperature": 0.1},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Metadata{}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return Metadata{}, nil
	}
	defer res.Body.Close()
	var out chatResp
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return Metadata{}, nil
	}
	var md Metadata
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.Message.Content)), &md); err != nil {
		return Metadata{}, nil
	}
	return md, nil
}
