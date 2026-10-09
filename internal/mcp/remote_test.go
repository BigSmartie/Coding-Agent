package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRemoteMCPModernStreamableHTTP(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1.0"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "Echo an input"}, func(_ context.Context, _ *sdk.CallToolRequest, input struct {
		Text string `json:"text"`
	}) (*sdk.CallToolResult, struct {
		Text string `json:"text"`
	}, error) {
		return nil, struct {
			Text string `json:"text"`
		}{Text: input.Text}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: true})
	https := httptest.NewTLSServer(handler)
	defer https.Close()
	client := &remoteClient{serverName: "fixture", config: config.MCPServerConfig{URL: https.URL, Protocol: "streamable-http"}, httpClientForTest: https.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.close()
	if !strings.Contains(client.protocolName(), "2026-07-28") {
		t.Fatalf("wrong negotiated protocol: %s", client.protocolName())
	}
	listed, err := client.listTools(ctx)
	if err != nil || len(listed) != 1 || listed[0].Name != "echo" {
		t.Fatalf("remote discovery failed: %#v, %v", listed, err)
	}
	result := client.callTool(ctx, "echo", map[string]any{"text": "hello remote"})
	if !result.OK || !strings.Contains(result.Output, "hello remote") {
		t.Fatalf("remote call failed: %#v", result)
	}
}

type remoteApprovalPermission struct {
	testPermission
	called int
}

func (p *remoteApprovalPermission) EnsureRemoteMCP(_ context.Context, server, operation, origin, _ string) error {
	p.called++
	if server != "fixture" || operation != "echo" || origin != "https://example.com" {
		return fmt.Errorf("unexpected remote approval scope")
	}
	return nil
}

func TestRemoteCallUsesOperationApproval(t *testing.T) {
	client := &remoteClient{config: config.MCPServerConfig{URL: "https://example.com/mcp"}}
	if err := authorizeClientCall(context.Background(), tools.Context{Permission: testPermission{}}, client, "fixture", "echo", []byte(`{}`)); err == nil {
		t.Fatal("remote call fell back to command approval")
	}
	permission := &remoteApprovalPermission{}
	if err := authorizeClientCall(context.Background(), tools.Context{Permission: permission}, client, "fixture", "echo", []byte(`{}`)); err != nil || permission.called != 1 {
		t.Fatalf("operation approval not used: %v, calls %d", err, permission.called)
	}
}

func TestRemoteMCPRejectsCredentialAndCommandMix(t *testing.T) {
	for _, config := range []config.MCPServerConfig{
		{URL: "http://example.com/mcp"},
		{URL: "https://token@example.com/mcp"},
		{URL: "https://example.com/mcp", Command: "sh"},
		{URL: "https://example.com/mcp", Env: map[string]any{"TOKEN": "x"}},
	} {
		client := &remoteClient{serverName: "bad", config: config}
		if err := client.start(context.Background()); err == nil {
			t.Fatalf("unsafe remote config accepted: %#v", config)
		}
	}
}

func TestRemoteSchemaRejectsExternalReferences(t *testing.T) {
	if err := validateRemoteSchema(map[string]any{"$ref": "https://evil.invalid/schema"}, 0); err == nil {
		t.Fatal("external schema fetched or allowed")
	}
	if err := validateRemoteSchema(map[string]any{"properties": map[string]any{"a": map[string]any{"$ref": "#/$defs/a"}}}, 0); err != nil {
		t.Fatal(err)
	}
}
