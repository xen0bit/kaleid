package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OpenAICompatible calls a server implementing the OpenAI embeddings API
// (POST {BaseURL}/embeddings).
type OpenAICompatible struct {
	// BaseURL is the API root, e.g. "https://api.openai.com/v1".
	BaseURL string
	APIKey  string
	Model   string
	// Dimensions optionally requests shortened embeddings (OpenAI v3 models).
	Dimensions int
	// BatchSize caps texts per request (default 256).
	BatchSize int
	HTTP      *http.Client
}

// NewOpenAI uses the OpenAI API.
func NewOpenAI(apiKey, model string) *OpenAICompatible {
	return &OpenAICompatible{BaseURL: "https://api.openai.com/v1", APIKey: apiKey, Model: model}
}

// NewOllama uses a local Ollama server's OpenAI-compatible endpoint.
func NewOllama(host, model string) *OpenAICompatible {
	return &OpenAICompatible{BaseURL: strings.TrimRight(host, "/") + "/v1", APIKey: "ollama", Model: model}
}

// Name implements Embedder.
func (o *OpenAICompatible) Name() string { return o.Model }

// Embed implements Embedder.
func (o *OpenAICompatible) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	batch := o.BatchSize
	if batch <= 0 {
		batch = 256
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batch {
		end := min(start+batch, len(texts))
		vecs, err := o.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (o *OpenAICompatible) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body := map[string]any{"model": o.Model, "input": texts}
	if o.Dimensions > 0 {
		body["dimensions"] = o.Dimensions
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+"/embeddings", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embeddings API: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("embeddings API: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings API returned %d vectors for %d inputs", len(parsed.Data), len(texts))
	}
	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })
	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = d.Embedding
	}
	return out, nil
}
