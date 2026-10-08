package permissions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnceDecisionsNeverLeakToLaterCalls(t *testing.T) {
	for _, decision := range []Decision{DecisionAllowOnce, DecisionDenyOnce} {
		for _, kind := range []Kind{KindPath, KindCommand, KindEdit} {
			t.Run(string(decision)+"/"+string(kind), func(t *testing.T) {
				cwd := t.TempDir()
				calls := 0
				manager, err := New(cwd, filepath.Join(t.TempDir(), "permissions.json"), func(_ context.Context, request Request) (PromptResult, error) {
					calls++
					if request.Choices[0].Decision != DecisionDenyOnce {
						t.Fatal("prompt must default to deny")
					}
					return PromptResult{Decision: decision}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "file.txt")
				for i := 0; i < 2; i++ {
					var callErr error
					switch kind {
					case KindPath:
						callErr = manager.EnsurePathAccess(context.Background(), target, "read")
					case KindCommand:
						callErr = manager.EnsureCommand(context.Background(), "npm", []string{"test"}, cwd)
					case KindEdit:
						callErr = manager.EnsureEdit(context.Background(), filepath.Join(cwd, "file.txt"), "diff")
					}
					if (callErr == nil) != (decision == DecisionAllowOnce) {
						t.Fatalf("unexpected decision error: %v", callErr)
					}
				}
				if calls != 2 {
					t.Fatalf("once decision persisted; prompted %d times", calls)
				}
			})
		}
	}
}

func TestTurnApprovalExpires(t *testing.T) {
	for _, decision := range []Decision{DecisionAllowTurn, DecisionAllowAllTurn} {
		t.Run(string(decision), func(t *testing.T) {
			cwd := t.TempDir()
			calls := 0
			manager, err := New(cwd, filepath.Join(t.TempDir(), "permissions.json"), func(context.Context, Request) (PromptResult, error) {
				calls++
				return PromptResult{Decision: decision}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(cwd, "file.txt")
			manager.BeginTurn()
			for i := 0; i < 2; i++ {
				if err := manager.EnsureEdit(context.Background(), target, "diff"); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 1 {
				t.Fatalf("expected one prompt in turn, got %d", calls)
			}
			manager.EndTurn()
			manager.BeginTurn()
			if err := manager.EnsureEdit(context.Background(), target, "diff"); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatal("turn approval leaked into next turn")
			}
		})
	}
}

func TestEveryCommandRequiresApprovalAndScopesExactArgv(t *testing.T) {
	cwd := t.TempDir()
	manager, err := New(cwd, filepath.Join(t.TempDir(), "permissions.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"go", "npm", "env", "git", "find", "rg", "sed"} {
		if err := manager.EnsureCommand(context.Background(), command, []string{"test"}, cwd); err == nil {
			t.Fatalf("%s bypassed approval", command)
		}
	}
	a := commandScope("test", []string{"a b"}, cwd)
	b := commandScope("test", []string{"a", "b"}, cwd)
	c := commandScope("test", []string{"a b"}, filepath.Join(cwd, "other"))
	if a == b || a == c {
		t.Fatal("approval not bound to exact argv and cwd")
	}
}

func TestPersistedCommandApprovalDoesNotContainArguments(t *testing.T) {
	cwd := t.TempDir()
	storePath := filepath.Join(t.TempDir(), "permissions.json")
	manager, err := New(cwd, storePath, func(context.Context, Request) (PromptResult, error) {
		return PromptResult{Decision: DecisionAllowAlways}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sentinel := "sensitive-argument-must-not-persist"
	if err := manager.EnsureCommand(context.Background(), "example", []string{"--token", sentinel}, cwd); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), sentinel) || strings.Contains(string(data), "--token") || strings.Contains(string(data), cwd) {
		t.Fatal("command scope leaked plaintext into permission store")
	}
	restored, err := New(cwd, storePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.EnsureCommand(context.Background(), "example", []string{"--token", sentinel}, cwd); err != nil {
		t.Fatalf("exact approval did not restore: %v", err)
	}
	if err := restored.EnsureCommand(context.Background(), "example", []string{"--token", "different"}, cwd); err == nil {
		t.Fatal("different argv inherited approval")
	}
}

func TestPermissionStoreSizeBound(t *testing.T) {
	target := filepath.Join(t.TempDir(), "permissions.json")
	if err := os.WriteFile(target, make([]byte, 1024*1024+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(t.TempDir(), target, nil); err == nil {
		t.Fatal("oversized permission store accepted")
	}
}

func TestProjectCannotSupplyPermissionStoreOrAlias(t *testing.T) {
	cwd := t.TempDir()
	projectStore := filepath.Join(cwd, "forged.json")
	if err := os.WriteFile(projectStore, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(cwd, projectStore, nil); err == nil {
		t.Fatal("accepted permission state inside project")
	}
	alias := filepath.Join(t.TempDir(), "permissions.json")
	if err := os.Link(projectStore, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := New(cwd, alias, nil); err == nil {
		t.Fatal("accepted hard-linked project permission state")
	}
}
