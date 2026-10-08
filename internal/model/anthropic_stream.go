package model

import (
	"encoding/json"
	"io"
	"sort"
)

func readAnthropicStream(body io.Reader, onTextDelta func(string)) (anthropicResponse, error) {
	var response anthropicResponse
	blocks := map[int]map[string]any{}
	arguments := map[int]string{}
	done := false
	err := readSSE(body, func(eventName string, data []byte) error {
		var event struct {
			Type         string            `json:"type"`
			Index        int               `json:"index"`
			Message      anthropicResponse `json:"message"`
			ContentBlock map[string]any    `json:"content_block"`
			Delta        struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return &RequestError{Reason: "invalid Anthropic stream event"}
		}
		if event.Index < 0 || event.Index > 255 {
			return &RequestError{Reason: "invalid content block index"}
		}
		switch firstNonEmptyString(event.Type, eventName) {
		case "message_start":
			response = event.Message
		case "content_block_start":
			if event.ContentBlock == nil {
				return &RequestError{Reason: "missing stream content block"}
			}
			blocks[event.Index] = event.ContentBlock
			if text, ok := event.ContentBlock["text"].(string); ok {
				emitText(onTextDelta, text)
			}
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil {
				return &RequestError{Reason: "stream delta has no content block"}
			}
			appendField := func(name, value string) { old, _ := block[name].(string); block[name] = old + value }
			switch event.Delta.Type {
			case "text_delta":
				appendField("text", event.Delta.Text)
				emitText(onTextDelta, event.Delta.Text)
			case "input_json_delta":
				arguments[event.Index] += event.Delta.PartialJSON
			case "thinking_delta":
				appendField("thinking", event.Delta.Thinking)
			case "signature_delta":
				appendField("signature", event.Delta.Signature)
			}
		case "message_delta":
			response.StopReason = event.Delta.StopReason
			response.Usage.OutputTokens = event.Usage.OutputTokens
		case "message_stop":
			done = true
			return io.EOF
		case "error":
			return &RequestError{Reason: "provider returned a stream error"}
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return response, err
	}
	if !done || response.StopReason == "" {
		return response, &RequestError{Reason: "Anthropic stream ended before completion"}
	}
	indices := make([]int, 0, len(blocks))
	for index := range blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	response.Content = nil
	for _, index := range indices {
		block := blocks[index]
		if args := arguments[index]; args != "" {
			var input any
			if json.Unmarshal([]byte(args), &input) != nil {
				return response, &RequestError{Reason: "invalid streamed tool arguments"}
			}
			block["input"] = input
		}
		raw, _ := json.Marshal(block)
		var decoded contentBlock
		if json.Unmarshal(raw, &decoded) != nil {
			return response, &RequestError{Reason: "invalid stream content"}
		}
		response.Content = append(response.Content, decoded)
	}
	return response, nil
}
