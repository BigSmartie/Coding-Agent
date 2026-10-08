package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

func TestResolveRejectsEscapesWithoutPermissionManager(t *testing.T) {
	cwd := t.TempDir()
	_, err := Resolve(context.Background(), cwd, "../outside", "read", nil)
	if err == nil {
		t.Fatal("expected escape error")
	}
}

func TestResolveAllowsWorkspacePath(t *testing.T) {
	cwd := t.TempDir()
	got, err := Resolve(context.Background(), cwd, "README.md", "read", nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Canonical(filepath.Join(cwd, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
