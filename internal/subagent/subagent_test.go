package subagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

type fixtureModel struct{ calls int }

func (m *fixtureModel) Next(_ context.Context, messages []message.Message) (message.Step, error) {
	m.calls++
	if m.calls == 1 {
		return message.ToolCallsStep([]message.ToolCall{{ID: "c1", ToolName: "read_file", Input: map[string]any{"path": "source.txt"}}}, "", message.ContentNone, message.Diagnostics{Usage: message.TokenUsage{TotalTokens: 100}}), nil
	}
	for _, msg := range messages {
		if msg.Role == message.RoleToolResult && strings.Contains(msg.Content, "scoped evidence") {
			return message.AssistantStep("Found scoped evidence.", message.ContentFinal, message.Diagnostics{Usage: message.TokenUsage{TotalTokens: 100}}), nil
		}
	}
	return message.AssistantStep("Missing source.", message.ContentFinal, message.Diagnostics{}), nil
}

func TestReadOnlySubagentRestrictsToolsPathsAndTrace(t *testing.T) {
	root := t.TempDir()
	scope := filepath.Join(root, "module")
	if err := os.Mkdir(scope, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "source.txt"), []byte("scoped evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	tracePath := filepath.Join(t.TempDir(), "trace.json")
	var store TraceStore
	manager := New(root, &config.Runtime{Provider: "mock"}, func(event Trace) error { return store.Append(tracePath, event) })
	manager.ModelFactory = func(runtime config.Runtime, registry *tools.Registry) (message.Model, error) {
		if runtime.MaxOutputTokens != 1024 || runtime.ContextWindowTokens != 8192 {
			t.Fatalf("child model budget is not narrow: %#v", runtime)
		}
		seen := map[string]bool{}
		for _, tool := range registry.List() {
			seen[tool.Name] = true
		}
		if len(seen) != 3 || !seen["read_file"] || !seen["grep_files"] || !seen["list_files"] || seen["run_command"] {
			t.Fatalf("child tool scope widened: %#v", seen)
		}
		return &fixtureModel{}, nil
	}
	answer, err := manager.Run(context.Background(), "Read the scoped source", "module")
	if err != nil || answer != "Found scoped evidence." {
		t.Fatalf("child failed: %q, %v", answer, err)
	}
	data, err := os.ReadFile(tracePath)
	if err != nil || !strings.Contains(string(data), `"status": "completed"`) || strings.Contains(string(data), "scoped evidence") || strings.Contains(string(data), "Read the scoped source") {
		t.Fatalf("trace leaked task/content or missed completion: %s, %v", data, err)
	}
	if _, err := manager.Run(context.Background(), "escape", ".."); err == nil {
		t.Fatal("child escaped selected workspace")
	}
}

func TestSubagentSessionCallLimit(t *testing.T) {
	manager := New(t.TempDir(), &config.Runtime{Provider: "mock"}, nil)
	manager.ModelFactory = func(config.Runtime, *tools.Registry) (message.Model, error) { return &fixtureModel{}, nil }
	for i := 0; i < maxCallsPerSession; i++ {
		_, _ = manager.Run(context.Background(), "task", ".")
	}
	if _, err := manager.Run(context.Background(), "task", "."); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("session limit bypassed: %v", err)
	}
}

func TestSubagentFailsWhenCompletionTraceCannotBeSaved(t *testing.T) {
	root := t.TempDir()
	count := 0
	manager := New(root, &config.Runtime{Provider: "mock"}, func(Trace) error {
		count++
		if count == 2 {
			return fmt.Errorf("trace failed")
		}
		return nil
	})
	manager.ModelFactory = func(config.Runtime, *tools.Registry) (message.Model, error) {
		return messageModelFunc(func(context.Context, []message.Message) (message.Step, error) {
			return message.AssistantStep("answer", message.ContentFinal, message.Diagnostics{}), nil
		}), nil
	}
	answer, err := manager.Run(context.Background(), "task", ".")
	if err == nil || answer != "" {
		t.Fatalf("unrecorded child result escaped: %q, %v", answer, err)
	}
}

type messageModelFunc func(context.Context, []message.Message) (message.Step, error)

func (f messageModelFunc) Next(ctx context.Context, messages []message.Message) (message.Step, error) {
	return f(ctx, messages)
}
