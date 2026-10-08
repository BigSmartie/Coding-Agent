package model

import (
	"encoding/json"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/config"
)

func TestCapabilitiesAreExplicitAboutUnknownContextAndWireFeatures(t *testing.T) {
	unknown := CapabilitiesFor(config.Runtime{Provider: "openai", Model: "gateway-model"})
	if unknown.ContextWindowTokens != 0 || unknown.ContextWindowSource != "unknown" || unknown.WireAPI != "openai_chat_completions" || unknown.ReasoningControl != "" {
		t.Fatalf("invented gateway capabilities: %#v", unknown)
	}
	responses := CapabilitiesFor(config.Runtime{Provider: "openai", Model: "configured-model", WireAPI: "responses", ContextWindowTokens: 128000, MaxOutputTokens: 4096})
	if responses.ContextWindowSource != "user_config" || responses.ContextWindowTokens != 128000 || responses.ReasoningControl != "reasoning_effort" || !responses.OpaqueReasoningState || !responses.AdapterParsesCacheUsage {
		t.Fatalf("missing Responses capabilities: %#v", responses)
	}
	anthropic := CapabilitiesFor(config.Runtime{Provider: "anthropic", Model: "configured-model"})
	if anthropic.WireAPI != "anthropic_messages" || !anthropic.OpaqueReasoningState || anthropic.ReasoningControl != "" {
		t.Fatalf("incorrect Anthropic capabilities: %#v", anthropic)
	}
}

func TestProviderCacheUsageIsParsedWithoutDiscardingTotals(t *testing.T) {
	var chat openAIChatResponse
	if err := json.Unmarshal([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":40,"cache_write_tokens":5}}}`), &chat); err != nil {
		t.Fatal(err)
	}
	if usage := openAIUsageToDiagnostics(chat.Usage); usage.TotalTokens != 120 || usage.CacheReadTokens != 40 || usage.CacheWriteTokens != 5 {
		t.Fatalf("chat cache usage lost: %#v", usage)
	}
	var responses openAIResponsesResponse
	if err := json.Unmarshal([]byte(`{"usage":{"input_tokens":80,"output_tokens":10,"total_tokens":90,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":6}}}`), &responses); err != nil {
		t.Fatal(err)
	}
	if usage := openAIUsageToDiagnostics(responses.Usage); usage.TotalTokens != 90 || usage.CacheReadTokens != 30 || usage.CacheWriteTokens != 6 {
		t.Fatalf("Responses cache usage lost: %#v", usage)
	}
	var anthropic anthropicResponse
	if err := json.Unmarshal([]byte(`{"usage":{"input_tokens":20,"output_tokens":10,"cache_creation_input_tokens":30,"cache_read_input_tokens":40}}`), &anthropic); err != nil {
		t.Fatal(err)
	}
	if anthropic.Usage.CacheReadInputTokens != 40 || anthropic.Usage.CacheCreationInputTokens != 30 {
		t.Fatalf("Anthropic cache usage lost: %#v", anthropic.Usage)
	}
	if usage := anthropicUsageToDiagnostics(anthropic); usage.InputTokens != 90 || usage.TotalTokens != 100 || usage.CacheReadTokens != 40 || usage.CacheWriteTokens != 30 {
		t.Fatalf("Anthropic cache usage counted incorrectly: %#v", usage)
	}
}
