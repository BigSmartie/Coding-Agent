package tools

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/egress"
)

type liveWebApproval struct{ testPermission }

func (*liveWebApproval) EnsureWebRequest(context.Context, string) error      { return nil }
func (*liveWebApproval) EnsureNetwork(context.Context, string, string) error { return nil }

// TestLiveSearXNGSearch is opt-in and sends one fixed, non-sensitive query to
// the explicitly supplied SearXNG endpoint through the normal egress client.
func TestLiveSearXNGSearch(t *testing.T) {
	if os.Getenv("MYTHOS_CODE_LIVE_WEB_SEARCH") != "1" {
		t.Skip("set MYTHOS_CODE_LIVE_WEB_SEARCH=1 to query a user-owned SearXNG instance")
	}
	endpoint := os.Getenv("MYTHOS_CODE_WEB_SEARCH_ENDPOINT")
	if err := config.ValidateWebSearchEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}
	approval := &liveWebApproval{}
	registry := NewRegistry([]Definition{WebSearchTool(endpoint)}, Metadata{})
	result := registry.Execute(context.Background(), "web_search", map[string]any{"query": "OpenAI"}, Context{Permission: approval, Network: &egress.Client{Permission: approval}})
	if !result.OK {
		t.Fatalf("live SearXNG search failed: %s", result.Output)
	}
	var decoded struct {
		Results []searchResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.Output), &decoded); err != nil || len(decoded.Results) == 0 {
		t.Fatalf("live SearXNG returned no usable results: %v", err)
	}
	t.Logf("SearXNG returned %d bounded public results", len(decoded.Results))
}
