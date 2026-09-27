// Package llm is a minimal OpenAI-compatible chat-completions client with
// tool calling, used by the examples and sample apps. OpenAI, Ollama,
// Google Gemini and xAI all expose this API.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Message is a chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a model's request to call a tool.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool describes a callable function (JSON Schema parameters).
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Client calls {BaseURL}/chat/completions.
type Client struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	HTTP     *http.Client
}

type preset struct {
	baseURL, keyEnv, model string
}

var presets = map[string]preset{
	"openai": {"https://api.openai.com/v1", "OPENAI_API_KEY", "gpt-4o-mini"},
	"ollama": {"", "", "llama3.2"},
	"gemini": {"https://generativelanguage.googleapis.com/v1beta/openai", "GEMINI_API_KEY", "gemini-2.0-flash"},
	"xai":    {"https://api.x.ai/v1", "XAI_API_KEY", "grok-3-mini"},
}

// FromEnv configures a client:
//
//	LLM_PROVIDER  openai (default) | ollama | gemini | xai | openai-compatible
//	LLM_MODEL     overrides the provider's default model
//	LLM_BASE_URL  required for openai-compatible
//	LLM_API_KEY   overrides the provider's key variable
//	              (OPENAI_API_KEY, GEMINI_API_KEY, XAI_API_KEY)
//	OLLAMA_HOST   for ollama (default http://localhost:11434)
func FromEnv() (*Client, error) {
	provider := os.Getenv("LLM_PROVIDER")
	if provider == "" {
		provider = "openai"
	}
	c := &Client{Provider: provider, Model: os.Getenv("LLM_MODEL"), APIKey: os.Getenv("LLM_API_KEY")}
	if provider == "openai-compatible" {
		c.BaseURL = os.Getenv("LLM_BASE_URL")
		if c.BaseURL == "" || c.Model == "" {
			return nil, fmt.Errorf("LLM_PROVIDER=openai-compatible requires LLM_BASE_URL and LLM_MODEL")
		}
		return c, nil
	}
	p, ok := presets[provider]
	if !ok {
		return nil, fmt.Errorf("unknown LLM_PROVIDER %q", provider)
	}
	c.BaseURL = p.baseURL
	if provider == "ollama" {
		host := os.Getenv("OLLAMA_HOST")
		if host == "" {
			host = "http://localhost:11434"
		}
		c.BaseURL = strings.TrimRight(host, "/") + "/v1"
		c.APIKey = "ollama"
	}
	if c.Model == "" {
		c.Model = p.model
	}
	if c.APIKey == "" && p.keyEnv != "" {
		c.APIKey = os.Getenv(p.keyEnv)
		if c.APIKey == "" {
			return nil, fmt.Errorf("LLM_PROVIDER=%s requires %s", provider, p.keyEnv)
		}
	}
	return c, nil
}

// Chat sends messages (and optional tools) and returns the model's reply.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool) (Message, error) {
	body := map[string]any{"model": c.Model, "messages": messages}
	if len(tools) > 0 {
		ts := make([]map[string]any, len(tools))
		for i, t := range tools {
			ts[i] = map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Parameters,
			}}
		}
		body["tools"] = ts
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 180 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return Message{}, fmt.Errorf("%s chat API: HTTP %d: %s", c.Provider, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Message{}, err
	}
	if len(parsed.Choices) == 0 {
		return Message{}, fmt.Errorf("%s chat API returned no choices", c.Provider)
	}
	return parsed.Choices[0].Message, nil
}

// Complete is a convenience for a single system+user exchange.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	msg, err := c.Chat(ctx, []Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, nil)
	return strings.TrimSpace(msg.Content), err
}
