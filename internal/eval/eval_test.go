package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

type scriptedModel struct {
	calls int
	t     *testing.T
}

func (m *scriptedModel) Next(_ context.Context, messages []message.Message) (message.Step, error) {
	m.calls++
	if m.calls == 1 {
		return message.ToolCallsStep([]message.ToolCall{{ID: "edit1", ToolName: "write_file", Input: map[string]any{"path": "answer.txt", "content": "fixed\n"}}}, "", message.ContentNone, message.Diagnostics{}), nil
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != message.RoleToolResult || messages[len(messages)-1].IsError {
		m.t.Fatalf("evaluation tool result missing: %#v", messages)
	}
	return message.AssistantStep("done", message.ContentFinal, message.Diagnostics{}), nil
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, output, err)
	}
	return strings.TrimSpace(string(output))
}

func TestRepositoryTaskRunsOnPinnedDisposableSnapshot(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.email", "eval@example.test")
	git(t, repo, "config", "user.name", "Eval")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "README.md")
	git(t, repo, "commit", "-qm", "fixture")
	commit := git(t, repo, "rev-parse", "HEAD")
	digest := sha256.Sum256([]byte("fixed\n"))
	spec := Spec{ID: "edit-small-file", Repository: repo, Commit: commit, Prompt: "Create answer.txt", ExpectedFiles: map[string]string{"answer.txt": hex.EncodeToString(digest[:])}}
	report, err := Run(context.Background(), spec, func(registry *tools.Registry) (message.Model, error) {
		if _, ok := registry.Find("run_command"); ok {
			t.Fatal("eval command tool escaped filter")
		}
		if _, ok := registry.Find("delegate_readonly"); ok {
			t.Fatal("eval nested delegation escaped filter")
		}
		return &scriptedModel{t: t}, nil
	})
	if err != nil || !report.Passed || len(report.ToolNames) != 1 || report.ToolNames[0] != "write_file" {
		t.Fatalf("evaluation failed: %#v, %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "answer.txt")); !os.IsNotExist(err) {
		t.Fatal("evaluation modified source repository")
	}
	spec.ExpectedFiles = map[string]string{"../outside": "absent"}
	if _, err := Run(context.Background(), spec, func(*tools.Registry) (message.Model, error) { return &scriptedModel{t: t}, nil }); err == nil {
		t.Fatal("unsafe expectation path accepted")
	}
}
