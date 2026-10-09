package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/brand"
	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/egress"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type exactRemoteOrigin string

func (allowed exactRemoteOrigin) EnsureNetwork(_ context.Context, origin, _ string) error {
	if origin != string(allowed) {
		return fmt.Errorf("remote MCP origin changed")
	}
	return nil
}

type remoteClient struct {
	serverName        string
	config            config.MCPServerConfig
	audit             func(egress.Event) error
	httpClientForTest *http.Client
	session           *sdk.ClientSession
	protocol          string
	toolsChanged      atomic.Bool
	resourcesChanged  atomic.Bool
	promptsChanged    atomic.Bool
}

func (c *remoteClient) start(ctx context.Context) error {
	if c.config.Command != "" || len(c.config.Args) > 0 || len(c.config.Env) > 0 || c.config.CWD != "" {
		return fmt.Errorf("remote MCP must use only an HTTPS URL, without command or environment fields")
	}
	origin, _, err := egress.Origin(c.config.URL)
	if err != nil {
		return err
	}
	if c.config.Protocol != "" && c.config.Protocol != "streamable-http" && c.config.Protocol != "auto" {
		return fmt.Errorf("remote MCP protocol must be streamable-http")
	}
	connection := &egress.Client{Permission: exactRemoteOrigin(origin), Audit: c.audit}
	httpClient := connection.HTTPClient()
	if c.httpClientForTest != nil {
		httpClient = c.httpClientForTest
	}
	client := sdk.NewClient(&sdk.Implementation{Name: brand.AgentName, Version: brand.Version}, &sdk.ClientOptions{
		Capabilities:               &sdk.ClientCapabilities{},
		ToolListChangedHandler:     func(context.Context, *sdk.ToolListChangedRequest) { c.toolsChanged.Store(true) },
		ResourceListChangedHandler: func(context.Context, *sdk.ResourceListChangedRequest) { c.resourcesChanged.Store(true) },
		PromptListChangedHandler:   func(context.Context, *sdk.PromptListChangedRequest) { c.promptsChanged.Store(true) },
	})
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	transport := &sdk.StreamableClientTransport{Endpoint: c.config.URL, HTTPClient: httpClient, MaxRetries: -1, MaxEventSize: egress.MaxResponseBytes}
	session, err := client.Connect(connectCtx, transport, &sdk.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		return fmt.Errorf("remote MCP connection failed: %w", err)
	}
	c.session = session
	if result := session.InitializeResult(); result != nil {
		c.protocol = "streamable-http/" + result.ProtocolVersion
	} else {
		c.protocol = "streamable-http"
	}
	return nil
}

func (c *remoteClient) protocolName() string {
	if c.protocol != "" {
		return c.protocol
	}
	return "streamable-http"
}
func (c *remoteClient) supports(kind string) bool {
	if c.session == nil || c.session.InitializeResult() == nil || c.session.InitializeResult().Capabilities == nil {
		return true
	}
	capabilities := c.session.InitializeResult().Capabilities
	switch kind {
	case "tools":
		return capabilities.Tools != nil
	case "resources":
		return capabilities.Resources != nil
	case "prompts":
		return capabilities.Prompts != nil
	default:
		return false
	}
}
func (c *remoteClient) health() error {
	if c.session == nil {
		return fmt.Errorf("remote MCP is not connected")
	}
	return nil
}
func (c *remoteClient) close() error {
	if c.session == nil {
		return nil
	}
	err := c.session.Close()
	c.session = nil
	return err
}

func (c *remoteClient) listTools(ctx context.Context) ([]toolDescriptor, error) {
	c.toolsChanged.Store(false)
	success := false
	defer func() {
		if !success {
			c.toolsChanged.Store(true)
		}
	}()
	var out []toolDescriptor
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 32; page++ {
		result, err := c.session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, item := range result.Tools {
			if item == nil || item.Name == "" {
				return nil, fmt.Errorf("remote MCP returned an unnamed tool")
			}
			schema, ok := item.InputSchema.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("remote MCP tool has an invalid input schema")
			}
			if err := validateRemoteSchema(schema, 0); err != nil {
				return nil, err
			}
			out = append(out, toolDescriptor{Name: item.Name, Description: item.Description, InputSchema: schema})
			if len(out) > 512 {
				return nil, fmt.Errorf("remote MCP tool limit exceeded")
			}
		}
		if result.NextCursor == "" {
			success = true
			return out, nil
		}
		if seen[result.NextCursor] {
			return nil, fmt.Errorf("remote MCP repeated a pagination cursor")
		}
		seen[result.NextCursor] = true
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("remote MCP pagination limit exceeded")
}

