package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/sandbox"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

func TestSanitizeToolSegment(t *testing.T) {
	if got := SanitizeToolSegment("My Server!"); got != "my_server" {
		t.Fatalf("got %q", got)
	}
}

func TestCreateBackedToolsWrapsNewlineJSONServer(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") == "1" {
		runFakeMCPServer()
		return
	}

	result := CreateBackedTools(context.Background(), t.TempDir(), map[string]config.MCPServerConfig{
		"fake": {
			Command:  os.Args[0],
			Args:     []string{"-test.run=TestCreateBackedToolsWrapsNewlineJSONServer"},
			Env:      map[string]any{"GO_WANT_MCP_HELPER": "1", "MCP_TEST_PROTOCOL": "newline-json"},
			Protocol: "newline-json",
		},
	}, protocolTestOptions())
	defer result.Dispose(context.Background())

	if len(result.Servers) != 1 || result.Servers[0].Status != "connected" || result.Servers[0].ToolCount != 1 {
		t.Fatalf("unexpected server summary: %#v", result.Servers)
	}
	registry := tools.NewRegistry(result.Tools, tools.Metadata{})
	if got := registry.Execute(context.Background(), "mcp__fake__hello", map[string]any{"name": "Ada"}, tools.Context{}); got.OK {
		t.Fatal("MCP call allowed without permission manager")
	}
	toolResult := registry.Execute(context.Background(), "mcp__fake__hello", map[string]any{"name": "Ada"}, tools.Context{Permission: testPermission{}})
	if !toolResult.OK || !strings.Contains(toolResult.Output, "Hello Ada") {
		t.Fatalf("unexpected tool result: %#v", toolResult)
	}

	resources := registry.Execute(context.Background(), "list_mcp_resources", map[string]any{}, tools.Context{})
	if !resources.OK || !strings.Contains(resources.Output, "fake: demo://resource") {
		t.Fatalf("unexpected resources: %#v", resources)
	}
}

func TestCreateBackedToolsWrapsContentLengthServer(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") == "1" {
		runFakeMCPServer()
		return
	}

	result := CreateBackedTools(context.Background(), t.TempDir(), map[string]config.MCPServerConfig{
		"fake": {
			Command:  os.Args[0],
			Args:     []string{"-test.run=TestCreateBackedToolsWrapsContentLengthServer"},
			Env:      map[string]any{"GO_WANT_MCP_HELPER": "1", "MCP_TEST_PROTOCOL": "content-length"},
			Protocol: "content-length",
		},
	}, protocolTestOptions())
	defer result.Dispose(context.Background())

	if len(result.Servers) != 1 || result.Servers[0].Protocol != "content-length" || result.Servers[0].Status != "connected" {
		t.Fatalf("unexpected summary: %#v", result.Servers)
	}
}

func TestClientFailsClosedAfterTimedOutRequest(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") == "1" {
		runFakeMCPServer()
		return
	}

	client := &stdioClient{
		prepare:    protocolTestPrepare,
		serverName: "fake",
		cwd:        t.TempDir(),
		config: config.MCPServerConfig{
			Command:  os.Args[0],
			Args:     []string{"-test.run=TestClientFailsClosedAfterTimedOutRequest"},
			Env:      map[string]any{"GO_WANT_MCP_HELPER": "1", "MCP_TEST_PROTOCOL": "newline-json"},
			Protocol: "newline-json",
		},
	}
	if err := client.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.close()

	shortCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err := client.request(shortCtx, "tools/call", map[string]any{"name": "slow", "arguments": map[string]any{"delay": "150ms"}})
	cancel()
	if err == nil || !strings.Contains(err.Error(), "request timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.listTools(ctx); err == nil {
		t.Fatal("timed-out server remained usable")
	}
	if client.cmd != nil || client.cleanup != nil {
		t.Fatal("timed-out server was not cleaned up")
	}
	if err := client.start(context.Background()); err != nil {
		t.Fatalf("explicit restart failed: %v", err)
	}
	descriptors, err := client.listTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 1 || descriptors[0].Name != "hello" {
		t.Fatalf("unexpected tools after timeout: %#v", descriptors)
	}
}

func protocolTestOptions() Options {
	return Options{Authorize: func(string, config.MCPServerConfig) bool { return true }, Prepare: protocolTestPrepare}
}

