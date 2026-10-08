package model

import (
	"context"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/message"
)

type Mock struct{}

func (Mock) Next(_ context.Context, messages []message.Message) (message.Step, error) {
	last := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == message.RoleUser {
			last = messages[i].Content
			break
		}
	}
	if strings.Contains(strings.ToLower(last), "readme") {
		return message.ToolCallsStep([]message.ToolCall{{ID: "mock_readme", ToolName: "read_file", Input: map[string]any{"path": "README.md"}}}, "I'll inspect README.md first.", message.ContentProgress, message.Diagnostics{}), nil
	}
	return message.AssistantStep("Mock mode response. Ask me to inspect README.md to see tool use.", message.ContentFinal, message.Diagnostics{}), nil
}
