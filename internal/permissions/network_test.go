package permissions

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/safety"
)

func TestNetworkGrantIsExactOriginAndPersistent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "permissions.json")
	called := 0
	manager, err := New(root, path, func(_ context.Context, request Request) (PromptResult, error) {
		called++
		if request.Kind != KindNetwork || request.Scope != "https://api.example.com" {
			t.Fatalf("wrong network approval: %#v", request)
		}
		return PromptResult{Decision: DecisionAllowAlways}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureNetwork(context.Background(), "https://api.example.com", "GET"); err != nil {
		t.Fatal(err)
	}
	loaded, err := New(root, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.EnsureNetwork(context.Background(), "https://api.example.com", "POST"); err != nil {
		t.Fatal(err)
	}
	if err := loaded.EnsureNetwork(context.Background(), "https://other.api.example.com", "GET"); err == nil {
		t.Fatal("subdomain inherited network grant")
	}
	if err := loaded.EnsureNetwork(context.Background(), "https://api.example.com:8443", "GET"); err == nil {
		t.Fatal("port inherited network grant")
	}
	if called != 1 {
		t.Fatalf("unexpected prompt count: %d", called)
	}
	if err := loaded.EnsureNetwork(context.Background(), "https://api.example.com/path", "GET"); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("path was treated as an origin: %v", err)
	}
}

func TestRemoteMCPRequiresFreshOperationApproval(t *testing.T) {
	count := 0
	manager, err := New(t.TempDir(), filepath.Join(t.TempDir(), "permissions.json"), func(_ context.Context, request Request) (PromptResult, error) {
		count++
		if request.Kind != KindMCP || strings.Contains(request.Summary, "sandbox") || strings.Contains(strings.Join(request.Details, " "), "secret-value") {
			t.Fatalf("unsafe remote approval: %#v", request)
		}
		return PromptResult{Decision: DecisionAllowOnce}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := safety.WithSecrets(context.Background(), "secret-value")
	for i := 0; i < 2; i++ {
		if err := manager.EnsureRemoteMCP(ctx, "issues", "create", "https://example.com", `{"token":"secret-value"}`); err != nil {
			t.Fatal(err)
		}
	}
	if count != 2 {
		t.Fatalf("operation approval was cached: %d", count)
	}
	manager.SetPrompt(nil)
	if err := manager.EnsureRemoteMCP(ctx, "issues", "create", "https://example.com", `{}`); err == nil {
		t.Fatal("headless remote operation was allowed")
	}
	for _, origin := range []string{"https://example.com:http", "https://example.com:70000", "https://example.com/path"} {
		if _, err := canonicalNetworkOrigin(origin); err == nil {
			t.Fatalf("invalid origin accepted: %s", origin)
		}
	}
}
