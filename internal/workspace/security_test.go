package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProtectedPathsRejected(t *testing.T) {
	cwd := t.TempDir()
	for _, name := range []string{".env", ".env.local", ".git/config", ".my-code/settings.json", "nested/.mcp.json", "key.pem", "x:stream"} {
		if _, err := Resolve(context.Background(), cwd, name, "read", nil); err == nil {
			t.Errorf("protected path accepted: %s", name)
		}
	}
}

func TestSymlinkCannotEscapeOrAliasProtectedState(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cwd, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(".env", filepath.Join(cwd, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"escape/secret.txt", "escape/new.txt", "alias.txt"} {
		if _, err := Resolve(context.Background(), cwd, name, "read", nil); err == nil {
			t.Fatalf("symlink allowed: %s", name)
		}
	}
}

func TestOpenedFileProtectionAfterPathWasValidated(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "visible.txt")
	if err := os.WriteFile(path, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	access, err := Open(cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	relative, err := access.relative(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".env", path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	// Bypass the already-completed string check and exercise the actual IO guard.
	file, err := access.openSecure(relative)
	if err == nil {
		file.Close()
		t.Fatal("link swap passed the opened-file security boundary")
	}
	if err := access.WriteFile(path, []byte("replacement")); err == nil {
		t.Fatal("write accepted swapped link")
	}
	data, err := os.ReadFile(filepath.Join(cwd, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "protected" {
		t.Fatal("protected file changed")
	}
}

func TestHardlinkAliasCannotReadProtectedFile(t *testing.T) {
	cwd := t.TempDir()
	secret := filepath.Join(cwd, ".env")
	if err := os.WriteFile(secret, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(cwd, "ordinary.txt")
	if err := os.Link(secret, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	access, err := Open(cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	if data, err := access.ReadFile(alias, 1024); err == nil {
		t.Fatalf("hardlink leaked %q", data)
	}
}

func TestChunkReadDoesNotAllocateWholeFile(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "large.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(64 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	file.Close()
	access, err := Open(cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	data, total, err := access.ReadChunk(path, 64*1024*1024-8, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 8 || total != 64*1024*1024 {
		t.Fatalf("unexpected chunk len=%d total=%d", len(data), total)
	}
	if _, err := access.ReadFile(path, 1024); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected bounded read failure: %v", err)
	}
}

func TestAtomicWritePreservesUnrelatedHardlink(t *testing.T) {
	cwd := t.TempDir()
	alias := filepath.Join(cwd, "alias.txt")
	original := filepath.Join(cwd, "original.txt")
	if err := os.WriteFile(original, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	access, err := Open(cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	if err := access.WriteFile(alias, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatal("atomic write followed a hard link")
	}
}
