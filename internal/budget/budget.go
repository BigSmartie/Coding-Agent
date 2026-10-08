package budget

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

const summaryMarker = "[context compaction v1]"

type Options struct {
	ContextWindowTokens  int
	MaxOutputTokens      int
	PreviousInputTokens  int
	PreviousRequestBytes int
	CurrentUserPrompt    string
}

type Result struct {
	Messages        []message.Message
	EstimatedTokens int
	RequestBytes    int
	Compacted       bool
}

// Prepare budgets a request before the model is called. Serialized bytes are
// only an estimate of provider tokens; the last provider-reported input count
// anchors growth during a multi-step turn. An unknown window (zero) disables
// automatic compaction instead of inventing a model limit.
func Prepare(messages []message.Message, registry *tools.Registry, opts Options) (Result, error) {
	if opts.ContextWindowTokens <= 0 {
		return Result{Messages: messages}, nil
	}
	result, err := estimate(messages, registry, opts)
	if err != nil {
		return result, err
	}
	reserve := opts.MaxOutputTokens
	if reserve <= 0 {
		reserve = min(4096, opts.ContextWindowTokens/4)
	}
	limit := opts.ContextWindowTokens - reserve - max(256, opts.ContextWindowTokens/10)
	if limit <= 0 {
		return Result{}, fmt.Errorf("configured context window leaves no input budget after output reserve")
	}
	if result.EstimatedTokens <= limit {
		return result, nil
	}

	systemEnd := 0
	for systemEnd < len(messages) && messages[systemEnd].Role == message.RoleSystem {
		systemEnd++
	}
	pending := map[string]int{}
	for cut := systemEnd + 1; cut < len(messages); cut++ {
		previous := messages[cut-1]
		switch previous.Role {
		case message.RoleAssistantToolCall:
			pending[previous.ToolUseID]++
		case message.RoleToolResult:
			pending[previous.ToolUseID]--
			if pending[previous.ToolUseID] == 0 {
				delete(pending, previous.ToolUseID)
			}
		}
		if len(pending) != 0 || !safeCut(previous, messages[cut]) {
			continue
		}
		candidate := make([]message.Message, 0, systemEnd+1+len(messages)-cut)
		candidate = append(candidate, messages[:systemEnd]...)
		summary := message.UserMessage(summaryFor(messages[systemEnd:cut], messages[cut:], opts.CurrentUserPrompt))
		candidate = append(candidate, summary)
		candidate = append(candidate, messages[cut:]...)
		measured, err := estimate(candidate, registry, Options{})
		if err != nil {
			return Result{}, err
		}
		if measured.EstimatedTokens <= limit {
			measured.Compacted = true
			return measured, nil
		}
	}
	return Result{}, fmt.Errorf("request exceeds configured context budget; no complete tool-call/result boundary can be compacted safely")
}

func safeCut(previous, next message.Message) bool {
	if next.Role == message.RoleToolResult || next.Role == message.RoleAssistantToolCall || next.MirrorProtocol != "" {
		return false
	}
	if previous.Role == message.RoleToolResult {
		return true
	}
	return previous.Role == message.RoleAssistant && next.Role == message.RoleUser
}

func summaryFor(removed, retained []message.Message, currentPrompt string) string {
	if currentPrompt == "" {
		for _, msg := range removed {
			if msg.Role == message.RoleUser && !strings.HasPrefix(msg.Content, summaryMarker) {
				currentPrompt = msg.Content
			}
		}
	}
	for _, msg := range retained {
		if msg.Role == message.RoleUser && msg.Content == currentPrompt {
			currentPrompt = ""
			break
		}
	}
	if currentPrompt == "" {
		return fmt.Sprintf("%s %d earlier messages were omitted to fit the configured context window. Reinspect the workspace before relying on old tool results.", summaryMarker, len(removed))
	}
	return fmt.Sprintf("%s %d earlier messages were omitted to fit the configured context window. Reinspect the workspace before relying on old tool results. Current user request: %s", summaryMarker, len(removed), strings.TrimSpace(currentPrompt))
}

func estimate(messages []message.Message, registry *tools.Registry, opts Options) (Result, error) {
	encoded, err := json.Marshal(messages)
	if err != nil {
		return Result{}, fmt.Errorf("cannot budget transcript: %w", err)
	}
	requestBytes := len(encoded) + len(messages)*32
	if registry != nil {
		for _, definition := range registry.List() {
			schema, err := json.Marshal(definition.InputSchema)
			if err != nil {
				return Result{}, fmt.Errorf("cannot budget tool schema %s: %w", definition.Name, err)
			}
			requestBytes += len(definition.Name) + len(definition.Description) + len(schema) + 128
		}
	}
	// The byte/3 estimate is deliberately padded. Provider usage is authoritative
	// after a response; any increase in serialized bytes is charged at 1:1.
	estimated := (requestBytes+2)/3 + 512
	if opts.PreviousInputTokens > 0 && opts.PreviousRequestBytes > 0 && requestBytes >= opts.PreviousRequestBytes {
		estimated = max(estimated, opts.PreviousInputTokens+requestBytes-opts.PreviousRequestBytes)
	}
	return Result{Messages: messages, EstimatedTokens: estimated, RequestBytes: requestBytes}, nil
}
