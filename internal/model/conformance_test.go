package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

// The same tool-call contract must hold across official and compatible wires.
func TestProviderConformanceMatrix(t *testing.T) {
	cases := []struct {
		name, provider, wire, path, response, incomplete string
	}{
		{"anthropic", "anthropic", "", "/v1/messages", `{"content":[{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"README.md"}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":3}}`, `{"content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens"}`},
		{"anthropic-gateway", "anthropic-compatible", "", "/v1/messages", `{"content":[{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"README.md"}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":3}}`, `{"content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens"}`},
		{"openai-chat", "openai", "chat-completions", "/v1/chat/completions", `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"README.md\"}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`, `{"choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`},
		{"openai-gateway", "openai-compatible", "chat-completions", "/v1/chat/completions", `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"README.md\"}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`, `{"choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`},
		{"openai-responses", "openai", "responses", "/v1/responses", `{"status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}"}],"usage":{"input_tokens":5,"output_tokens":3}}`, `{"status":"incomplete","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := tc.response
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("wrong wire path: %s", r.URL.Path)
				}
				if r.Header.Get("Authorization") == "" && r.Header.Get("x-api-key") == "" {
					t.Error("missing provider credential")
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request["model"] != "fixture" {
					t.Errorf("wrong model: %#v", request["model"])
				}
				_, _ = w.Write([]byte(fixture))
			}))
			defer server.Close()
			registry := tools.NewRegistry([]tools.Definition{{Name: "read_file", InputSchema: map[string]any{"type": "object"}}}, tools.Metadata{})
			adapter, err := NewFromRuntime(config.Runtime{Provider: tc.provider, WireAPI: tc.wire, Model: "fixture", BaseURL: server.URL, APIKey: "test-key"}, registry)
			if err != nil {
				t.Fatal(err)
			}
			step, err := adapter.Next(context.Background(), []message.Message{message.UserMessage("read")})
			if err != nil || step.Type != message.StepToolCalls || len(step.Calls) != 1 || step.Calls[0].ID != "call_1" || step.Calls[0].ToolName != "read_file" {
				t.Fatalf("provider contract mismatch: %#v, %v", step, err)
			}
			if input, ok := step.Calls[0].Input.(map[string]any); !ok || input["path"] != "README.md" {
				t.Fatalf("tool input mismatch: %#v", step.Calls[0].Input)
			}
			if step.Diagnostics.Usage.TotalTokens != 8 {
				t.Fatalf("usage mismatch: %#v", step.Diagnostics.Usage)
			}
			fixture = tc.incomplete
			if _, err := adapter.Next(context.Background(), []message.Message{message.UserMessage("read")}); err == nil || !strings.Contains(err.Error(), "incomplete") {
				t.Fatalf("partial output was accepted: %v", err)
			}
		})
	}
}
