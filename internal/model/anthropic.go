package model

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

type RuntimeProvider func(context.Context) (config.Runtime, error)

type Anthropic struct {
	runtime RuntimeProvider
	tools   *tools.Registry
	client  *http.Client
}

func NewAnthropic(runtime RuntimeProvider, registry *tools.Registry) *Anthropic {
	return &Anthropic{runtime: runtime, tools: registry, client: newHTTPClient()}
}

func (a *Anthropic) Next(ctx context.Context, messages []message.Message) (message.Step, error) {
	return a.NextStream(ctx, messages, nil)
}

func (a *Anthropic) NextStream(ctx context.Context, messages []message.Message, onTextDelta func(string)) (step message.Step, err error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	runtime, err := a.runtime(ctx)
	if err != nil {
		return message.Step{}, err
	}
	payload := toAnthropicPayload(runtime, a.tools, messages)
	if onTextDelta != nil {
		payload["stream"] = true
	}
	res, err := requestJSON(ctx, a.client, anthropicMessagesURL(runtime.BaseURL), payload, func(req *http.Request) {
		req.Header.Set("anthropic-version", "2023-06-01")
		if runtime.AuthToken != "" {
			req.Header.Set("Authorization", "Bearer "+runtime.AuthToken)
		} else if runtime.APIKey != "" {
			req.Header.Set("x-api-key", runtime.APIKey)
		}
	})
	if err != nil {
		return message.Step{}, err
	}
	defer res.Body.Close()

	var data anthropicResponse
	if isSSE(res) {
		data, err = readAnthropicStream(res.Body, onTextDelta)
	} else {
		err = decodeResponse(res.Body, &data)
	}
	if err != nil {
		return message.Step{}, err
	}
	if data.Error != nil {
		return message.Step{}, &RequestError{Reason: "provider returned an error"}
	}

	textParts := []string{}
	calls := []message.ToolCall{}
	blockTypes := []string{}
	ignored := []string{}
	ignoredSeen := map[string]bool{}
	for _, block := range data.Content {
		blockTypes = append(blockTypes, block.Type)
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			calls = append(calls, message.ToolCall{ID: block.ID, ToolName: block.Name, Input: block.Input})
		default:
			if !ignoredSeen[block.Type] {
				ignoredSeen[block.Type] = true
				ignored = append(ignored, block.Type)
			}
		}
	}

	content, kind := ParseAssistantText(strings.TrimSpace(strings.Join(textParts, "\n")))
	diagnostics := message.Diagnostics{StopReason: data.StopReason, BlockTypes: blockTypes, IgnoredBlockTypes: ignored}
	diagnostics.Usage = anthropicUsageToDiagnostics(data)
	if data.StopReason == "max_tokens" && (len(calls) > 0 || content != "") {
		return message.Step{}, &RequestError{Reason: "incomplete model output (max_tokens)"}
	}
	state := &message.ProviderState{Protocol: "anthropic_messages"}
	for _, block := range data.Content {
		raw := block.Raw
		if len(raw) == 0 {
			raw, _ = json.Marshal(block)
		}
		state.Items = append(state.Items, raw)
	}
	if len(calls) > 0 {
		contentKind := message.ContentNone
		if kind == message.ContentProgress {
			contentKind = message.ContentProgress
		}
		step = message.ToolCallsStep(calls, content, contentKind, diagnostics)
	} else {
		step = message.AssistantStep(content, kind, diagnostics)
	}
	step.ProviderState = state
	return step, nil
}

func anthropicUsageToDiagnostics(data anthropicResponse) message.TokenUsage {
	return message.TokenUsage{
		InputTokens:      data.Usage.InputTokens + data.Usage.CacheCreationInputTokens + data.Usage.CacheReadInputTokens,
		OutputTokens:     data.Usage.OutputTokens,
		TotalTokens:      data.Usage.InputTokens + data.Usage.OutputTokens + data.Usage.CacheCreationInputTokens + data.Usage.CacheReadInputTokens,
		CacheReadTokens:  data.Usage.CacheReadInputTokens,
		CacheWriteTokens: data.Usage.CacheCreationInputTokens,
	}
}

