package egress

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Exercise the SDK over the same guarded HTTP client used by remoteClient.
// Unlike an injected httptest.Client, this checks header and transport policy.
func TestStreamableMCPThroughGuardedTransport(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo"}, func(_ context.Context, _ *sdk.CallToolRequest, input struct {
		Text string `json:"text"`
	}) (*sdk.CallToolResult, struct {
		Text string `json:"text"`
	}, error) {
		return nil, struct {
			Text string `json:"text"`
		}{Text: input.Text}, nil
	})
	https := httptest.NewTLSServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: true}))
	defer https.Close()
	pool := x509.NewCertPool()
	pool.AddCert(https.Certificate())
	permission := &allowOrigin{}
	var phases []string
	connection := &Client{Permission: permission, allowPrivateForTest: true, rootCAsForTest: pool, Audit: func(event Event) error {
		phases = append(phases, event.Phase)
		return nil
	}}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: https.URL, HTTPClient: connection.HTTPClient(), MaxRetries: -1, MaxEventSize: MaxResponseBytes}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 {
		t.Fatalf("tool discovery through guard: %v, %#v", err, tools)
	}
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "guarded"}})
	if err != nil || !strings.Contains(strings.Join(phases, ","), "completed") || result == nil {
		t.Fatalf("guarded call failed: %v, %#v, audit %#v", err, result, phases)
	}
	for _, seen := range permission.seen {
		if !strings.HasSuffix(seen, " "+https.URL) {
			t.Fatalf("MCP escaped exact origin: %q", seen)
		}
	}
}
