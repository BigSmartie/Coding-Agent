package model

import (
	"encoding/json"
	"io"
	"sort"
)

func validateResponsesStatus(data openAIResponsesResponse) error {
	if data.Error.Message != "" {
		return &RequestError{Reason: "provider returned a response error"}
	}
	if data.IncompleteDetails != nil || data.Status == "incomplete" {
		reason := "incomplete model output"
		if data.IncompleteDetails != nil {
			switch data.IncompleteDetails.Reason {
			case "max_output_tokens", "content_filter":
				reason += " (" + data.IncompleteDetails.Reason + ")"
			}
		}
		return &RequestError{Reason: reason}
	}
	// Empty status remains compatible with older Responses-compatible gateways.
	if data.Status != "" && data.Status != "completed" {
		return &RequestError{Reason: "response did not complete"}
	}
	return nil
}

func readResponsesStream(body io.Reader, onTextDelta func(string)) (openAIResponsesResponse, error) {
	var response openAIResponsesResponse
	completed := false
	err := readSSE(body, func(eventName string, data []byte) error {
		var event struct {
			Type     string                  `json:"type"`
			Delta    string                  `json:"delta"`
			Response openAIResponsesResponse `json:"response"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return &RequestError{Reason: "invalid Responses stream event"}
		}
		kind := firstNonEmptyString(event.Type, eventName)
		switch kind {
		case "response.output_text.delta", "response.refusal.delta":
			emitText(onTextDelta, event.Delta)
		case "response.completed":
			response, completed = event.Response, true
			if err := validateResponsesStatus(response); err != nil {
				return err
			}
			return io.EOF
		case "response.failed", "response.incomplete", "error":
			return &RequestError{Reason: "response stream failed or ended incomplete"}
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return response, err
	}
	if !completed {
		return response, &RequestError{Reason: "response stream ended before completion"}
	}
	return response, nil
}

func readChatStream(body io.Reader, onTextDelta func(string)) (openAIChatResponse, error) {
	var response openAIChatResponse
	var content, finish string
	toolCalls := map[int]map[string]any{}
	done := false
	err := readSSE(body, func(_ string, data []byte) error {
		if string(data) == "[DONE]" {
			done = true
			return io.EOF
		}
		var event struct {
			Error   json.RawMessage `json:"error"`
			Usage   *openAIUsage    `json:"usage"`
			Choices []struct {
				Index        int    `json:"index"`
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return &RequestError{Reason: "invalid chat stream event"}
		}
		if len(event.Error) != 0 && string(event.Error) != "null" {
			return &RequestError{Reason: "provider returned a stream error"}
		}
		if event.Usage != nil {
			response.Usage = *event.Usage
		}
		for _, choice := range event.Choices {
			if choice.Index != 0 {
				continue
			}
			content += choice.Delta.Content
			emitText(onTextDelta, choice.Delta.Content)
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			for _, delta := range choice.Delta.ToolCalls {
				if delta.Index < 0 || delta.Index > 255 {
					return &RequestError{Reason: "invalid tool call index"}
				}
				call := toolCalls[delta.Index]
				if call == nil {
					call = map[string]any{"id": "", "type": "function", "function": map[string]any{"name": "", "arguments": ""}}
					toolCalls[delta.Index] = call
				}
				if delta.ID != "" {
					call["id"] = delta.ID
				}
				fn := call["function"].(map[string]any)
				fn["name"] = fn["name"].(string) + delta.Function.Name
				fn["arguments"] = fn["arguments"].(string) + delta.Function.Arguments
			}
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return response, err
	}
	if !done || finish == "" {
		return response, &RequestError{Reason: "chat stream ended before completion"}
	}
	indices := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	calls := make([]map[string]any, 0, len(indices))
	for _, index := range indices {
		calls = append(calls, toolCalls[index])
	}
	encoded, _ := json.Marshal(map[string]any{"usage": response.Usage, "choices": []map[string]any{{"finish_reason": finish, "message": map[string]any{"role": "assistant", "content": content, "tool_calls": calls}}}})
	if err := json.Unmarshal(encoded, &response); err != nil {
		return response, &RequestError{Reason: "invalid assembled chat response"}
	}
	return response, nil
}
