package mcp

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/sandbox"
)

func TestConcurrentMCPCloseCleansEachGenerationOnce(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") == "1" {
		runFakeMCPServer()
		return
	}
	var cleaned atomic.Int32
	client := &stdioClient{serverName: "fake", cwd: t.TempDir(), config: config.MCPServerConfig{Command: os.Args[0], Args: []string{"-test.run=TestConcurrentMCPCloseCleansEachGenerationOnce"}, Env: map[string]any{"GO_WANT_MCP_HELPER": "1", "MCP_TEST_PROTOCOL": "newline-json"}, Protocol: "newline-json"}}
	client.prepare = func(ctx context.Context, options sandbox.Options) (*exec.Cmd, func(), error) {
		cmd, _, err := protocolTestPrepare(ctx, options)
		return cmd, func() { cleaned.Add(1) }, err
	}
	for generation := int32(1); generation <= 2; generation++ {
		if err := client.start(context.Background()); err != nil {
			t.Fatal(err)
		}
		var group sync.WaitGroup
		for i := 0; i < 8; i++ {
			group.Add(1)
			go func() { defer group.Done(); _ = client.close() }()
		}
		group.Wait()
		if cleaned.Load() != generation {
			t.Fatalf("cleanup count=%d generation=%d", cleaned.Load(), generation)
		}
	}
}

func TestOversizedStderrIsDrained(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	client := &stdioClient{}
	drained := make(chan struct{})
	go func() { client.captureStderr(reader); close(drained) }()
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(writer, strings.Repeat("x", 256*1024)+"\nlast line\n")
		writer.Close()
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("oversized stderr blocked writer")
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("stderr reader did not exit")
	}
}

func TestCanceledMCPRequestStopsServer(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") == "1" {
		runFakeMCPServer()
		return
	}
	client := &stdioClient{prepare: protocolTestPrepare, serverName: "fake", cwd: t.TempDir(), config: config.MCPServerConfig{Command: os.Args[0], Args: []string{"-test.run=TestCanceledMCPRequestStopsServer"}, Env: map[string]any{"GO_WANT_MCP_HELPER": "1", "MCP_TEST_PROTOCOL": "newline-json"}, Protocol: "newline-json"}}
	if err := client.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.close()
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(20*time.Millisecond, cancel)
	defer timer.Stop()
	if _, err := client.request(ctx, "tools/call", map[string]any{"name": "slow", "arguments": map[string]any{"delay": "30s"}}); err == nil {
		t.Fatal("cancellation ignored")
	}
	if client.cmd != nil {
		t.Fatal("canceled MCP process still active")
	}
}

func TestOptionalDiscoveryFailureDoesNotExposeStoppedServer(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") == "1" {
		runFakeMCPServer()
		return
	}
	for _, method := range []string{"resources/list", "prompts/list"} {
		for _, outcome := range []string{"timeout", "unsupported"} {
			t.Run(method+"/"+outcome, func(t *testing.T) {
				t.Parallel()
				result := CreateBackedTools(context.Background(), t.TempDir(), map[string]config.MCPServerConfig{"fake": {Command: os.Args[0], Args: []string{"-test.run=TestOptionalDiscoveryFailureDoesNotExposeStoppedServer"}, Env: map[string]any{"GO_WANT_MCP_HELPER": "1", "MCP_TEST_PROTOCOL": "newline-json", "MCP_TEST_OPTIONAL_METHOD": method, "MCP_TEST_OPTIONAL_RESULT": outcome}, Protocol: "newline-json"}}, protocolTestOptions())
				defer result.Dispose(context.Background())
				if len(result.Servers) != 1 {
					t.Fatalf("unexpected summaries: %#v", result.Servers)
				}
				if outcome == "timeout" {
					if result.Servers[0].Status != "error" || result.Servers[0].ToolCount != 0 || len(result.Tools) != 0 {
						t.Fatalf("stopped server exposed capabilities: %#v tools=%d", result.Servers, len(result.Tools))
					}
				} else {
					if result.Servers[0].Status != "connected" || result.Servers[0].ToolCount != 1 {
						t.Fatalf("unsupported optional method disabled healthy server: %#v", result.Servers)
					}
					found := false
					for _, tool := range result.Tools {
						if tool.Name == "mcp__fake__hello" {
							found = true
						}
					}
					if !found {
						t.Fatal("healthy server tool disappeared")
					}
				}
			})
		}
	}
}
