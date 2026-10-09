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

type OpenAI struct {
	runtime RuntimeProvider
	tools   *tools.Registry
	client  *http.Client
}

func NewOpenAI(runtime RuntimeProvider, registry *tools.Registry) *OpenAI {
	return &OpenAI{runtime: runtime, tools: registry, client: newHTTPClient()}
}

func (o *OpenAI) Next(ctx context.Context, messages []message.Message) (message.Step, error) {
	return o.NextStream(ctx, messages, nil)
}

func (o *OpenAI) NextStream(ctx context.Context, messages []message.Message, onTextDelta func(string)) (step message.Step, err error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	runtime, err := o.runtime(ctx)
	if err != nil {
		return message.Step{}, err
	}
	wireAPI := normalizeOpenAIWireAPI(runtime.WireAPI)
	if wireAPI == "responses" {
		return o.nextResponses(ctx, runtime, messages, onTextDelta)
	}
	return o.nextChatCompletions(ctx, runtime, messages, onTextDelta)
}

func (o *OpenAI) nextChatCompletions(ctx context.Context, runtime config.Runtime, messages []message.Message, onTextDelta func(string)) (message.Step, error) {
	payload := toOpenAIPayload(runtime, o.tools, messages)
	if onTextDelta != nil {
		payload["stream"] = true
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	res, err := requestJSON(ctx, o.client, openAIChatCompletionsURL(runtime.BaseURL), payload, func(req *http.Request) { setOpenAIAuth(req, runtime) })
	if err != nil {
		return message.Step{}, err
	}
	defer res.Body.Close()

	var data openAIChatResponse
	if isSSE(res) {
		data, err = readChatStream(res.Body, onTextDelta)
	} else {
		err = decodeResponse(res.Body, &data)
	}
	if err != nil {
		return message.Step{}, err
	}
	if data.Error.Message != "" {
		return message.Step{}, &RequestError{Reason: "provider returned an error"}
	}
	if len(data.Choices) == 0 {
		return message.AssistantStep("", message.ContentNone, message.Diagnostics{StopReason: "empty_choices"}), nil
	}

	choice := data.Choices[0]
	if choice.FinishReason == "length" || choice.FinishReason == "content_filter" {
		return message.Step{}, &RequestError{Reason: "incomplete model output (" + choice.FinishReason + ")"}
	}
	content, kind := ParseAssistantText(strings.TrimSpace(choice.Message.Content))
	calls := []message.ToolCall{}
	for _, call := range choice.Message.ToolCalls {
		input := any(map[string]any{})
		if strings.TrimSpace(call.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
				input = map[string]any{"_invalidArguments": call.Function.Arguments}
			}
		}
		calls = append(calls, message.ToolCall{ID: call.ID, ToolName: call.Function.Name, Input: input})
	}
	diagnostics := message.Diagnostics{StopReason: choice.FinishReason, BlockTypes: []string{"message"}}
	diagnostics.Usage = openAIUsageToDiagnostics(data.Usage)
	if len(calls) > 0 {
		contentKind := message.ContentNone
		if kind == message.ContentProgress {
			contentKind = message.ContentProgress
		}
		return message.ToolCallsStep(calls, content, contentKind, diagnostics), nil
	}
	return message.AssistantStep(content, kind, diagnostics), nil
}

