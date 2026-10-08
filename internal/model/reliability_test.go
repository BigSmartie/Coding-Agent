package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

func testOpenAI(endpoint, wire string) *OpenAI {
	return NewOpenAI(func(context.Context) (config.Runtime, error) {
		return config.Runtime{Provider: "openai", Model: "test", BaseURL: endpoint, APIKey: "secret-test-key", WireAPI: wire}, nil
	}, tools.NewRegistry(nil, tools.Metadata{}))
}

func writeSSE(w http.ResponseWriter, data string) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func TestChatSSEArrivesBeforeCompletionAndAssemblesTools(t *testing.T) {
	ack := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Error("stream not requested")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, `{"choices":[{"index":0,"delta":{"content":"<progress>reading"}}]}`)
		select {
		case <-ack:
		case <-r.Context().Done():
			return
		}
		writeSSE(w, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`)
		writeSSE(w, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]},"finish_reason":"tool_calls"}]}`)
		writeSSE(w, `{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`)
		writeSSE(w, `[DONE]`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var visible strings.Builder
	step, err := testOpenAI(server.URL, "").NextStream(ctx, nil, func(delta string) { visible.WriteString(delta); close(ack) })
	if err != nil {
		t.Fatal(err)
	}
	if visible.String() != "<progress>reading" || step.Content != "reading" || len(step.Calls) != 1 {
		t.Fatalf("bad stream: %#v", step)
	}
	if step.Calls[0].Input.(map[string]any)["path"] != "a.go" || step.Diagnostics.Usage.TotalTokens != 10 {
		t.Fatalf("lost streamed arguments/usage: %#v", step)
	}
}

func TestStreamingCancellationDoesNotRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, `{"choices":[{"index":0,"delta":{"content":"first"}}]}`)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := testOpenAI(server.URL, "").NextStream(ctx, nil, func(string) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("stream retried %d times", requests.Load())
	}
}

