package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

type scriptedModel struct {
	steps []message.Step
	index int
}

func (m *scriptedModel) Next(context.Context, []message.Message) (message.Step, error) {
	step := m.steps[m.index]
	m.index++
	return step, nil
}

func TestRunTurnExecutesToolThenReturnsFinalAnswer(t *testing.T) {
	model := &scriptedModel{steps: []message.Step{
		message.ToolCallsStep([]message.ToolCall{
			{ID: "toolu_1", ToolName: "echo", Input: map[string]any{"text": "hi"}},
		}, "", message.ContentNone, message.Diagnostics{}),
		message.AssistantStep("done", message.ContentFinal, message.Diagnostics{}),
	}}
	registry := tools.NewRegistry([]tools.Definition{
		{
			Name: "echo",
			Run: func(_ context.Context, input json.RawMessage, _ tools.Context) tools.Result {
				var parsed struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(input, &parsed)
				return tools.Success(parsed.Text)
			},
		},
	}, tools.Metadata{})

	out, err := RunTurn(context.Background(), Args{
		Model:    model,
		Tools:    registry,
		Messages: []message.Message{message.SystemMessage("system"), message.UserMessage("hello")},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := out[len(out)-1]; got.Role != message.RoleAssistant || got.Content != "done" {
		t.Fatalf("unexpected final message: %#v", got)
	}
	if out[2].Role != message.RoleAssistantToolCall || out[3].Role != message.RoleToolResult {
		t.Fatalf("tool messages not appended in order: %#v", out)
	}
	if out[3].Content != "hi" || out[3].IsError {
		t.Fatalf("unexpected tool result: %#v", out[3])
	}
}

func TestRunTurnDoesNotExecuteToolWhenIntentCannotBeJournaled(t *testing.T) {
	model := &scriptedModel{steps: []message.Step{
		message.ToolCallsStep([]message.ToolCall{{ID: "call-1", ToolName: "side_effect", Input: map[string]any{}}}, "", message.ContentNone, message.Diagnostics{}),
	}}
	executions := 0
	registry := tools.NewRegistry([]tools.Definition{{Name: "side_effect", Run: func(context.Context, json.RawMessage, tools.Context) tools.Result {
		executions++
		return tools.Success("done")
	}}}, tools.Metadata{})
	want := errors.New("journal unavailable")
	_, err := RunTurn(context.Background(), Args{
		Model: model, Tools: registry,
		OnEvent: func(event Event) error {
			if event.Kind == "tool_started" {
				return want
			}
			return nil
		},
	})
	if !errors.Is(err, want) || executions != 0 {
		t.Fatalf("tool ran without durable intent: executions=%d, error=%v", executions, err)
	}
}

func TestRunTurnTreatsProgressAsContinuation(t *testing.T) {
	model := &scriptedModel{steps: []message.Step{
		message.AssistantStep("working", message.ContentProgress, message.Diagnostics{}),
		message.AssistantStep("done", message.ContentFinal, message.Diagnostics{}),
	}}

	out, err := RunTurn(context.Background(), Args{
		Model:    model,
		Tools:    tools.NewRegistry(nil, tools.Metadata{}),
		Messages: []message.Message{message.SystemMessage("system"), message.UserMessage("hello")},
	})
	if err != nil {
		t.Fatal(err)
	}

	if out[2].Role != message.RoleAssistantProgress {
		t.Fatalf("expected progress message, got %#v", out[2])
	}
	if out[len(out)-1].Content != "done" {
		t.Fatalf("expected final continuation, got %#v", out)
	}
}

func TestRunTurnUsesToolResultStatusAsProgressUnlessClarifyingQuestion(t *testing.T) {
	model := &captureMessagesModel{
		steps: []message.Step{
			message.ToolCallsStep([]message.ToolCall{{ID: "toolu_1", ToolName: "echo", Input: map[string]any{}}}, "", message.ContentNone, message.Diagnostics{}),
			message.AssistantStep("I changed the file and will run tests next.", message.ContentNone, message.Diagnostics{}),
			message.AssistantStep("done", message.ContentFinal, message.Diagnostics{}),
		},
	}
	progress := []string{}
	out, err := RunTurn(context.Background(), Args{
		Model: model,
		Tools: tools.NewRegistry([]tools.Definition{{
			Name: "echo",
			Run: func(context.Context, json.RawMessage, tools.Context) tools.Result {
				return tools.Success("ok")
			},
		}}, tools.Metadata{}),
		Messages: []message.Message{message.SystemMessage("system"), message.UserMessage("hello")},
		OnProgressMessage: func(content string) {
			progress = append(progress, content)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) != 1 || !strings.Contains(progress[0], "changed the file") {
		t.Fatalf("expected plain status after tool result to be progress, got %#v", progress)
	}
	continuation := model.calls[2][len(model.calls[2])-1]
	if !strings.Contains(continuation.Content, "plain status text as progress") {
		t.Fatalf("unexpected continuation prompt: %#v", continuation)
	}
	if out[len(out)-1].Content != "done" {
		t.Fatalf("unexpected final output: %#v", out)
	}
}

func TestRunTurnStopsForClarifyingQuestionAfterToolResult(t *testing.T) {
	model := &scriptedModel{steps: []message.Step{
		message.ToolCallsStep([]message.ToolCall{{ID: "toolu_1", ToolName: "echo", Input: map[string]any{}}}, "", message.ContentNone, message.Diagnostics{}),
		message.AssistantStep("Which option would you prefer?", message.ContentNone, message.Diagnostics{}),
	}}
	out, err := RunTurn(context.Background(), Args{
		Model: model,
		Tools: tools.NewRegistry([]tools.Definition{{
			Name: "echo",
			Run: func(context.Context, json.RawMessage, tools.Context) tools.Result {
				return tools.Success("ok")
			},
		}}, tools.Metadata{}),
		Messages: []message.Message{message.SystemMessage("system"), message.UserMessage("hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	last := out[len(out)-1]
	if last.Role != message.RoleAssistant || last.Content != "Which option would you prefer?" {
		t.Fatalf("expected clarifying question to stop turn, got %#v", out)
	}
}

func TestRunTurnStopsForClarifyingQuestionBeforeToolCalls(t *testing.T) {
	called := false
	model := &scriptedModel{steps: []message.Step{
		message.ToolCallsStep(
			[]message.ToolCall{{ID: "toolu_1", ToolName: "echo", Input: map[string]any{}}},
			"Do you want me to update the generated files?",
			message.ContentNone,
			message.Diagnostics{},
		),
	}}
	out, err := RunTurn(context.Background(), Args{
		Model: model,
		Tools: tools.NewRegistry([]tools.Definition{{
			Name: "echo",
			Run: func(context.Context, json.RawMessage, tools.Context) tools.Result {
				called = true
				return tools.Success("ok")
			},
		}}, tools.Metadata{}),
		Messages: []message.Message{message.SystemMessage("system"), message.UserMessage("hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("tool should not run when model asks a clarifying question")
	}
	last := out[len(out)-1]
	if last.Role != message.RoleAssistant || !strings.Contains(last.Content, "Do you want") {
		t.Fatalf("expected clarifying question assistant message, got %#v", out)
	}
}

func TestRunTurnIncludesDiagnosticsInEmptyFallback(t *testing.T) {
	model := &scriptedModel{steps: []message.Step{
		message.AssistantStep("", message.ContentNone, message.Diagnostics{StopReason: "end_turn", BlockTypes: []string{"thinking"}, IgnoredBlockTypes: []string{"thinking"}}),
		message.AssistantStep("", message.ContentNone, message.Diagnostics{StopReason: "end_turn", BlockTypes: []string{"thinking"}, IgnoredBlockTypes: []string{"thinking"}}),
		message.AssistantStep("", message.ContentNone, message.Diagnostics{StopReason: "end_turn", BlockTypes: []string{"thinking"}, IgnoredBlockTypes: []string{"thinking"}}),
	}}

	out, err := RunTurn(context.Background(), Args{
		Model:    model,
		Tools:    tools.NewRegistry(nil, tools.Metadata{}),
		Messages: []message.Message{message.SystemMessage("system"), message.UserMessage("hello")},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := out[len(out)-1].Content
	for _, want := range []string{"Model returned an empty response", "Diagnostics: stop_reason=end_turn; blocks=thinking; ignored=thinking."} {
		if !strings.Contains(got, want) {
			t.Fatalf("fallback missing %q:\n%s", want, got)
		}
	}
}

func TestRunTurnUsesSpecificContinuationForThinkingMaxTokens(t *testing.T) {
	model := &captureMessagesModel{
		steps: []message.Step{
			message.AssistantStep("", message.ContentNone, message.Diagnostics{StopReason: "max_tokens", BlockTypes: []string{"thinking"}, IgnoredBlockTypes: []string{"thinking"}}),
			message.AssistantStep("done", message.ContentFinal, message.Diagnostics{}),
		},
	}
	progress := []string{}

	out, err := RunTurn(context.Background(), Args{
		Model:    model,
		Tools:    tools.NewRegistry(nil, tools.Metadata{}),
		Messages: []message.Message{message.SystemMessage("system"), message.UserMessage("hello")},
		OnProgressMessage: func(content string) {
			progress = append(progress, content)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out[len(out)-1].Content != "done" {
		t.Fatalf("unexpected final output: %#v", out)
	}
	if len(progress) != 1 || !strings.Contains(progress[0], "max_tokens") {
		t.Fatalf("expected max_tokens progress, got %#v", progress)
	}
	lastCallMessages := model.calls[1]
	continuation := lastCallMessages[len(lastCallMessages)-1]
	if continuation.Role != message.RoleUser || !strings.Contains(continuation.Content, "max_tokens during thinking") || strings.Contains(continuation.Content, "previous interrupted thinking step") {
		t.Fatalf("unexpected continuation prompt: %#v", continuation)
	}
}

type captureMessagesModel struct {
	steps []message.Step
	calls [][]message.Message
	index int
}

func (m *captureMessagesModel) Next(_ context.Context, messages []message.Message) (message.Step, error) {
	m.calls = append(m.calls, append([]message.Message(nil), messages...))
	step := m.steps[m.index]
	m.index++
	return step, nil
}