func protocolTestPrepare(ctx context.Context, options sandbox.Options) (*exec.Cmd, func(), error) {
	cmd := exec.CommandContext(ctx, options.Command, options.Args...)
	cmd.Dir = options.CWD
	cmd.Env = sandbox.HostEnvironment()
	for key, value := range options.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd, func() {}, nil
}

type testPermission struct{}

func (testPermission) EnsurePathAccess(context.Context, string, string) error        { return nil }
func (testPermission) EnsureCommand(context.Context, string, []string, string) error { return nil }
func (testPermission) EnsureEdit(context.Context, string, string) error              { return nil }

func TestUntrustedServerNeverStarts(t *testing.T) {
	called := false
	result := CreateBackedTools(context.Background(), t.TempDir(), map[string]config.MCPServerConfig{"untrusted": {Command: "must-not-run"}}, Options{Prepare: func(context.Context, sandbox.Options) (*exec.Cmd, func(), error) {
		called = true
		return nil, nil, fmt.Errorf("must not be called")
	}})
	if called || len(result.Servers) != 1 || result.Servers[0].Status != "untrusted" {
		t.Fatal("untrusted process started")
	}
}

func TestMCPFramesAreBounded(t *testing.T) {
	for _, header := range []string{"Content-Length: 999999999\r\n\r\n", "Content-Length: -1\r\n\r\n", strings.Repeat("A", maxHeaderBytes+1)} {
		if _, err := readContentLengthMessage(strings.NewReader(header)); err == nil {
			t.Fatal("unsafe frame accepted")
		}
	}
	if _, err := readBoundedLine(bufio.NewReader(bytes.NewReader(bytes.Repeat([]byte{'x'}, maxMessageBytes+1))), maxMessageBytes); err == nil {
		t.Fatal("oversized newline message accepted")
	}
}

func runFakeMCPServer() {
	protocol := os.Getenv("MCP_TEST_PROTOCOL")
	reader := newProtocolReader(os.Stdin, protocol)
	for {
		msg, err := reader.read()
		if err != nil {
			return
		}
		method, _ := msg["method"].(string)
		id, hasID := msg["id"]
		if !hasID {
			continue
		}
		if method == os.Getenv("MCP_TEST_OPTIONAL_METHOD") {
			switch os.Getenv("MCP_TEST_OPTIONAL_RESULT") {
			case "timeout":
				time.Sleep(30 * time.Second)
				continue
			case "unsupported":
				writeProtocolMessage(os.Stdout, protocol, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "Method not found"}})
				continue
			}
		}
		var result any
		switch method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05"}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name":        "hello",
				"description": "Say hello",
				"inputSchema": map[string]any{"type": "object"},
			}}}
		case "resources/list":
			result = map[string]any{"resources": []map[string]any{{
				"uri":         "demo://resource",
				"name":        "Demo",
				"description": "Demo resource",
			}}}
		case "resources/read":
			result = map[string]any{"contents": []map[string]any{{"uri": "demo://resource", "text": "resource body"}}}
		case "prompts/list":
			result = map[string]any{"prompts": []map[string]any{{"name": "demo", "description": "Demo prompt"}}}
		case "prompts/get":
			result = map[string]any{"description": "Demo prompt", "messages": []map[string]any{{"role": "user", "content": "hello"}}}
		case "tools/call":
			params, _ := msg["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			if delay, _ := args["delay"].(string); delay != "" {
				duration, _ := time.ParseDuration(delay)
				time.Sleep(duration)
			}
			name, _ := args["name"].(string)
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "Hello " + name}}}
		default:
			result = map[string]any{}
		}
		writeProtocolMessage(os.Stdout, protocol, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
}

type protocolReader struct {
	source   io.Reader
	protocol string
	decoder  *json.Decoder
}

func newProtocolReader(source io.Reader, protocol string) protocolReader {
	return protocolReader{source: source, protocol: protocol, decoder: json.NewDecoder(source)}
}

func (r protocolReader) read() (map[string]any, error) {
	if r.protocol == "newline-json" {
		var msg map[string]any
		err := r.decoder.Decode(&msg)
		return msg, err
	}
	data, err := readContentLengthMessage(r.source)
	if err != nil {
		return nil, err
	}
	var msg map[string]any
	return msg, json.Unmarshal(data, &msg)
}

func writeProtocolMessage(out io.Writer, protocol string, msg map[string]any) {
	data, _ := json.Marshal(msg)
	if protocol == "newline-json" {
		fmt.Fprintln(out, string(data))
		return
	}
	fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(data), data)
}
