package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/BigSmartie/Coding-Agent/internal/safety"
)

type Result struct {
	OK     bool
	Output string
}

func Success(output string) Result {
	return Result{OK: true, Output: output}
}

func Error(output string) Result {
	return Result{OK: false, Output: output}
}

type PermissionManager interface {
	EnsurePathAccess(ctx context.Context, targetPath, intent string) error
	EnsureCommand(ctx context.Context, command string, args []string, cwd string) error
	EnsureEdit(ctx context.Context, targetPath, diffPreview string) error
}

type Context struct {
	CWD        string
	Permission PermissionManager
}

type Definition struct {
	Name        string
	Description string
	InputSchema map[string]any
	Run         func(context.Context, json.RawMessage, Context) Result
}

type SkillSummary struct {
	Name        string
	Description string
	Path        string
	Source      string
}

type MCPServerSummary struct {
	Name          string
	Command       string
	Status        string
	ToolCount     int
	Error         string
	Protocol      string
	ResourceCount int
	PromptCount   int
}

type Metadata struct {
	Skills     []SkillSummary
	MCPServers []MCPServerSummary
}

type Registry struct {
	tools    []Definition
	byName   map[string]Definition
	metadata Metadata
	dispose  func(context.Context) error
}

func NewRegistry(definitions []Definition, metadata Metadata) *Registry {
	byName := map[string]Definition{}
	for _, definition := range definitions {
		byName[definition.Name] = definition
	}
	return &Registry{tools: definitions, byName: byName, metadata: metadata}
}

func (r *Registry) WithDisposer(dispose func(context.Context) error) *Registry {
	r.dispose = dispose
	return r
}

func (r *Registry) List() []Definition {
	out := make([]Definition, len(r.tools))
	copy(out, r.tools)
	return out
}

func (r *Registry) Skills() []SkillSummary {
	out := make([]SkillSummary, len(r.metadata.Skills))
	copy(out, r.metadata.Skills)
	return out
}

func (r *Registry) MCPServers() []MCPServerSummary {
	out := make([]MCPServerSummary, len(r.metadata.MCPServers))
	copy(out, r.metadata.MCPServers)
	return out
}

func (r *Registry) Find(name string) (Definition, bool) {
	definition, ok := r.byName[name]
	return definition, ok
}

func (r *Registry) Execute(ctx context.Context, toolName string, input any, toolContext Context) (result Result) {
	defer func() { result.Output = safety.Redact(ctx, result.Output) }()
	definition, ok := r.Find(toolName)
	if !ok {
		return Error("Unknown tool: " + toolName)
	}

	raw, err := normalizeInput(input)
	if err != nil {
		return Error(err.Error())
	}
	if err := validateInputSchema(definition.InputSchema, raw); err != nil {
		return Error("Invalid input for " + toolName + ": " + err.Error())
	}

	if definition.Run == nil {
		return Error("Tool has no runner: " + toolName)
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			result = Error(fmt.Sprintf("Tool %s panicked: %v", toolName, recovered))
		}
	}()
	return definition.Run(ctx, raw, toolContext)
}

func (r *Registry) Dispose(ctx context.Context) error {
	if r.dispose == nil {
		return nil
	}
	return r.dispose(ctx)
}

func normalizeInput(input any) (json.RawMessage, error) {
	if input == nil {
		return json.RawMessage(`{}`), nil
	}
	if raw, ok := input.(json.RawMessage); ok {
		return raw, nil
	}
	bytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("invalid tool input: %w", err)
	}
	return bytes, nil
}