func (o *OpenAI) nextResponses(ctx context.Context, runtime config.Runtime, messages []message.Message, onTextDelta func(string)) (message.Step, error) {
	payload := toOpenAIResponsesPayload(runtime, o.tools, messages)
	if onTextDelta != nil {
		payload["stream"] = true
	}
	res, err := requestJSON(ctx, o.client, openAIResponsesURL(runtime.BaseURL), payload, func(req *http.Request) { setOpenAIAuth(req, runtime) })
	if err != nil {
		return message.Step{}, err
	}
	defer res.Body.Close()

	var data openAIResponsesResponse
	if isSSE(res) {
		data, err = readResponsesStream(res.Body, onTextDelta)
	} else {
		err = decodeResponse(res.Body, &data)
	}
	if err != nil {
		return message.Step{}, err
	}
	if err := validateResponsesStatus(data); err != nil {
		return message.Step{}, err
	}

	content, kind, calls, diagnostics := parseOpenAIResponsesOutput(data.Output)
	diagnostics.Usage = openAIUsageToDiagnostics(data.Usage)
	diagnostics.StopReason = data.Status
	state := &message.ProviderState{Protocol: "openai_responses"}
	for _, item := range data.Output {
		raw := item.Raw
		if len(raw) == 0 {
			raw, _ = json.Marshal(item)
		}
		state.Items = append(state.Items, raw)
	}
	var step message.Step
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

func setOpenAIAuth(req *http.Request, runtime config.Runtime) {
	if runtime.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+runtime.AuthToken)
		return
	}
	if runtime.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+runtime.APIKey)
	}
}

func openAIChatCompletionsURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch {
	case base == "":
		return "https://api.openai.com/v1/chat/completions"
	case strings.HasSuffix(base, "/v1"):
		return base + "/chat/completions"
	case strings.HasSuffix(base, "/chat/completions"):
		return base
	default:
		return base + "/v1/chat/completions"
	}
}

func openAIResponsesURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch {
	case base == "":
		return "https://api.openai.com/v1/responses"
	case strings.HasSuffix(base, "/v1"):
		return base + "/responses"
	case strings.HasSuffix(base, "/responses"):
		return base
	default:
		return base + "/v1/responses"
	}
}

type openAIChatResponse struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage   openAIUsage `json:"usage"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

type openAIResponsesResponse struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
	Output []openAIResponseOutputItem `json:"output"`
	Usage  openAIResponsesUsage       `json:"usage"`
}

type openAIUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
}

type openAIResponsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
}

type openAIResponseOutputItem struct {
	Raw          json.RawMessage              `json:"-"`
	Type         string                       `json:"type"`
	Role         string                       `json:"role,omitempty"`
	ID           string                       `json:"id,omitempty"`
	Name         string                       `json:"name,omitempty"`
	Status       string                       `json:"status,omitempty"`
	Content      []openAIResponseContentBlock `json:"content,omitempty"`
	Arguments    string                       `json:"arguments,omitempty"`
	CallID       string                       `json:"call_id,omitempty"`
	ToolCallID   string                       `json:"tool_call_id,omitempty"`
	Output       string                       `json:"output,omitempty"`
	FinishReason string                       `json:"finish_reason,omitempty"`
}

func (item *openAIResponseOutputItem) UnmarshalJSON(raw []byte) error {
	type decoded openAIResponseOutputItem
	if err := json.Unmarshal(raw, (*decoded)(item)); err != nil {
		return err
	}
	item.Raw = append(json.RawMessage(nil), raw...)
	return nil
}

type openAIResponseContentBlock struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Content string `json:"content,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

