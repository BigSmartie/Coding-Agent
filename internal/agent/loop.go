package agent

import (
	"context"
	"strconv"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

type Args struct {
	Model             message.Model
	Tools             *tools.Registry
	Messages          []message.Message
	CWD               string
	Permission        tools.PermissionManager
	MaxSteps          int
	OnToolStart       func(toolName string, input any)
	OnToolResult      func(toolName string, output string, isError bool)
	OnAssistant       func(content string)
	OnProgressMessage func(content string)
	OnUsage           func(message.TokenUsage)
	OnTextDelta       func(string)
	OnModelStart      func()
}

func RunTurn(ctx context.Context, args Args) ([]message.Message, error) {
	if lifecycle, ok := args.Permission.(interface {
		BeginTurn()
		EndTurn()
	}); ok {
		lifecycle.BeginTurn()
		defer lifecycle.EndTurn()
	}
	maxSteps := args.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 80
	}

	messages := append([]message.Message(nil), args.Messages...)
	emptyRetries := 0
	recoverableThinkingRetries := 0
	sawToolResult := false
	toolErrors := 0

	pushContinuation := func(content string) {
		messages = append(messages, message.UserMessage(content))
	}

	for stepIndex := 0; stepIndex < maxSteps; stepIndex++ {
		if err := ctx.Err(); err != nil {
			return messages, err
		}
		if args.OnModelStart != nil {
			args.OnModelStart()
		}
		var next message.Step
		var err error
		if streaming, ok := args.Model.(message.StreamingModel); ok && args.OnTextDelta != nil {
			next, err = streaming.NextStream(ctx, messages, args.OnTextDelta)
		} else {
			next, err = args.Model.Next(ctx, messages)
		}
		if err != nil {
			return messages, err
		}
		callUsage(args.OnUsage, next.Diagnostics.Usage)
		if next.ProviderState != nil && len(next.ProviderState.Items) > 0 {
			messages = append(messages, message.Message{Role: message.RoleProviderState, ProviderState: next.ProviderState})
		}
		projection := func(msg message.Message) message.Message {
			if next.ProviderState != nil && len(next.ProviderState.Items) > 0 {
				msg.MirrorProtocol = next.ProviderState.Protocol
			}
			return msg
		}

		switch next.Type {
		case message.StepAssistant:
			content := strings.TrimSpace(next.Content)
			isEmpty := content == ""
			if !isEmpty && shouldTreatAssistantAsProgress(next.Kind, content, sawToolResult) {
				callProgress(args.OnProgressMessage, content)
				messages = append(messages, projection(message.ProgressMessage(content)))
				if sawToolResult && next.Kind != message.ContentProgress {
					pushContinuation("Continue from your progress update. You have already used tools in this turn, so treat plain status text as progress, not a final answer. Respond with the next concrete tool call, code change, or an explicit <final> answer only if the task is truly complete.")
				} else {
					pushContinuation("Continue immediately from your <progress> update with concrete tool calls, code changes, or an explicit <final> answer only if the task is complete.")
				}
				continue
			}

			if isRecoverableThinkingStop(isEmpty, next.Diagnostics) && recoverableThinkingRetries < 3 {
				recoverableThinkingRetries++
				progress := "Model returned pause_turn while thinking; continuing the request."
				continuation := "Resume from the previous pause_turn and continue the task immediately. Produce the next concrete tool call, code change, or an explicit <final> answer only if the task is complete."
				if next.Diagnostics.StopReason == "max_tokens" {
					progress = "Model hit max_tokens while thinking; continuing the request."
					continuation = "Your previous response hit max_tokens during thinking before producing the next actionable step. Resume immediately and continue with the next concrete tool call, code change, or an explicit <final> answer only if the task is complete. Do not repeat the earlier plan."
				}
				callProgress(args.OnProgressMessage, progress)
				messages = append(messages, message.ProgressMessage(progress))
				pushContinuation(continuation)
				continue
			}

			if isEmpty && emptyRetries < 2 {
				emptyRetries++
				if sawToolResult {
					pushContinuation("Your last response was empty after recent tool results. Continue immediately by trying the next concrete step, adapting to any tool errors, or giving an explicit <final> answer only if the task is complete.")
				} else {
					pushContinuation("Your last response was empty. Continue immediately with concrete tool calls, code changes, or an explicit <final> answer only if the task is complete.")
				}
				continue
			}

			if isEmpty {
				diagnosticsSuffix := formatDiagnostics(next.Diagnostics)
				fallback := "Model returned an empty response, so this turn has stopped. Try again or ask the model to continue."
				if sawToolResult {
					fallback = "Model returned an empty response after tool execution, so this turn has stopped. Try again or ask the model to finish the remaining steps."
					if toolErrors > 0 {
						fallback = "Model returned an empty response after tool execution, so this turn has stopped. Recent tool errors: " + strconv.Itoa(toolErrors) + ". Try again, adjust the command, or ask the model to use another approach."
					}
				}
				fallback += diagnosticsSuffix
				callAssistant(args.OnAssistant, fallback)
				return append(messages, message.AssistantMessage(fallback)), nil
			}

			callAssistant(args.OnAssistant, content)
			return append(messages, projection(message.AssistantMessage(content))), nil

		case message.StepToolCalls:
			if next.Content != "" && looksLikeClarifyingQuestion(next.Content) {
				callAssistant(args.OnAssistant, next.Content)
				messages = append(messages, projection(message.AssistantMessage(next.Content)))
				// A provider transcript must close every proposed tool call even when
				// clarification causes us to defer execution.
				if next.ProviderState != nil {
					for _, call := range next.Calls {
						messages = append(messages, projection(message.AssistantToolCallMessage(call)))
					}
					for _, call := range next.Calls {
						messages = append(messages, message.ToolResultMessage(call.ID, call.ToolName, "Tool was not executed because the assistant requested user clarification.", true))
					}
				}
				return messages, nil
			}

			if next.Content != "" {
				if next.ContentKind == message.ContentProgress {
					callProgress(args.OnProgressMessage, next.Content)
					messages = append(messages, projection(message.ProgressMessage(next.Content)))
				} else {
					callAssistant(args.OnAssistant, next.Content)
					messages = append(messages, projection(message.AssistantMessage(next.Content)))
				}
			}

			// All calls belong to a single assistant turn and must precede results.
			for _, call := range next.Calls {
				messages = append(messages, projection(message.AssistantToolCallMessage(call)))
			}
			for _, call := range next.Calls {
				if ctx.Err() != nil {
					messages = append(messages, message.ToolResultMessage(call.ID, call.ToolName, "Tool execution canceled.", true))
					continue
				}
				callToolStart(args.OnToolStart, call.ToolName, call.Input)
				result := args.Tools.Execute(ctx, call.ToolName, call.Input, tools.Context{
					CWD:        args.CWD,
					Permission: args.Permission,
				})
				sawToolResult = true
				if !result.OK {
					toolErrors++
				}
				callToolResult(args.OnToolResult, call.ToolName, result.Output, !result.OK)
				messages = append(messages,
					message.ToolResultMessage(call.ID, call.ToolName, result.Output, !result.OK),
				)
			}
			if err := ctx.Err(); err != nil {
				return messages, err
			}
		}
	}

	content := "Reached the maximum tool-step limit; this turn has stopped."
	callAssistant(args.OnAssistant, content)
	return append(messages, message.AssistantMessage(content)), nil
}

