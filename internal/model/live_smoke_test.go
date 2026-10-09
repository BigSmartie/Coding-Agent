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
// environment and explicitly select MYTHOS_CODE_SMOKE_PROVIDER/MODEL.
func TestLiveProviderSmoke(t *testing.T) {
	if os.Getenv("MYTHOS_CODE_LIVE_SMOKE") != "1" {
		t.Skip("set MYTHOS_CODE_LIVE_SMOKE=1 to opt in to a billable live provider smoke test")
	}
	provider, modelName := os.Getenv("MYTHOS_CODE_SMOKE_PROVIDER"), os.Getenv("MYTHOS_CODE_SMOKE_MODEL")
	if provider == "" || modelName == "" {
		t.Fatal("MYTHOS_CODE_SMOKE_PROVIDER and MYTHOS_CODE_SMOKE_MODEL are required")
	}
	runtime := config.Runtime{Provider: provider, Model: modelName, MaxOutputTokens: 2048, DisableResponseStorage: true}
	switch provider {
	case "openai":
		runtime.APIKey = os.Getenv("OPENAI_API_KEY")
		runtime.BaseURL = "https://api.openai.com"
		runtime.WireAPI = os.Getenv("MYTHOS_CODE_SMOKE_WIRE_API")
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
