package model

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

// TestLiveProviderSmoke is deliberately opt-in and never reads project/user
// config. It may incur API charges. Supply credentials only in the invocation's
// environment and explicitly select MY_CODE_SMOKE_PROVIDER/MODEL.
func TestLiveProviderSmoke(t *testing.T) {
	if os.Getenv("MY_CODE_LIVE_SMOKE") != "1" {
		t.Skip("set MY_CODE_LIVE_SMOKE=1 to opt in to a billable live provider smoke test")
	}
	provider, modelName := os.Getenv("MY_CODE_SMOKE_PROVIDER"), os.Getenv("MY_CODE_SMOKE_MODEL")
	if provider == "" || modelName == "" {
		t.Fatal("MY_CODE_SMOKE_PROVIDER and MY_CODE_SMOKE_MODEL are required")
	}
	runtime := config.Runtime{Provider: provider, Model: modelName, MaxOutputTokens: 2048, DisableResponseStorage: true}
	switch provider {
	case "openai":
		runtime.APIKey = os.Getenv("OPENAI_API_KEY")
		runtime.BaseURL = "https://api.openai.com"
		runtime.WireAPI = os.Getenv("MY_CODE_SMOKE_WIRE_API")
	case "anthropic":
		runtime.APIKey = os.Getenv("ANTHROPIC_API_KEY")
		runtime.BaseURL = "https://api.anthropic.com"
	default:
		t.Fatal("smoke provider must be openai or anthropic")
	}
	if runtime.APIKey == "" {
		t.Fatal("provider API key must be supplied in process environment")
	}
	adapter, err := NewFromRuntime(runtime, tools.NewRegistry(nil, tools.Metadata{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	var streamed int
	step, err := adapter.(message.StreamingModel).NextStream(ctx, []message.Message{message.UserMessage("Reply with the single word OK.")}, func(delta string) { streamed += len(delta) })
	if err != nil {
		t.Fatal(err)
	}
	if streamed == 0 || strings.TrimSpace(step.Content) == "" {
		t.Fatal("provider did not return visible streamed text")
	}
	if step.Diagnostics.Usage.TotalTokens == 0 && step.Diagnostics.Usage.InputTokens == 0 {
		t.Log("provider did not report token usage")
	}
}

// TestLiveAnthropicCache verifies an actual write followed by a cache read.
// It is deliberately separate from CI because it sends two billable requests.
func TestLiveAnthropicCache(t *testing.T) {
	if os.Getenv("MY_CODE_LIVE_CACHE_CHECK") != "1" {
		t.Skip("set MY_CODE_LIVE_CACHE_CHECK=1 for two billable Anthropic requests")
	}
	key, modelName := os.Getenv("ANTHROPIC_API_KEY"), os.Getenv("MY_CODE_SMOKE_MODEL")
	if key == "" || modelName == "" {
		t.Fatal("ANTHROPIC_API_KEY and MY_CODE_SMOKE_MODEL are required")
	}
	runtime := config.Runtime{Provider: "anthropic", Model: modelName, APIKey: key, BaseURL: "https://api.anthropic.com", MaxOutputTokens: 32, PromptCaching: true}
	adapter, err := NewFromRuntime(runtime, tools.NewRegistry(nil, tools.Metadata{}))
	if err != nil {
		t.Fatal(err)
	}
	// The repeated static prefix exceeds the documented minimum even for
	// models requiring 4,096 cacheable tokens. Both requests are identical.
	static := strings.Repeat("one two three four five six seven eight. ", 1100)
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
	if usage[0].CacheWriteTokens == 0 || usage[1].CacheReadTokens == 0 {
		t.Fatalf("cache write/read not reported; check model eligibility and prefix size: first=%+v second=%+v", usage[0], usage[1])
	}
	t.Logf("Anthropic cache verified: first write=%d tokens, second read=%d tokens", usage[0].CacheWriteTokens, usage[1].CacheReadTokens)
}
