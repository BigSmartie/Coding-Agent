// Package subagent runs short, read-only repository investigations with a
// smaller model budget and a strictly narrower tool and path scope.
package subagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/agent"
	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/model"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
	"github.com/BigSmartie/Coding-Agent/internal/workspace"
)

const (
	maxCallsPerSession = 8
	maxConcurrent      = 2
	maxSteps           = 8
	maxToolCalls       = 16
	maxTotalTokens     = 12000
	maxTaskBytes       = 4096
	maxResultBytes     = 8192
)

type Trace struct {
	ID          string   `json:"id"`
	Status      string   `json:"status"`
	ScopeDigest string   `json:"scopeDigest"`
	ToolNames   []string `json:"toolNames,omitempty"`
	Tokens      int      `json:"tokens,omitempty"`
	DurationMS  int64    `json:"durationMs,omitempty"`
}

type Manager struct {
	Root         string
	Runtime      *config.Runtime
	WriteTrace   func(Trace) error
	ModelFactory func(config.Runtime, *tools.Registry) (message.Model, error)
	semaphore    chan struct{}
	started      atomic.Int32
}

func New(root string, runtime *config.Runtime, trace func(Trace) error) *Manager {
	return &Manager{Root: root, Runtime: runtime, WriteTrace: trace, semaphore: make(chan struct{}, maxConcurrent)}
}

func (m *Manager) Definition() tools.Definition {
	return tools.Definition{
		Name:        "delegate_readonly",
		Description: "Delegate a short, read-only repository investigation. The child can only list, grep, and read files beneath the selected directory; it has no command, edit, MCP, or network tools. Results and usage are bounded.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"task": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"}}, "required": []string{"task"}},
		Run: func(ctx context.Context, raw json.RawMessage, _ tools.Context) tools.Result {
			var input struct {
				Task string `json:"task"`
				Path string `json:"path"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return tools.Error(err.Error())
			}
			result, err := m.Run(ctx, input.Task, input.Path)
			if err != nil {
				return tools.Error(safety.Redact(ctx, err.Error()))
			}
			return tools.Success(safety.Redact(ctx, result))
		},
	}
}

func (m *Manager) Run(ctx context.Context, task, path string) (answer string, runErr error) {
	if m == nil || m.Runtime == nil {
		return "", fmt.Errorf("subagent model is not configured")
	}
	if len(task) == 0 || len(task) > maxTaskBytes || strings.TrimSpace(task) == "" {
		return "", fmt.Errorf("subagent task must be 1–4096 bytes")
	}
	callNumber := m.started.Add(1)
	if callNumber > maxCallsPerSession {
		return "", fmt.Errorf("subagent session limit reached")
	}
	select {
	case m.semaphore <- struct{}{}:
		defer func() { <-m.semaphore }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if path == "" {
		path = "."
	}
	root, err := workspace.Canonical(m.Root)
	if err != nil {
		return "", err
	}
	scope, err := workspace.Resolve(ctx, root, path, "list", nil)
	if err != nil {
		return "", err
	}
	if !workspace.Within(root, scope) {
		return "", fmt.Errorf("subagent scope escapes workspace")
	}
	info, err := os.Stat(scope)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("subagent scope must be a directory")
	}
	relative, _ := filepath.Rel(root, scope)
	digest := sha256.Sum256([]byte(filepath.ToSlash(relative)))
	trace := Trace{ID: fmt.Sprintf("sa-%08d", callNumber), Status: "started", ScopeDigest: hex.EncodeToString(digest[:8])}
	if m.WriteTrace != nil {
		if err := m.WriteTrace(trace); err != nil {
			return "", err
		}
	}
	startedAt := time.Now()
	defer func() {
		trace.DurationMS = time.Since(startedAt).Milliseconds()
		if m.WriteTrace != nil {
			if err := m.WriteTrace(trace); err != nil && runErr == nil {
				answer, runErr = "", err
			}
		}
	}()
	childDefs := []tools.Definition{}
	for _, definition := range tools.Builtins(scope, nil, nil).List() {
		switch definition.Name {
		case "list_files", "grep_files", "read_file":
			childDefs = append(childDefs, definition)
		}
	}
	registry := tools.NewRegistry(childDefs, tools.Metadata{})
	childRuntime := *m.Runtime
	childOutput := 1024
	if childRuntime.MaxOutputTokens > 0 && childRuntime.MaxOutputTokens < childOutput {
		childOutput = childRuntime.MaxOutputTokens
	}
	childWindow := 8192
	if childRuntime.ContextWindowTokens > 0 && childRuntime.ContextWindowTokens < childWindow {
		childWindow = childRuntime.ContextWindowTokens
	}
	childRuntime.MaxOutputTokens = childOutput
	childRuntime.ContextWindowTokens = childWindow
	factory := m.ModelFactory
	if factory == nil {
		factory = model.NewFromRuntime
	}
	adapter, err := factory(childRuntime, registry)
	if err != nil {
		trace.Status = "failed"
		return "", err
	}
	childCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	tokens := 0
	messages, err := agent.RunTurn(childCtx, agent.Args{
		Model: adapter, Tools: registry, CWD: scope, MaxSteps: maxSteps, ContextWindowTokens: childWindow, MaxOutputTokens: childOutput,
		CurrentUserPrompt: task,
		Messages:          []message.Message{message.SystemMessage("Investigate the repository using only the provided read-only tools. Stay within the scoped directory. Do not assume access to commands, edits, network, MCP, or another agent. Return a concise, evidence-based answer."), message.UserMessage(task)},
		OnToolStart: func(name string, _ any) {
			trace.ToolNames = append(trace.ToolNames, name)
			if len(trace.ToolNames) >= maxToolCalls {
				cancel()
			}
		},
		OnUsage: func(usage message.TokenUsage) {
			amount := usage.TotalTokens
			if amount == 0 {
				amount = usage.InputTokens + usage.OutputTokens
			}
			tokens += amount
			trace.Tokens = tokens
			if tokens > maxTotalTokens {
				cancel()
			}
		},
	})
	if err != nil {
		trace.Status = "failed"
		return "", err
	}
	if tokens > maxTotalTokens {
		trace.Status = "failed"
		return "", fmt.Errorf("subagent token budget exhausted")
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == message.RoleAssistant {
			answer = safety.Redact(ctx, messages[i].Content)
			if len(answer) > maxResultBytes {
				answer = answer[:maxResultBytes] + "…"
			}
			if strings.Contains(answer, "Reached the maximum tool-step limit") {
				trace.Status = "failed"
				return "", fmt.Errorf("subagent step limit reached")
			}
			trace.Status = "completed"
			return answer, nil
		}
	}
	trace.Status = "failed"
	return "", fmt.Errorf("subagent returned no answer")
}
