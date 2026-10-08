package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/safety"
)

type testPermission struct{ edit func() }

func (p testPermission) EnsurePathAccess(context.Context, string, string) error        { return nil }
func (p testPermission) EnsureCommand(context.Context, string, []string, string) error { return nil }
func (p testPermission) EnsureEdit(context.Context, string, string) error {
	if p.edit != nil {
		p.edit()
	}
	return nil
}

func TestCommandsAndEditsFailClosedWithoutApproval(t *testing.T) {
	cwd := t.TempDir()
	registry := Builtins(cwd, nil, nil)
	for _, test := range []struct {
		name  string
		input map[string]any
	}{
		{"run_command", map[string]any{"command": "go", "args": []string{"test", "./..."}}},
		{"run_command", map[string]any{"command": "env"}},
		{"run_command", map[string]any{"command": "echo hello > escaped.txt"}},
		{"write_file", map[string]any{"path": "written.txt", "content": "hello"}},
	} {
		result := registry.Execute(context.Background(), test.name, test.input, Context{CWD: cwd})
		if result.OK || !strings.Contains(result.Output, "approval") {
			t.Fatalf("missing fail-closed approval: %#v", result)
		}
	}
	for _, file := range []string{"escaped.txt", "written.txt"} {
		if _, err := os.Stat(filepath.Join(cwd, file)); !os.IsNotExist(err) {
			t.Fatalf("unapproved mutation: %s", file)
		}
	}
}

func TestSearchDoesNotExposeProtectedFilesOrInterpretPatternAsOption(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte("needle secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "source.txt"), []byte("needle public\n--pre=anything"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := Builtins(cwd, nil, nil)
	result := registry.Execute(context.Background(), "grep_files", map[string]any{"pattern": "needle"}, Context{CWD: cwd})
	if !result.OK || !strings.Contains(result.Output, "public") || strings.Contains(result.Output, "secret") {
		t.Fatalf("unexpected protected search: %#v", result)
	}
	result = registry.Execute(context.Background(), "grep_files", map[string]any{"pattern": "--pre=anything"}, Context{CWD: cwd})
	if !result.OK || !strings.Contains(result.Output, "source.txt:2:") {
		t.Fatalf("pattern became a subprocess option: %#v", result)
	}
}

func TestReviewedWriteDetectsFileChangeDuringApproval(t *testing.T) {
	cwd := t.TempDir()
	target := filepath.Join(cwd, "source.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	permission := testPermission{edit: func() {
		if err := os.WriteFile(target, []byte("concurrent user edit"), 0o600); err != nil {
			t.Fatal(err)
		}
	}}
	registry := Builtins(cwd, permission, nil)
	result := registry.Execute(context.Background(), "write_file", map[string]any{"path": "source.txt", "content": "model edit"}, Context{CWD: cwd, Permission: permission})
	if result.OK {
		t.Fatal("overwrote concurrent user edit")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "concurrent user edit" {
		t.Fatalf("user edit lost: %q %v", data, err)
	}
}

func TestToolResultRedactsRegisteredCredentials(t *testing.T) {
	secret := "test-private-credential-value"
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "source.txt"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := Builtins(cwd, nil, nil)
	result := registry.Execute(safety.WithSecrets(context.Background(), secret), "read_file", map[string]any{"path": "source.txt"}, Context{CWD: cwd})
	if !result.OK || strings.Contains(result.Output, secret) || !strings.Contains(result.Output, "[REDACTED]") {
		t.Fatalf("credential not redacted: %#v", result)
	}
}
