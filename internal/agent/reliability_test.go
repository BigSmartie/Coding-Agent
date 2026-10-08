package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

type modelFunc func(context.Context, []message.Message) (message.Step, error)

func (f modelFunc) Next(ctx context.Context, history []message.Message) (message.Step, error) {
	return f(ctx, history)
}

type lifecyclePermission struct {
	tools.PermissionManager
	began, ended int
}

func (p *lifecyclePermission) BeginTurn() { p.began++ }
func (p *lifecyclePermission) EndTurn()   { p.ended++ }

func TestPermissionTurnAlwaysEndsOnModelError(t *testing.T) {
	permission := &lifecyclePermission{}
	want := errors.New("model failed")
	_, err := RunTurn(context.Background(), Args{Permission: permission, Model: modelFunc(func(context.Context, []message.Message) (message.Step, error) { return message.Step{}, want })})
	if !errors.Is(err, want) || permission.began != 1 || permission.ended != 1 {
		t.Fatalf("permission lifecycle leaked: %#v %v", permission, err)
	}
}

func TestToolBatchRetainsProviderStateAndOrdersCallsBeforeResults(t *testing.T) {
	calls := []message.ToolCall{{ID: "a", ToolName: "echo", Input: map[string]any{}}, {ID: "b", ToolName: "echo", Input: map[string]any{}}}
	first := message.ToolCallsStep(calls, "checking", message.ContentProgress, message.Diagnostics{})
	first.ProviderState = &message.ProviderState{Protocol: "openai_responses", Items: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}}
	round := 0
	model := modelFunc(func(_ context.Context, history []message.Message) (message.Step, error) {
		round++
		if round == 1 {
			return first, nil
		}
		if len(history) != 7 {
			t.Fatalf("unexpected history length: %#v", history)
		}
		if history[1].Role != message.RoleProviderState || history[2].MirrorProtocol != "openai_responses" || history[3].Role != message.RoleAssistantToolCall || history[4].Role != message.RoleAssistantToolCall || history[5].Role != message.RoleToolResult || history[6].Role != message.RoleToolResult {
			t.Fatalf("provider state/order lost: %#v", history)
		}
		return message.AssistantStep("done", message.ContentFinal, message.Diagnostics{}), nil
	})
	registry := tools.NewRegistry([]tools.Definition{{Name: "echo", Run: func(context.Context, json.RawMessage, tools.Context) tools.Result { return tools.Success("ok") }}}, tools.Metadata{})
	if _, err := RunTurn(context.Background(), Args{Model: model, Tools: registry, Messages: []message.Message{message.UserMessage("hi")}}); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationClosesRemainingToolCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := []message.ToolCall{{ID: "a", ToolName: "echo", Input: map[string]any{}}, {ID: "b", ToolName: "echo", Input: map[string]any{}}}
	model := &scriptedModel{steps: []message.Step{message.ToolCallsStep(calls, "", message.ContentNone, message.Diagnostics{})}}
	executed := 0
	registry := tools.NewRegistry([]tools.Definition{{Name: "echo", Run: func(context.Context, json.RawMessage, tools.Context) tools.Result {
		executed++
		cancel()
		return tools.Success("ok")
	}}}, tools.Metadata{})
	history, err := RunTurn(ctx, Args{Model: model, Tools: registry})
	if !errors.Is(err, context.Canceled) || executed != 1 || len(history) != 4 {
		t.Fatalf("bad cancellation: %v %#v", err, history)
	}
	if history[3].Role != message.RoleToolResult || history[3].ToolUseID != "b" || !history[3].IsError {
		t.Fatal("remaining call has no canceled result")
	}
}

type streamingTestModel struct{ plainCalled bool }

func (m *streamingTestModel) Next(context.Context, []message.Message) (message.Step, error) {
	m.plainCalled = true
	return message.AssistantStep("plain", message.ContentFinal, message.Diagnostics{}), nil
}
func (m *streamingTestModel) NextStream(_ context.Context, _ []message.Message, delta func(string)) (message.Step, error) {
	delta("partial")
	return message.AssistantStep("final", message.ContentFinal, message.Diagnostics{}), nil
}

func TestRunTurnUsesOptionalStreamingAndModelStart(t *testing.T) {
	model := &streamingTestModel{}
	var events []string
	_, err := RunTurn(context.Background(), Args{Model: model, OnModelStart: func() { events = append(events, "start") }, OnTextDelta: func(delta string) { events = append(events, delta) }, OnAssistant: func(text string) { events = append(events, text) }})
	if err != nil || model.plainCalled || len(events) != 3 || events[0] != "start" || events[1] != "partial" || events[2] != "final" {
		t.Fatalf("stream callbacks incorrect: %#v %v", events, err)
	}
}