func shouldTreatAssistantAsProgress(kind message.ContentKind, content string, sawToolResult bool) bool {
	if kind == message.ContentProgress {
		return true
	}
	if kind == message.ContentFinal || !sawToolResult {
		return false
	}
	return !looksLikeClarifyingQuestion(content)
}

func formatDiagnostics(diagnostics message.Diagnostics) string {
	parts := []string{}
	if diagnostics.StopReason != "" {
		parts = append(parts, "stop_reason="+diagnostics.StopReason)
	}
	if len(diagnostics.BlockTypes) > 0 {
		parts = append(parts, "blocks="+strings.Join(diagnostics.BlockTypes, ","))
	}
	if len(diagnostics.IgnoredBlockTypes) > 0 {
		parts = append(parts, "ignored="+strings.Join(diagnostics.IgnoredBlockTypes, ","))
	}
	if len(parts) == 0 {
		return ""
	}
	return " Diagnostics: " + strings.Join(parts, "; ") + "."
}

func looksLikeClarifyingQuestion(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	asksDirectQuestion := strings.HasSuffix(trimmed, "?") ||
		strings.HasSuffix(trimmed, "？") ||
		strings.Contains(lower, "would you like") ||
		strings.Contains(lower, "what would you like") ||
		strings.Contains(trimmed, "请告诉我") ||
		strings.Contains(trimmed, "请选择")
	if !asksDirectQuestion {
		return false
	}
	userHints := []string{"你", "您", "would you", "do you", "which", "what", "prefer", "want", "choose", "confirm"}
	decisionHints := []string{"希望", "想要", "选择", "确认", "决定", "偏好", "prefer", "want", "choose", "confirm", "decide", "preference"}
	return containsAny(lower, trimmed, userHints) && containsAny(lower, trimmed, decisionHints)
}

func containsAny(lower, original string, hints []string) bool {
	for _, hint := range hints {
		if strings.Contains(lower, hint) || strings.Contains(original, hint) {
			return true
		}
	}
	return false
}

func isRecoverableThinkingStop(isEmpty bool, diagnostics message.Diagnostics) bool {
	if !isEmpty {
		return false
	}
	if diagnostics.StopReason != "pause_turn" && diagnostics.StopReason != "max_tokens" {
		return false
	}
	for _, blockType := range diagnostics.IgnoredBlockTypes {
		if blockType == "thinking" {
			return true
		}
	}
	return false
}

func callAssistant(fn func(string), content string) {
	if fn != nil {
		fn(content)
	}
}

func callProgress(fn func(string), content string) {
	if fn != nil {
		fn(content)
	}
}

func callToolStart(fn func(string, any), name string, input any) {
	if fn != nil {
		fn(name, input)
	}
}

func callToolResult(fn func(string, string, bool), name, output string, isError bool) {
	if fn != nil {
		fn(name, output, isError)
	}
}

func callUsage(fn func(message.TokenUsage), usage message.TokenUsage) {
	if fn == nil {
		return
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 {
		return
	}
	fn(usage)
}
