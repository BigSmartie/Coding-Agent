package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/trust"
)

func TestDiscoverAndLoadSkill(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	root := filepath.Join(cwd, ".my-code", "skills", "demo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("# Demo\n\nUse this demo skill."), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewStore(cwd, home)
	trusted := trust.Store{Dir: t.TempDir()}
	fingerprint, _, err := trust.WorkspaceFingerprint(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := trusted.Grant(cwd, "workspace", fingerprint, fingerprint); err != nil {
		t.Fatal(err)
	}
	store.ProjectSnapshot = func() *trust.WorkspaceSnapshot { snapshot, _ := trusted.Workspace(cwd); return snapshot }
	discovered, err := store.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 1 || discovered[0].Name != "demo" || discovered[0].Description != "Use this demo skill." {
		t.Fatalf("unexpected skills: %#v", discovered)
	}

	loaded, err := store.Load(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "demo" || loaded.Content == "" {
		t.Fatalf("unexpected loaded skill: %#v", loaded)
	}
}

func TestSkillLoadConsumesReviewedSnapshotInsteadOfReopeningFile(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	path := filepath.Join(cwd, ".my-code", "skills", "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Demo\n\nReviewed skill."), 0600); err != nil {
		t.Fatal(err)
	}
	trusted := trust.Store{Dir: t.TempDir()}
	fingerprint, _, err := trust.WorkspaceFingerprint(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := trusted.Grant(cwd, "workspace", fingerprint, fingerprint); err != nil {
		t.Fatal(err)
	}
	snapshot, err := trusted.Workspace(cwd)
	if err != nil || snapshot == nil {
		t.Fatalf("snapshot unavailable: %v", err)
	}
	store := NewStore(cwd, home)
	store.ProjectSnapshot = func() *trust.WorkspaceSnapshot {
		if err := os.WriteFile(path, []byte("unreviewed replacement"), 0600); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	loaded, err := store.Load(context.Background(), "demo")
	if err != nil || loaded.Content != "# Demo\n\nReviewed skill." {
		t.Fatalf("load reread unreviewed file: %v", err)
	}
	store.ProjectSnapshot = func() *trust.WorkspaceSnapshot { current, _ := trusted.Workspace(cwd); return current }
	if _, err := store.Load(context.Background(), "demo"); err == nil {
		t.Fatal("changed skill retained trust")
	}
}

func TestUntrustedProjectAndTraversalAreRejected(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	store := NewStore(cwd, home)
	source := filepath.Join(home, "source.md")
	if err := os.WriteFile(source, []byte("# Demo\n\ntrusted description"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(context.Background(), source, "demo", "project"); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Discover(context.Background()); err != nil || len(loaded) != 0 {
		t.Fatal("untrusted project skill discovered", err)
	}
	if _, err := store.Load(context.Background(), "demo"); err == nil {
		t.Fatal("untrusted project skill loaded")
	}
	for _, name := range []string{"..", "../escape", `..\escape`, "a/b", "/tmp", "C:\\escape"} {
		if _, err := store.Load(context.Background(), name); err == nil {
			t.Fatal("loaded traversal", name)
		}
		if _, err := store.Install(context.Background(), source, name, "user"); err == nil {
			t.Fatal("installed traversal", name)
		}
		if _, _, err := store.Remove(context.Background(), name, "user"); err == nil {
			t.Fatal("removed traversal", name)
		}
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("source outside skills was modified")
	}
}
