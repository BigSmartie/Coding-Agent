package permissions

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
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