func TestTransientStatusRetriesAndPermanentErrorsStayPrivate(t *testing.T) {
	for _, status := range []int{429, 503, 401} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 || status == 401 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"error":{"message":"secret-test-key and sensitive request content"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`)
			}))
			defer server.Close()
			step, err := testOpenAI(server.URL, "").Next(context.Background(), nil)
			if status == 401 {
				var requestError *RequestError
				if !errors.As(err, &requestError) || requestError.StatusCode != 401 || requests.Load() != 1 {
					t.Fatalf("bad error/retry: %v", err)
				}
				if strings.Contains(err.Error(), "secret-test-key") || strings.Contains(err.Error(), server.URL) {
					t.Fatal("error exposed credentials or URL")
				}
			} else if err != nil || step.Content != "ok" || requests.Load() != 2 {
				t.Fatalf("retry failed: %#v, %v, count=%d", step, err, requests.Load())
			}
		})
	}
}

func TestRetryAfterAndCancellation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		value    string
		expected time.Duration
	}{{"3", 3 * time.Second}, {now.Add(5 * time.Second).Format(http.TimeFormat), 5 * time.Second}} {
		delay, ok := parseRetryAfter(tc.value, now)
		if !ok || delay != tc.expected {
			t.Fatalf("Retry-After %q: %v %v", tc.value, delay, ok)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(waitRetry(ctx, 30*time.Second), context.Canceled) {
		t.Fatal("retry wait ignored cancellation")
	}
}

func TestRedirectNeverForwardsCredential(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, err := testOpenAI(source.URL, "").Next(context.Background(), nil)
	if err == nil || requests.Load() != 0 {
		t.Fatalf("credential redirect followed: %v", err)
	}
}

func TestResponsesOpaqueStateSurvivesPersistenceWithoutDuplicateCalls(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			writeSSE(w, `{"type":"response.output_text.delta","delta":"checking"}`)
			writeSSE(w, `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque-blob"},{"type":"message","id":"msg_1","phase":"commentary","role":"assistant","content":[{"type":"output_text","text":"checking"}]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{}"},{"type":"function_call","id":"fc_2","call_id":"call_2","name":"read_file","arguments":"{}"}]}}`)
			return
		}
		var body struct {
			Input []map[string]any `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Input) != 7 {
			t.Errorf("duplicated/missing items: %#v", body.Input)
		}
		if len(body.Input) >= 7 {
			if body.Input[1]["encrypted_content"] != "opaque-blob" || body.Input[2]["phase"] != "commentary" {
				t.Error("opaque reasoning or phase lost")
			}
			if body.Input[3]["call_id"] != "call_1" || body.Input[4]["call_id"] != "call_2" || body.Input[5]["type"] != "function_call_output" || body.Input[6]["type"] != "function_call_output" {
				t.Error("call/result order corrupted")
			}
		}
		_, _ = io.WriteString(w, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`)
	}))
	defer server.Close()
	adapter := testOpenAI(server.URL, "responses")
	step, err := adapter.NextStream(context.Background(), []message.Message{message.UserMessage("hi")}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	history := []message.Message{message.UserMessage("hi"), {Role: message.RoleProviderState, ProviderState: step.ProviderState}, {Role: message.RoleAssistant, Content: step.Content, MirrorProtocol: "openai_responses"}}
	for _, call := range step.Calls {
		msg := message.AssistantToolCallMessage(call)
		msg.MirrorProtocol = "openai_responses"
		history = append(history, msg)
	}
	for _, call := range step.Calls {
		history = append(history, message.ToolResultMessage(call.ID, call.ToolName, "ok", false))
	}
	saved, _ := json.Marshal(history)
	var resumed []message.Message
	if err := json.Unmarshal(saved, &resumed); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Next(context.Background(), resumed); err != nil {
		t.Fatal(err)
	}
}

func TestIncompleteAndOversizedResponsesFailClosed(t *testing.T) {
	for _, status := range []string{"incomplete", "failed", "in_progress"} {
		if validateResponsesStatus(openAIResponsesResponse{Status: status}) == nil {
			t.Fatalf("accepted %s", status)
		}
	}
	if _, err := readResponsesStream(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"), func(string) {}); err == nil {
		t.Fatal("accepted truncated Responses stream")
	}
	if _, err := readChatStream(strings.NewReader("data: {\"choices\":[]}\n\n"), nil); err == nil {
		t.Fatal("accepted truncated chat stream")
	}
	if err := readSSE(strings.NewReader("data: "+strings.Repeat("x", maxEventBytes)+"\n\n"), func(string, []byte) error { return nil }); err == nil {
		t.Fatal("accepted oversized event")
	}
	var target any
	if err := decodeResponse(strings.NewReader(strings.Repeat(" ", maxResponseBytes+1)), &target); err == nil {
		t.Fatal("accepted oversized response")
	}
	if _, err := requestJSON(context.Background(), nil, "https://unused.invalid", strings.Repeat("x", maxRequestBytes), func(*http.Request) {}); err == nil {
		t.Fatal("accepted oversized request")
	}
}

func TestAnthropicStreamingPreservesThinkingSignature(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":8}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"opaque-thought"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hello"}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		`data: {"type":"message_stop"}`,
	}, "\n\n") + "\n\n"
	var visible string
	response, err := readAnthropicStream(strings.NewReader(stream), func(text string) { visible += text })
	if err != nil {
		t.Fatal(err)
	}
	if visible != "hello" || len(response.Content) != 2 || !strings.Contains(string(response.Content[0].Raw), `"signature":"sig"`) {
		t.Fatalf("thinking leaked/lost: %#v", response)
	}
	payload := toAnthropicPayload(config.Runtime{Model: "test"}, tools.NewRegistry(nil, tools.Metadata{}), []message.Message{{Role: message.RoleProviderState, ProviderState: &message.ProviderState{Protocol: "anthropic_messages", Items: []json.RawMessage{response.Content[0].Raw}}}})
	if payload["max_tokens"] != 4096 {
		t.Fatal("required default max_tokens missing")
	}
	encoded, _ := json.Marshal(payload)
	if !strings.Contains(string(encoded), `"signature":"sig"`) {
		t.Fatal("signature lost on replay")
	}
}

func TestChatMultipleCallsShareOneAssistantMessage(t *testing.T) {
	state := []message.Message{message.UserMessage("hi"), message.AssistantToolCallMessage(message.ToolCall{ID: "a", ToolName: "read", Input: map[string]any{}}), message.AssistantToolCallMessage(message.ToolCall{ID: "b", ToolName: "read", Input: map[string]any{}}), message.ToolResultMessage("a", "read", "a", false), message.ToolResultMessage("b", "read", "b", false)}
	payload := toOpenAIPayload(config.Runtime{}, tools.NewRegistry(nil, tools.Metadata{}), state)
	messages := payload["messages"].([]map[string]any)
	if len(messages) != 4 || len(messages[1]["tool_calls"].([]map[string]any)) != 2 {
		t.Fatalf("tool batch split: %#v", messages)
	}
}

func TestAnthropicTruncatedTextDoesNotReportSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"stop_reason":"max_tokens","content":[{"type":"text","text":"Partial answer"}]}`)
	}))
	defer server.Close()
	adapter := NewAnthropic(func(context.Context) (config.Runtime, error) {
		return config.Runtime{Model: "test", BaseURL: server.URL}, nil
	}, tools.NewRegistry(nil, tools.Metadata{}))
	_, err := adapter.Next(context.Background(), []message.Message{message.UserMessage("hello")})
	if err == nil || !strings.Contains(err.Error(), "incomplete model output") {
		t.Fatalf("truncated text accepted: %v", err)
	}
}
