package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceRootAliasKeepsContainmentAndProtection(t *testing.T) {
	realRoot := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Skipf("root alias unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "visible.txt"), []byte("visible"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, ".env"), []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(realRoot, "escape")); err != nil {
		t.Fatal(err)
	}

	visible := filepath.Join(alias, "visible.txt")
	resolved, err := Resolve(context.Background(), alias, visible, "read", nil)
	if err != nil {
		t.Fatalf("root alias rejected ordinary file: %v", err)
	}
	want, err := Canonical(visible)
	if err != nil || resolved != want {
		t.Fatalf("resolved %q, want canonical %q: %v", resolved, want, err)
	}
	access, err := Open(alias)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	if data, err := access.ReadFile(visible, 64); err != nil || string(data) != "visible" {
		t.Fatalf("root alias read failed: %q, %v", data, err)
	}

	for _, path := range []string{
		filepath.Join(alias, ".env"),
		filepath.Join(alias, "escape", "secret.txt"),
		filepath.Join(alias, "..", "outside.txt"),
		alias + "-sibling",
	} {
		if _, err := Resolve(context.Background(), alias, path, "read", nil); err == nil {
			t.Errorf("aliased root admitted forbidden path: %s", path)
		}
		if _, err := access.ReadFile(path, 64); err == nil {
			t.Errorf("root handle admitted forbidden path: %s", path)
		}
	}
}