type anthropicResponse struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	StopReason string         `json:"stop_reason"`
	Content    []contentBlock `json:"content"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func anthropicMessagesURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "https://api.anthropic.com/v1/messages"
	}
	if strings.HasSuffix(base, "/messages") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

type contentBlock struct {
	Raw   json.RawMessage `json:"-"`
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input any             `json:"input,omitempty"`
}

func (block *contentBlock) UnmarshalJSON(raw []byte) error {
	type decoded contentBlock
	if err := json.Unmarshal(raw, (*decoded)(block)); err != nil {
		return err
	}
	block.Raw = append(json.RawMessage(nil), raw...)
	return nil
}

func ParseAssistantText(content string) (string, message.ContentKind) {
	trimmed := strings.TrimSpace(content)
	markers := []struct {
		prefix string
		kind   message.ContentKind
		close  string
	}{
		{"<final>", message.ContentFinal, "</final>"},
		{"[FINAL]", message.ContentFinal, ""},
		{"<progress>", message.ContentProgress, "</progress>"},
		{"[PROGRESS]", message.ContentProgress, ""},
	}
	for _, marker := range markers {
		if strings.HasPrefix(trimmed, marker.prefix) {
			out := strings.TrimSpace(strings.TrimPrefix(trimmed, marker.prefix))
			if marker.close != "" {
				out = strings.TrimSpace(strings.ReplaceAll(out, marker.close, ""))
			}
			return out, marker.kind
		}
	}
	return trimmed, message.ContentNone
}

func toAnthropicPayload(runtime config.Runtime, registry *tools.Registry, messages []message.Message) map[string]any {
	systemParts := []string{}
	apiMessages := []map[string]any{}
	for _, msg := range messages {
		if msg.MirrorProtocol == "anthropic_messages" {
			continue
		}
		switch msg.Role {
		case message.RoleProviderState:
			if msg.ProviderState != nil && msg.ProviderState.Protocol == "anthropic_messages" {
				for _, raw := range msg.ProviderState.Items {
					apiMessages = appendAnthropicBlock(apiMessages, "assistant", rawObject(raw))
				}
			}
		case message.RoleSystem:
			systemParts = append(systemParts, msg.Content)
		case message.RoleUser:
			apiMessages = appendAnthropicBlock(apiMessages, "user", map[string]any{"type": "text", "text": msg.Content})
		case message.RoleAssistant, message.RoleAssistantProgress:
			text := msg.Content
			if msg.Role == message.RoleAssistantProgress {
				text = "<progress>\n" + msg.Content + "\n</progress>"
			}
			apiMessages = appendAnthropicBlock(apiMessages, "assistant", map[string]any{"type": "text", "text": text})
		case message.RoleAssistantToolCall:
			apiMessages = appendAnthropicBlock(apiMessages, "assistant", map[string]any{"type": "tool_use", "id": msg.ToolUseID, "name": msg.ToolName, "input": msg.Input})
		case message.RoleToolResult:
			apiMessages = appendAnthropicBlock(apiMessages, "user", map[string]any{"type": "tool_result", "tool_use_id": msg.ToolUseID, "content": msg.Content, "is_error": msg.IsError})
		}
	}
	toolSchemas := []map[string]any{}
	for _, definition := range registry.List() {
		toolSchemas = append(toolSchemas, map[string]any{
			"name":         definition.Name,
			"description":  definition.Description,
			"input_schema": definition.InputSchema,
		})
	}
	payload := map[string]any{
		"max_tokens": 4096,
		"model":      runtime.Model,
		"system":     strings.Join(systemParts, "\n\n"),
		"messages":   apiMessages,
		"tools":      toolSchemas,
	}
	if runtime.MaxOutputTokens > 0 {
		payload["max_tokens"] = runtime.MaxOutputTokens
	}
	return payload
}

func appendAnthropicBlock(messages []map[string]any, role string, block map[string]any) []map[string]any {
	if len(messages) > 0 && messages[len(messages)-1]["role"] == role {
		content := messages[len(messages)-1]["content"].([]map[string]any)
		messages[len(messages)-1]["content"] = append(content, block)
		return messages
	}
	return append(messages, map[string]any{"role": role, "content": []map[string]any{block}})
}
