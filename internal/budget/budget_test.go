package budget

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/message"
)

func TestCompactionKeepsCompleteToolGroupAndOpaqueState(t *testing.T) {
	opaque := json.RawMessage(`{"type":"reasoning","encrypted_content":"signed-opaque-bytes"}`)
	messages := []message.Message{
		message.SystemMessage("system"),
		message.UserMessage(strings.Repeat("old context ", 600)),
		message.AssistantMessage("old answer"),
		message.UserMessage("current task"),
		{Role: message.RoleProviderState, ProviderState: &message.ProviderState{Protocol: "openai_responses", Items: []json.RawMessage{opaque}}},
		message.AssistantToolCallMessage(message.ToolCall{ID: "call-1", ToolName: "read_file", Input: map[string]any{"path": "a.go"}}),
		message.ToolResultMessage("call-1", "read_file", "file content", false),
	}
	result, err := Prepare(messages, nil, Options{ContextWindowTokens: 1800, MaxOutputTokens: 200, CurrentUserPrompt: "current task"})
	if err != nil || !result.Compacted {
		t.Fatalf("expected compaction: %#v, %v", result, err)
	}
	if len(result.Messages) != 6 || result.Messages[0].Role != message.RoleSystem || !strings.HasPrefix(result.Messages[1].Content, summaryMarker) {
		t.Fatalf("unexpected compacted transcript: %#v", result.Messages)
	}
	if result.Messages[3].ProviderState == nil || !bytes.Equal(result.Messages[3].ProviderState.Items[0], opaque) {
		t.Fatal("opaque continuation was altered")
	}
	if result.Messages[4].Role != message.RoleAssistantToolCall || result.Messages[5].Role != message.RoleToolResult {
		t.Fatal("tool call/result pair was split")
	}
}

func TestCompactionDropsWholeCompletedToolBatch(t *testing.T) {
	messages := []message.Message{
		message.SystemMessage("system"),
		message.UserMessage(strings.Repeat("large current request ", 400)),
		message.AssistantToolCallMessage(message.ToolCall{ID: "call-1", ToolName: "read_file", Input: map[string]any{}}),
		message.ToolResultMessage("call-1", "read_file", strings.Repeat("large result ", 400), false),
		message.AssistantMessage("next answer"),
	}
	result, err := Prepare(messages, nil, Options{ContextWindowTokens: 1800, MaxOutputTokens: 200, CurrentUserPrompt: "current task"})
	if err != nil || !result.Compacted {
		t.Fatalf("expected compaction: %#v, %v", result, err)
	}
	if len(result.Messages) != 3 || result.Messages[2].Content != "next answer" || !strings.Contains(result.Messages[1].Content, "current task") {
		t.Fatalf("completed batch was not removed atomically: %#v", result.Messages)
	}
}

func TestOversizedUnfinishedToolBatchFailsBeforeModelCall(t *testing.T) {
	messages := []message.Message{
		message.SystemMessage("system"),
		message.UserMessage(strings.Repeat("large request ", 600)),
		message.AssistantToolCallMessage(message.ToolCall{ID: "call-1", ToolName: "read_file", Input: map[string]any{}}),
	}
	if _, err := Prepare(messages, nil, Options{ContextWindowTokens: 1200, MaxOutputTokens: 200}); err == nil {
		t.Fatal("oversized unfinished tool group was compacted unsafely")
	}
}

func TestCompactionDoesNotTruncateCurrentUserRequest(t *testing.T) {
	prompt := strings.Repeat("must preserve this instruction ", 500)
	messages := []message.Message{
		message.SystemMessage("system"),
		message.UserMessage(prompt),
		message.AssistantToolCallMessage(message.ToolCall{ID: "call-1", ToolName: "read_file", Input: map[string]any{}}),
		message.ToolResultMessage("call-1", "read_file", strings.Repeat("result ", 500), false),
		message.AssistantMessage("next answer"),
	}
	if _, err := Prepare(messages, nil, Options{ContextWindowTokens: 1800, MaxOutputTokens: 200, CurrentUserPrompt: prompt}); err == nil {
		t.Fatal("compaction silently truncated the current request")
	}
}

func TestProviderUsageAnchorsGrowthEstimate(t *testing.T) {
	messages := []message.Message{message.SystemMessage("system"), message.UserMessage("hello")}
	first, err := Prepare(messages, nil, Options{ContextWindowTokens: 100000})
	if err != nil {
		t.Fatal(err)
	}
	messages = append(messages, message.AssistantMessage("more words"))
	next, err := Prepare(messages, nil, Options{ContextWindowTokens: 100000, PreviousInputTokens: 1000, PreviousRequestBytes: first.RequestBytes})
	if err != nil || next.EstimatedTokens < 1000+(next.RequestBytes-first.RequestBytes) {
		t.Fatalf("provider usage did not anchor request growth: %#v, %v", next, err)
	}
}