func (c *remoteClient) listResources(ctx context.Context) ([]resourceDescriptor, error) {
	c.resourcesChanged.Store(false)
	success := false
	defer func() {
		if !success {
			c.resourcesChanged.Store(true)
		}
	}()
	var out []resourceDescriptor
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 32; page++ {
		result, err := c.session.ListResources(ctx, &sdk.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, item := range result.Resources {
			if item == nil || item.URI == "" {
				return nil, fmt.Errorf("remote MCP returned an invalid resource")
			}
			out = append(out, resourceDescriptor{URI: item.URI, Name: item.Name, Description: item.Description, MimeType: item.MIMEType})
			if len(out) > 512 {
				return nil, fmt.Errorf("remote MCP resource limit exceeded")
			}
		}
		if result.NextCursor == "" {
			success = true
			return out, nil
		}
		if seen[result.NextCursor] {
			return nil, fmt.Errorf("remote MCP repeated a pagination cursor")
		}
		seen[result.NextCursor] = true
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("remote MCP pagination limit exceeded")
}

func (c *remoteClient) listPrompts(ctx context.Context) ([]promptDescriptor, error) {
	c.promptsChanged.Store(false)
	success := false
	defer func() {
		if !success {
			c.promptsChanged.Store(true)
		}
	}()
	var out []promptDescriptor
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 32; page++ {
		result, err := c.session.ListPrompts(ctx, &sdk.ListPromptsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, item := range result.Prompts {
			if item == nil || item.Name == "" {
				return nil, fmt.Errorf("remote MCP returned an invalid prompt")
			}
			out = append(out, promptDescriptor{Name: item.Name, Description: item.Description})
			if len(out) > 512 {
				return nil, fmt.Errorf("remote MCP prompt limit exceeded")
			}
		}
		if result.NextCursor == "" {
			success = true
			return out, nil
		}
		if seen[result.NextCursor] {
			return nil, fmt.Errorf("remote MCP repeated a pagination cursor")
		}
		seen[result.NextCursor] = true
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("remote MCP pagination limit exceeded")
}

func (c *remoteClient) callTool(ctx context.Context, name string, input any) tools.Result {
	if c.toolsChanged.Load() {
		fresh, err := c.listTools(ctx)
		if err != nil {
			return tools.Error(err.Error())
		}
		found := false
		for _, item := range fresh {
			if item.Name == name {
				found = true
				break
			}
		}
		if !found {
			return tools.Error("Remote MCP tool was removed; restart to refresh available tools")
		}
	}
	result, err := c.session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		return tools.Error(err.Error())
	}
	return formatToolCallResult(result)
}

func (c *remoteClient) readResource(ctx context.Context, uri string) tools.Result {
	if c.resourcesChanged.Load() {
		fresh, err := c.listResources(ctx)
		if err != nil {
			return tools.Error(err.Error())
		}
		found := false
		for _, item := range fresh {
			if item.URI == uri {
				found = true
				break
			}
		}
		if !found {
			return tools.Error("Remote MCP resource was removed; restart to refresh available resources")
		}
	}
	result, err := c.session.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri})
	if err != nil {
		return tools.Error(err.Error())
	}
	return formatReadResourceResult(result)
}
func (c *remoteClient) getPrompt(ctx context.Context, name string, args map[string]string) tools.Result {
	if c.promptsChanged.Load() {
		fresh, err := c.listPrompts(ctx)
		if err != nil {
			return tools.Error(err.Error())
		}
		found := false
		for _, item := range fresh {
			if item.Name == name {
				found = true
				break
			}
		}
		if !found {
			return tools.Error("Remote MCP prompt was removed; restart to refresh available prompts")
		}
	}
	result, err := c.session.GetPrompt(ctx, &sdk.GetPromptParams{Name: name, Arguments: args})
	if err != nil {
		return tools.Error(err.Error())
	}
	return formatPromptResult(result)
}

func validateRemoteSchema(value any, depth int) error {
	if depth > 32 {
		return fmt.Errorf("remote MCP schema nesting limit exceeded")
	}
	switch item := value.(type) {
	case map[string]any:
		if len(item) > 128 {
			return fmt.Errorf("remote MCP schema property limit exceeded")
		}
		for key, child := range item {
			if key == "$ref" {
				ref, ok := child.(string)
				if !ok || !strings.HasPrefix(ref, "#/") {
					return fmt.Errorf("remote MCP external schema references are blocked")
				}
			}
			if err := validateRemoteSchema(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(item) > 256 {
			return fmt.Errorf("remote MCP schema array limit exceeded")
		}
		for _, child := range item {
			if err := validateRemoteSchema(child, depth+1); err != nil {
				return err
			}
		}
	case string:
		if len(item) > 16384 {
			return fmt.Errorf("remote MCP schema string limit exceeded")
		}
	case json.Number, float64, int, bool, nil:
	default:
		return fmt.Errorf("remote MCP schema has an unsupported value")
	}
	return nil
}