func parseOpenAIResponsesOutput(items []openAIResponseOutputItem) (string, message.ContentKind, []message.ToolCall, message.Diagnostics) {
	textParts := []string{}
	calls := []message.ToolCall{}
	blockTypes := []string{}
	ignored := []string{}
	ignoredSeen := map[string]bool{}

	for _, item := range items {
		if item.Type != "" {
			blockTypes = append(blockTypes, item.Type)
		}
		switch item.Type {
		case "message":
			for _, block := range item.Content {
				blockTypes = append(blockTypes, block.Type)
				switch block.Type {
				case "refusal":
					textParts = append(textParts, block.Refusal)
				case "output_text", "text":
					if block.Text != "" {
						textParts = append(textParts, block.Text)
					} else if block.Content != "" {
						textParts = append(textParts, block.Content)
					}
				case "tool_call":
					call := message.ToolCall{ID: firstNonEmptyString(item.ID, block.Content), ToolName: firstNonEmptyString(item.Name, block.Kind), Input: map[string]any{}}
					if block.Content != "" {
						var input any
						if err := json.Unmarshal([]byte(block.Content), &input); err == nil {
							call.Input = input
						} else {
							call.Input = map[string]any{"_invalidArguments": block.Content}
						}
					}
					calls = append(calls, call)
				default:
					if !ignoredSeen[block.Type] {
						ignoredSeen[block.Type] = true
						ignored = append(ignored, block.Type)
					}
				}
			}
		case "output_text":
			if item.Output != "" {
				textParts = append(textParts, item.Output)
			}
		case "function_call":
			input := any(map[string]any{})
			if strings.TrimSpace(item.Arguments) != "" {
				if err := json.Unmarshal([]byte(item.Arguments), &input); err != nil {
					input = map[string]any{"_invalidArguments": item.Arguments}
				}
			}
			calls = append(calls, message.ToolCall{ID: firstNonEmptyString(item.CallID, item.ID), ToolName: item.Name, Input: input})
		case "reasoning":
			// Opaque reasoning is retained in ProviderState, never displayed.
		case "tool_call_output":
			if item.Output != "" {
				textParts = append(textParts, item.Output)
			}
		default:
			if !ignoredSeen[item.Type] {
				ignoredSeen[item.Type] = true
				ignored = append(ignored, item.Type)
			}
		}
	}

	content, kind := ParseAssistantText(strings.TrimSpace(strings.Join(textParts, "\n")))
	return content, kind, calls, message.Diagnostics{BlockTypes: blockTypes, IgnoredBlockTypes: ignored}
}

func toOpenAIPayload(runtime config.Runtime, registry *tools.Registry, messages []message.Message) map[string]any {
	apiMessages := []map[string]any{}
	for _, msg := range messages {
		switch msg.Role {
		case message.RoleSystem:
			apiMessages = append(apiMessages, map[string]any{"role": "system", "content": msg.Content})
		case message.RoleUser:
			apiMessages = append(apiMessages, map[string]any{"role": "user", "content": msg.Content})
		case message.RoleAssistant, message.RoleAssistantProgress:
			text := msg.Content
			if msg.Role == message.RoleAssistantProgress {
				text = "<progress>\n" + msg.Content + "\n</progress>"
			}
			apiMessages = append(apiMessages, map[string]any{"role": "assistant", "content": text})
		case message.RoleAssistantToolCall:
			call := map[string]any{
				"id":   msg.ToolUseID,
				"type": "function",
				"function": map[string]any{
					"name":      msg.ToolName,
					"arguments": mustJSONString(msg.Input),
				},
			}
			if len(apiMessages) == 0 || apiMessages[len(apiMessages)-1]["role"] != "assistant" {
				apiMessages = append(apiMessages, map[string]any{"role": "assistant", "content": ""})
			}
			last := apiMessages[len(apiMessages)-1]
			calls, _ := last["tool_calls"].([]map[string]any)
			last["tool_calls"] = append(calls, call)
		case message.RoleToolResult:
			apiMessages = append(apiMessages, map[string]any{"role": "tool", "tool_call_id": msg.ToolUseID, "content": msg.Content})
		}
	}
	toolSchemas := []map[string]any{}
	for _, definition := range registry.List() {
		toolSchemas = append(toolSchemas, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        definition.Name,
				"description": definition.Description,
				"parameters":  definition.InputSchema,
			},
		})
	}
	payload := map[string]any{
		"model":    runtime.Model,
		"messages": apiMessages,
	}
	if len(toolSchemas) > 0 {
		payload["tools"] = toolSchemas
	}
	if runtime.MaxOutputTokens > 0 {
		payload["max_tokens"] = runtime.MaxOutputTokens
	}
	return payload
}

