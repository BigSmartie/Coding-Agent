package model

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

// TestLiveDeepSeekToolContinuation uses the user's OS-backed MythosCode credential.
// It is opt-in because it sends two billable requests to the configured model.
func TestLiveDeepSeekToolContinuation(t *testing.T) {
	if os.Getenv("MYTHOS_CODE_LIVE_DEEPSEEK") != "1" {
		t.Skip("set MYTHOS_CODE_LIVE_DEEPSEEK=1 for a billable DeepSeek tool-continuation check")
	}
	runtime, err := config.LoadRuntime("")
	if err != nil {
		t.Fatal(err)
	}
	origin, err := config.CredentialOrigin(runtime.BaseURL)
	if err != nil || runtime.Provider != "openai" || origin != "https://api.deepseek.com" || runtime.APIKey == "" || runtime.WireAPI == "responses" {
		t.Fatal("live test requires a configured DeepSeek OpenAI Chat Completions credential")
	}
	registry := tools.NewRegistry([]tools.Definition{{
		Name: "report_check", Description: "Report a readiness check exactly once.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"type": "string"}}, "required": []string{"status"}},
	}}, tools.Metadata{})
	adapter, err := NewFromRuntime(runtime, registry)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	history := []message.Message{message.UserMessage("Call report_check with status ready exactly once. Wait for the tool result before giving a final answer.")}
	first, err := adapter.(message.StreamingModel).NextStream(ctx, history, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if first.Type != message.StepToolCalls || len(first.Calls) != 1 || first.Calls[0].ToolName != "report_check" {
		t.Fatalf("DeepSeek did not produce the requested tool call: type=%s calls=%d", first.Type, len(first.Calls))
	}
	history = append(history, message.AssistantToolCallMessage(first.Calls[0]), message.ToolResultMessage(first.Calls[0].ID, first.Calls[0].ToolName, "Readiness check passed. Reply OK.", false))
	var streamed strings.Builder
	second, err := adapter.(message.StreamingModel).NextStream(ctx, history, func(delta string) { streamed.WriteString(delta) })
	if err != nil {
		t.Fatal(err)
	}
	if second.Type != message.StepAssistant || strings.TrimSpace(second.Content) == "" || streamed.Len() == 0 {
		t.Fatalf("DeepSeek did not complete after the tool result: type=%s streamed=%t", second.Type, streamed.Len() > 0)
	}
	t.Logf("DeepSeek tool continuation passed; usage first=%d second=%d tokens", first.Diagnostics.Usage.TotalTokens, second.Diagnostics.Usage.TotalTokens)
}

// TestLiveDeepSeekCache verifies a real cached-prefix read using the configured
// DeepSeek credential. DeepSeek caches matching prefixes automatically, unlike
// Anthropic's explicit cache_control request contract.
func TestLiveDeepSeekCache(t *testing.T) {
	if os.Getenv("MYTHOS_CODE_LIVE_DEEPSEEK_CACHE") != "1" {
		t.Skip("set MYTHOS_CODE_LIVE_DEEPSEEK_CACHE=1 for two billable DeepSeek cache requests")
	}
	runtime, err := config.LoadRuntime("")
	if err != nil {
		t.Fatal(err)
	}
	origin, err := config.CredentialOrigin(runtime.BaseURL)
	if err != nil || runtime.Provider != "openai" || origin != "https://api.deepseek.com" || runtime.APIKey == "" || runtime.WireAPI == "responses" {
		t.Fatal("live test requires a configured DeepSeek OpenAI Chat Completions credential")
	}
	runtime.MaxOutputTokens = 2048
	adapter, err := NewFromRuntime(runtime, tools.NewRegistry(nil, tools.Metadata{}))
	if err != nil {
		t.Fatal(err)
	}
	static := "Cache validation " + time.Now().UTC().Format(time.RFC3339Nano) + "\n" + strings.Repeat("one two three four five six seven eight. ", 1100)
	history := []message.Message{message.SystemMessage(static), message.UserMessage("Reply with OK.")}
	var usage [2]message.TokenUsage
	for i := range usage {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		step, err := adapter.(message.StreamingModel).NextStream(ctx, history, func(string) {})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		usage[i] = step.Diagnostics.Usage
	}
	if usage[1].CacheReadTokens <= usage[0].CacheReadTokens {
		t.Fatalf("DeepSeek did not report a new cached-prefix hit: first=%+v second=%+v", usage[0], usage[1])
	}
	t.Logf("DeepSeek cache hit verified: first=%d second=%d cached input tokens", usage[0].CacheReadTokens, usage[1].CacheReadTokens)
}
