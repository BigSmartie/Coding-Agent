package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/permissions"
)

func TestReadFileSupportsOffsetLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := Builtins(dir, nil, nil)
	result := reg.Execute(context.Background(), "read_file", map[string]any{"path": "a.txt", "offset": 2, "limit": 3}, Context{CWD: dir})
	if !result.OK || !strings.Contains(result.Output, "TRUNCATED: yes") || !strings.HasSuffix(result.Output, "cde") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestEditFileReplacesText(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := Builtins(dir, nil, nil)
	permission, err := permissions.New(dir, filepath.Join(t.TempDir(), "permissions.json"), func(context.Context, permissions.Request) (permissions.PromptResult, error) {
		return permissions.PromptResult{Decision: permissions.DecisionAllowOnce}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result := reg.Execute(context.Background(), "edit_file", map[string]any{"path": "a.txt", "search": "world", "replace": "mini"}, Context{CWD: dir, Permission: permission})
	if !result.OK {
		t.Fatalf("unexpected result: %#v", result)
	}
	content, _ := os.ReadFile(target)
	if string(content) != "hello mini" {
		t.Fatalf("unexpected content: %q", string(content))
	}
}

func TestPatchFileValidatesReplacementShapeBeforeReadingFile(t *testing.T) {
	dir := t.TempDir()
	reg := Builtins(dir, nil, nil)
	result := reg.Execute(context.Background(), "patch_file", map[string]any{
		"path": "missing.txt",
		"replacements": []any{
			map[string]any{"search": "old"},
		},
	}, Context{CWD: dir})
	if result.OK || !strings.Contains(result.Output, "replacements[0].replace") {
		t.Fatalf("unexpected validation result: %#v", result)
	}
	if strings.Contains(result.Output, "no such file") {
		t.Fatalf("validation should run before file IO: %#v", result)
	}
}
