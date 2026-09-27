package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatWithTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path != "/v1/chat/completions" || req["model"] != "m" || len(req["tools"].([]any)) != 1 {
			t.Errorf("unexpected request %s %v", r.URL.Path, req)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"search","arguments":"{\"query\":\"x\"}"}}]}}]}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/v1", Model: "m"}
	msg, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, []Tool{{Name: "search", Parameters: map[string]any{"type": "object"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "search" {
		t.Fatalf("got %+v", msg)
	}
}

func TestFromEnvPresets(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "gemini")
	t.Setenv("GEMINI_API_KEY", "g")
	c, err := FromEnv()
	if err != nil || c.APIKey != "g" || c.Model == "" {
		t.Fatalf("gemini preset: %+v %v", c, err)
	}
	t.Setenv("LLM_PROVIDER", "xai")
	if _, err := FromEnv(); err == nil {
		t.Fatal("xai without XAI_API_KEY must fail")
	}
	t.Setenv("LLM_PROVIDER", "ollama")
	if c, err := FromEnv(); err != nil || c.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("ollama preset: %+v %v", c, err)
	}
}