func toOpenAIResponsesPayload(runtime config.Runtime, registry *tools.Registry, messages []message.Message) map[string]any {
	input := []map[string]any{}
	for _, msg := range messages {
		if msg.MirrorProtocol == "openai_responses" {
			continue
		}
		switch msg.Role {
		case message.RoleProviderState:
			if msg.ProviderState != nil && msg.ProviderState.Protocol == "openai_responses" {
				for _, raw := range msg.ProviderState.Items {
					input = append(input, rawObject(raw))
				}
			}
		case message.RoleSystem:
			input = append(input, map[string]any{"role": "system", "content": msg.Content})
		case message.RoleUser:
			input = append(input, map[string]any{"role": "user", "content": msg.Content})
		case message.RoleAssistant, message.RoleAssistantProgress:
			text := msg.Content
			if msg.Role == message.RoleAssistantProgress {
				text = "<progress>\n" + msg.Content + "\n</progress>"
			}
			input = append(input, map[string]any{"role": "assistant", "content": text})
		case message.RoleAssistantToolCall:
			input = append(input, map[string]any{
				"type":      "function_call",
				"call_id":   msg.ToolUseID,
				"name":      msg.ToolName,
				"arguments": mustJSONString(msg.Input),
			})
		case message.RoleToolResult:
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": msg.ToolUseID,
				"output":  msg.Content,
			})
		}
	}
	toolsPayload := []map[string]any{}
	for _, definition := range registry.List() {
		toolsPayload = append(toolsPayload, map[string]any{
			"type":        "function",
			"name":        definition.Name,
			"description": definition.Description,
			"parameters":  definition.InputSchema,
		})
	}
	payload := map[string]any{
		"model":   runtime.Model,
		"input":   input,
		"include": []string{"reasoning.encrypted_content"},
	}
	if len(toolsPayload) > 0 {
		payload["tools"] = toolsPayload
	}
	if runtime.ReasoningEffort != "" {
		payload["reasoning"] = map[string]any{
			"effort": normalizeOpenAIReasoningEffort(runtime.ReasoningEffort),
		}
	}
	if runtime.DisableResponseStorage {
		payload["store"] = false
	}
	if runtime.MaxOutputTokens > 0 {
		payload["max_output_tokens"] = runtime.MaxOutputTokens
	}
	return payload
}

func normalizeOpenAIWireAPI(wireAPI string) string {
	switch strings.ToLower(strings.TrimSpace(wireAPI)) {
	case "", "chat", "chat_completions", "chat-completions":
		return "chat_completions"
	case "responses", "response":
		return "responses"
	default:
		return "chat_completions"
	}
}

func normalizeOpenAIReasoningEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "xlow", "low":
		return "low"
	case "medium", "mid":
		return "medium"
	case "high", "xhigh", "extra-high":
		return "high"
	default:
		return raw
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func openAIUsageToDiagnostics(usage any) message.TokenUsage {
	switch typed := usage.(type) {
	case openAIUsage:
		total := typed.TotalTokens
		if total == 0 {
			total = typed.PromptTokens + typed.CompletionTokens
		}
		return message.TokenUsage{
			InputTokens:      typed.PromptTokens,
			OutputTokens:     typed.CompletionTokens,
			TotalTokens:      total,
			CacheReadTokens:  typed.PromptTokensDetails.CachedTokens,
			CacheWriteTokens: typed.PromptTokensDetails.CacheWriteTokens,
		}
	case openAIResponsesUsage:
		total := typed.TotalTokens
		if total == 0 {
			total = typed.InputTokens + typed.OutputTokens
		}
		return message.TokenUsage{
			InputTokens:      typed.InputTokens,
			OutputTokens:     typed.OutputTokens,
			TotalTokens:      total,
			CacheReadTokens:  typed.InputTokensDetails.CachedTokens,
			CacheWriteTokens: typed.InputTokensDetails.CacheWriteTokens,
		}
	default:
		return message.TokenUsage{}
	}
}

func mustJSONString(value any) string {
	bytes, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(bytes)
}
