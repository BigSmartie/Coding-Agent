package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

func TestParseAssistantMarkers(t *testing.T) {
	content, kind := ParseAssistantText("<final>\ndone\n</final>")
	if content != "done" || kind != message.ContentFinal {
		t.Fatalf("unexpected parse: %q %q", content, kind)
	}
}

func TestNewFromRuntimeSelectsProvider(t *testing.T) {
	registry := tools.NewRegistry(nil, tools.Metadata{})
	anthropic, err := NewFromRuntime(config.Runtime{Provider: "anthropic", Model: "claude", BaseURL: "https://example.test", APIKey: "key"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := anthropic.(*Anthropic); !ok {
		t.Fatalf("expected Anthropic adapter, got %T", anthropic)
	}

	openai, err := NewFromRuntime(config.Runtime{Provider: "openai", Model: "gpt", BaseURL: "https://example.test", APIKey: "key"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := openai.(*OpenAI); !ok {
		t.Fatalf("expected OpenAI adapter, got %T", openai)
	}

	if _, err := NewFromRuntime(config.Runtime{Provider: "unknown", Model: "x"}, registry); err == nil || !strings.Contains(err.Error(), "unsupported model provider") {
		t.Fatalf("expected unsupported provider error, got %v", err)
	}
}

func TestErrorModelReturnsConfigurationError(t *testing.T) {
	step, err := (ErrorModel{Err: errors.New("missing config")}).Next(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "missing config") {
		t.Fatalf("expected configuration error, got step=%#v err=%v", step, err)
	}
}

func TestOpenAIAdapterParsesToolUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer openai-key" {
			t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-test" {
			t.Fatalf("unexpected request body: %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"finish_reason": "tool_calls",
					"message": map[string]any{
						"role":    "assistant",
						"content": "<progress>checking",
						"tool_calls": []map[string]any{
							{
								"id":   "call_1",
								"type": "function",
								"function": map[string]any{
									"name":      "read_file",
									"arguments": `{"path":"README.md"}`,
								},
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	adapter := NewOpenAI(func(context.Context) (config.Runtime, error) {
		return config.Runtime{Provider: "openai", Model: "gpt-test", BaseURL: server.URL, APIKey: "openai-key"}, nil
	}, tools.NewRegistry([]tools.Definition{{Name: "read_file", Description: "read", InputSchema: map[string]any{"type": "object"}}}, tools.Metadata{}))

	step, err := adapter.Next(context.Background(), []message.Message{message.SystemMessage("sys"), message.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if step.Type != message.StepToolCalls || len(step.Calls) != 1 || step.Calls[0].ToolName != "read_file" || step.ContentKind != message.ContentProgress {
		t.Fatalf("unexpected step: %#v", step)
	}
	input, ok := step.Calls[0].Input.(map[string]any)
	if !ok || input["path"] != "README.md" {
		t.Fatalf("unexpected tool input: %#v", step.Calls[0].Input)
	}
	if step.Diagnostics.StopReason != "tool_calls" {
		t.Fatalf("unexpected diagnostics: %#v", step.Diagnostics)
	}
}

func TestOpenAIResponsesAdapterParsesToolUseAndSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer openai-key" {
			t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-5.5" {
			t.Fatalf("unexpected request body: %#v", body)
		}
		if body["store"] != false {
			t.Fatalf("expected store=false, got %#v", body["store"])
		}
		reasoning, ok := body["reasoning"].(map[string]any)
		if !ok || reasoning["effort"] != "high" {
			t.Fatalf("expected normalized reasoning effort, got %#v", body["reasoning"])
		}
		toolsBody, ok := body["tools"].([]any)
		if !ok || len(toolsBody) != 1 {
			t.Fatalf("expected tools payload, got %#v", body["tools"])
		}
		input, ok := body["input"].([]any)
		if !ok || len(input) != 2 {
			t.Fatalf("unexpected input payload: %#v", body["input"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": []map[string]any{
				{"type": "reasoning"},
				{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": "<progress>checking"},
					},
				},
				{
					"type":      "function_call",
					"id":        "call_1",
					"name":      "read_file",
					"arguments": `{"path":"README.md"}`,
				},
			},
		})
	}))
	defer server.Close()

	adapter := NewOpenAI(func(context.Context) (config.Runtime, error) {
		return config.Runtime{
			Provider:               "openai",
			Model:                  "gpt-5.5",
			BaseURL:                server.URL,
			APIKey:                 "openai-key",
			WireAPI:                "responses",
			ReasoningEffort:        "xhigh",
			DisableResponseStorage: true,
		}, nil
	}, tools.NewRegistry([]tools.Definition{{Name: "read_file", Description: "read", InputSchema: map[string]any{"type": "object"}}}, tools.Metadata{}))

	step, err := adapter.Next(context.Background(), []message.Message{message.SystemMessage("sys"), message.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if step.Type != message.StepToolCalls || len(step.Calls) != 1 || step.Calls[0].ToolName != "read_file" || step.ContentKind != message.ContentProgress {
		t.Fatalf("unexpected step: %#v", step)
	}
	input, ok := step.Calls[0].Input.(map[string]any)
	if !ok || input["path"] != "README.md" {
		t.Fatalf("unexpected tool input: %#v", step.Calls[0].Input)
	}
	if got := step.Diagnostics.IgnoredBlockTypes; len(got) != 0 {
		t.Fatalf("reasoning must be preserved, got ignored diagnostics %#v", step.Diagnostics)
	}
	if step.ProviderState == nil || step.ProviderState.Protocol != "openai_responses" || len(step.ProviderState.Items) != 3 {
		t.Fatalf("expected complete opaque output state, got %#v", step.ProviderState)
	}
}

func TestOpenAIResponsesPayloadEncodesToolResults(t *testing.T) {
	registry := tools.NewRegistry([]tools.Definition{{Name: "read_file", Description: "read", InputSchema: map[string]any{"type": "object"}}}, tools.Metadata{})
	payload := toOpenAIResponsesPayload(config.Runtime{
		Model:                  "gpt-5.5",
		ReasoningEffort:        "xhigh",
		DisableResponseStorage: true,
		MaxOutputTokens:        512,
	}, registry, []message.Message{
		message.SystemMessage("sys"),
		message.UserMessage("hi"),
		message.AssistantToolCallMessage(message.ToolCall{ID: "call_1", ToolName: "read_file", Input: map[string]any{"path": "README.md"}}),
		message.ToolResultMessage("call_1", "read_file", "file contents", false),
	})

	if payload["max_output_tokens"] != 512 {
		t.Fatalf("unexpected max_output_tokens: %#v", payload["max_output_tokens"])
	}
	if payload["store"] != false {
		t.Fatalf("expected store=false, got %#v", payload["store"])
	}
	reasoning, ok := payload["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("unexpected reasoning payload: %#v", payload["reasoning"])
	}
	input, ok := payload["input"].([]map[string]any)
	if !ok {
		t.Fatalf("unexpected input payload type: %#v", payload["input"])
	}
	if len(input) != 4 {
		t.Fatalf("unexpected input payload length: %#v", input)
	}
	if input[2]["type"] != "function_call" || input[3]["type"] != "function_call_output" {
		t.Fatalf("unexpected tool message encoding: %#v", input)
	}
	if input[3]["call_id"] != "call_1" || input[3]["output"] != "file contents" {
		t.Fatalf("unexpected tool result payload: %#v", input[3])
	}
}

func TestAnthropicAdapterParsesToolUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("missing auth header: %s", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "claude-test" {
			t.Fatalf("unexpected body: %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stop_reason": "tool_use",
			"content": []map[string]any{
				{"type": "text", "text": "<progress>checking"},
				{"type": "tool_use", "id": "toolu_1", "name": "read_file", "input": map[string]any{"path": "README.md"}},
			},
		})
	}))
	defer server.Close()

	adapter := NewAnthropic(func(context.Context) (config.Runtime, error) {
		return config.Runtime{Model: "claude-test", BaseURL: server.URL, AuthToken: "token"}, nil
	}, tools.NewRegistry([]tools.Definition{{Name: "read_file", Description: "read", InputSchema: map[string]any{"type": "object"}}}, tools.Metadata{}))

	step, err := adapter.Next(context.Background(), []message.Message{message.SystemMessage("sys"), message.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if step.Type != message.StepToolCalls || len(step.Calls) != 1 || step.ContentKind != message.ContentProgress {
		t.Fatalf("unexpected step: %#v", step)
	}
}

func TestAnthropicAdapterDeduplicatesIgnoredBlockDiagnostics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stop_reason": "pause_turn",
			"content": []map[string]any{
				{"type": "thinking", "thinking": "hidden"},
				{"type": "thinking", "thinking": "hidden again"},
			},
		})
	}))
	defer server.Close()

	adapter := NewAnthropic(func(context.Context) (config.Runtime, error) {
		return config.Runtime{Model: "claude-test", BaseURL: server.URL, AuthToken: "token"}, nil
	}, tools.NewRegistry(nil, tools.Metadata{}))

	step, err := adapter.Next(context.Background(), []message.Message{message.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if step.Diagnostics.StopReason != "pause_turn" {
		t.Fatalf("unexpected diagnostics: %#v", step.Diagnostics)
	}
	if got := step.Diagnostics.IgnoredBlockTypes; len(got) != 1 || got[0] != "thinking" {
		t.Fatalf("expected deduped ignored block diagnostics, got %#v", got)
	}
}
