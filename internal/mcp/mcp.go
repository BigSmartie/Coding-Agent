package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/brand"
	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/egress"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"github.com/BigSmartie/Coding-Agent/internal/sandbox"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

const maxMessageBytes = 4 << 20
const maxHeaderBytes = 8 << 10

type Options struct {
	Authorize    func(string, config.MCPServerConfig) bool
	NetworkAudit func(egress.Event) error
	// Prepare defaults to the isolated sandbox. Injection supports protocol tests.
	Prepare func(context.Context, sandbox.Options) (*exec.Cmd, func(), error)
}

type Result struct {
	Tools   []tools.Definition
	Servers []tools.MCPServerSummary
	Dispose func(context.Context) error
}

type jsonRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcResponse struct {
	msg jsonRPCMessage
	err error
}

type toolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

type resourceDescriptor struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type promptDescriptor struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type stdioClient struct {
	serverName       string
	config           config.MCPServerConfig
	cwd              string
	protocol         string
	cmd              *exec.Cmd
	stdin            io.WriteCloser
	reader           *bufio.Reader
	nextID           int
	mu               sync.Mutex
	lifecycleMu      sync.Mutex
	writeMu          sync.Mutex
	generation       int
	readErr          error
	prepare          func(context.Context, sandbox.Options) (*exec.Cmd, func(), error)
	cleanup          func()
	cancel           context.CancelFunc
	pending          map[int]chan rpcResponse
	stderrMu         sync.Mutex
	stderrLines      []string
	toolsChanged     atomic.Bool
	resourcesChanged atomic.Bool
	promptsChanged   atomic.Bool
	capabilities     map[string]any
}

type mcpClient interface {
	start(context.Context) error
	listTools(context.Context) ([]toolDescriptor, error)
	listResources(context.Context) ([]resourceDescriptor, error)
	listPrompts(context.Context) ([]promptDescriptor, error)
	callTool(context.Context, string, any) tools.Result
	readResource(context.Context, string) tools.Result
	getPrompt(context.Context, string, map[string]string) tools.Result
	close() error
	protocolName() string
	health() error
	supports(string) bool
}

func CreateBackedTools(ctx context.Context, cwd string, servers map[string]config.MCPServerConfig, options ...Options) Result {
	var opts Options
	if len(options) > 0 {
		opts = options[0]
	}
	clients := []mcpClient{}
	definitions := []tools.Definition{}
	summaries := []tools.MCPServerSummary{}
	resourceIndex := map[string]resourceEntry{}
	promptIndex := map[string]promptEntry{}

	for serverName, serverConfig := range servers {
		serverTarget := serverConfig.Command
		if serverConfig.URL != "" {
			serverTarget = serverConfig.URL
		}
		if serverConfig.Enabled != nil && !*serverConfig.Enabled {
			summaries = append(summaries, tools.MCPServerSummary{
				Name:      serverName,
				Command:   serverTarget,
				Status:    "disabled",
				ToolCount: 0,
				Protocol:  configuredProtocol(serverConfig.Protocol),
			})
			continue
		}

		if opts.Authorize == nil || !opts.Authorize(serverName, serverConfig) {
			summaries = append(summaries, tools.MCPServerSummary{Name: serverName, Command: serverTarget, Status: "untrusted", Error: "Review with mythoscode trust mcp <server> before enabling this configuration."})
			continue
		}
		var client mcpClient
		if serverConfig.URL != "" {
			client = &remoteClient{serverName: serverName, config: serverConfig, audit: opts.NetworkAudit}
		} else {
			client = &stdioClient{serverName: serverName, config: serverConfig, cwd: cwd, prepare: opts.Prepare}
		}
		if err := client.start(ctx); err != nil {
			_ = client.close()
			summaries = append(summaries, tools.MCPServerSummary{
				Name:      serverName,
				Command:   serverTarget,
				Status:    "error",
				ToolCount: 0,
				Error:     err.Error(),
				Protocol:  client.protocolName(),
			})
			continue
		}
		var descriptors []toolDescriptor
		if client.supports("tools") {
			var err error
			descriptors, err = client.listTools(ctx)
			if err != nil {
				_ = client.close()
				summaries = append(summaries, tools.MCPServerSummary{Name: serverName, Command: serverTarget, Status: "error", Error: safety.Redact(ctx, err.Error()), Protocol: client.protocolName()})
				continue
			}
		}
		var resources []resourceDescriptor
		if client.supports("resources") {
			resources, _ = client.listResources(ctx)
		}
		var prompts []promptDescriptor
		if client.supports("prompts") {
			prompts, _ = client.listPrompts(ctx)
		}
		// Optional methods may be unsupported while the connection stays healthy.
		// A timeout or transport failure instead invalidates every discovered tool.
		connectionErr := client.health()
		if connectionErr != nil {
			_ = client.close()
			summaries = append(summaries, tools.MCPServerSummary{Name: serverName, Command: serverTarget, Status: "error", Error: safety.Redact(ctx, connectionErr.Error()), Protocol: client.protocolName()})
			continue
		}
		clients = append(clients, client)

		for _, resource := range resources {
			resourceIndex[serverName+":"+resource.URI] = resourceEntry{serverName: serverName, resource: resource, client: client}
		}
		for _, prompt := range prompts {
			promptIndex[serverName+":"+prompt.Name] = promptEntry{serverName: serverName, prompt: prompt, client: client}
		}
		for _, descriptor := range descriptors {
			descriptor := descriptor
			wrappedName := "mcp__" + SanitizeToolSegment(serverName) + "__" + SanitizeToolSegment(descriptor.Name)
			description := strings.TrimSpace(descriptor.Description)
			if description == "" {
				description = "Call MCP tool " + descriptor.Name + " from server " + serverName + "."
			}
			notice := sandbox.SnapshotNotice
			if serverConfig.URL != "" {
				notice = "Remote MCP call may change external state; review its operation approval."
			}
			definitions = append(definitions, tools.Definition{
				Name:        wrappedName,
				Description: description + " " + notice,
				InputSchema: normalizeInputSchema(descriptor.InputSchema),
				Run: func(ctx context.Context, raw json.RawMessage, toolCtx tools.Context) tools.Result {
					if err := authorizeClientCall(ctx, toolCtx, client, serverName, descriptor.Name, raw); err != nil {
						return tools.Error(err.Error())
					}
					var input any = map[string]any{}
					if len(raw) > 0 {
						_ = json.Unmarshal(raw, &input)
					}
					return client.callTool(ctx, descriptor.Name, input)
				},
			})
		}

		summaries = append(summaries, tools.MCPServerSummary{
			Name:          serverName,
			Command:       serverTarget,
			Status:        "connected",
			ToolCount:     len(descriptors),
			Protocol:      client.protocolName(),
			ResourceCount: len(resources),
			PromptCount:   len(prompts),
		})
	}

	definitions = append(definitions, resourceTools(resourceIndex)...)
	definitions = append(definitions, promptTools(promptIndex)...)

	return Result{
		Tools:   definitions,
		Servers: summaries,
		Dispose: func(context.Context) error {
			var firstErr error
			for _, client := range clients {
				if err := client.close(); err != nil && firstErr == nil {
					firstErr = err
				}
			}
			return firstErr
		},
	}
}

func (c *stdioClient) protocolName() string { return c.protocol }
func (c *stdioClient) supports(kind string) bool {
	if c.capabilities == nil {
		return true
	} // legacy servers may omit capabilities
	_, ok := c.capabilities[kind]
	return ok
}
func (c *stdioClient) health() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readErr
}

func (c *stdioClient) start(ctx context.Context) error {
	c.capabilities = nil
	if strings.TrimSpace(c.config.Command) == "" {
		return fmt.Errorf("MCP server %q has no command configured", c.serverName)
	}
	var lastErr error
	for _, protocol := range protocolCandidates(c.config.Protocol) {
		if err := c.spawn(ctx, protocol); err != nil {
			lastErr = err
			_ = c.close()
			continue
		}
		requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		initialized, err := c.request(requestCtx, "initialize", map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": brand.AgentName, "version": brand.Version},
		})
		cancel()
		if err == nil {
			if result, ok := initialized.(map[string]any); ok {
				if capabilities, ok := result["capabilities"].(map[string]any); ok {
					c.capabilities = capabilities
				}
			}
			_ = c.notify("notifications/initialized", map[string]any{})
			return nil
		}
		lastErr = err
		_ = c.close()
	}
	return lastErr
}

func (c *stdioClient) spawn(ctx context.Context, protocol string) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	commandCWD := c.cwd
	if c.config.CWD != "" {
		commandCWD = filepath.Join(c.cwd, c.config.CWD)
	}
	env := map[string]string{}
	for key, value := range c.config.Env {
		env[key] = fmt.Sprint(value)
	}
	prepare := c.prepare
	if prepare == nil {
		prepare = sandbox.Prepare
	}
	lifeCtx, cancel := context.WithCancel(ctx)
	cmd, cleanup, err := prepare(lifeCtx, sandbox.Options{Workspace: c.cwd, CWD: commandCWD, Command: c.config.Command, Args: c.config.Args, Env: env})
	if err != nil {
		cancel()
		return err
	}
	c.cancel, c.cleanup, c.cmd = cancel, cleanup, cmd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	c.stdin = stdin
	c.reader = bufio.NewReader(stdout)
	c.protocol = protocol
	c.mu.Lock()
	c.nextID = 1
	c.pending = map[int]chan rpcResponse{}
	c.readErr = nil
	c.generation++
	generation := c.generation
	c.mu.Unlock()
	c.stderrMu.Lock()
	c.stderrLines = nil
	c.stderrMu.Unlock()
	go c.captureStderr(stderr)
	go c.readLoop(c.reader, protocol, generation)
	return nil
}

func (c *stdioClient) listTools(ctx context.Context) ([]toolDescriptor, error) {
	c.toolsChanged.Store(false)
	var out []toolDescriptor
	err := c.listPages(ctx, "tools/list", "tools", func(raw json.RawMessage) error {
		var page []toolDescriptor
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, item := range page {
			if item.Name == "" {
				return fmt.Errorf("MCP returned an unnamed tool")
			}
			if err := validateRemoteSchema(item.InputSchema, 0); err != nil {
				return err
			}
		}
		out = append(out, page...)
		if len(out) > 512 {
			return fmt.Errorf("MCP tool limit exceeded")
		}
		return nil
	})
	if err != nil {
		c.toolsChanged.Store(true)
	}
	return out, err
}

func (c *stdioClient) listResources(ctx context.Context) ([]resourceDescriptor, error) {
	c.resourcesChanged.Store(false)
	var out []resourceDescriptor
	err := c.listPages(ctx, "resources/list", "resources", func(raw json.RawMessage) error {
		var page []resourceDescriptor
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, item := range page {
			if item.URI == "" {
				return fmt.Errorf("MCP returned an invalid resource")
			}
		}
		out = append(out, page...)
		if len(out) > 512 {
			return fmt.Errorf("MCP resource limit exceeded")
		}
		return nil
	})
	if err != nil {
		c.resourcesChanged.Store(true)
	}
	return out, err
}

func (c *stdioClient) listPrompts(ctx context.Context) ([]promptDescriptor, error) {
	c.promptsChanged.Store(false)
	var out []promptDescriptor
	err := c.listPages(ctx, "prompts/list", "prompts", func(raw json.RawMessage) error {
		var page []promptDescriptor
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, item := range page {
			if item.Name == "" {
				return fmt.Errorf("MCP returned an invalid prompt")
			}
		}
		out = append(out, page...)
		if len(out) > 512 {
			return fmt.Errorf("MCP prompt limit exceeded")
		}
		return nil
	})
	if err != nil {
		c.promptsChanged.Store(true)
	}
	return out, err
}

func (c *stdioClient) listPages(ctx context.Context, method, field string, appendPage func(json.RawMessage) error) error {
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 32; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result map[string]json.RawMessage
		if err := c.requestInto(ctx, method, params, &result); err != nil {
			return err
		}
		if err := appendPage(result[field]); err != nil {
			return err
		}
		cursor = ""
		if len(result["nextCursor"]) > 0 {
			if err := json.Unmarshal(result["nextCursor"], &cursor); err != nil {
				return err
			}
		}
		if cursor == "" {
			return nil
		}
		if seen[cursor] {
			return fmt.Errorf("MCP repeated a pagination cursor")
		}
		seen[cursor] = true
	}
	return fmt.Errorf("MCP pagination limit exceeded")
}

func (c *stdioClient) callTool(ctx context.Context, name string, input any) tools.Result {
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
			return tools.Error("MCP tool was removed; restart to refresh available tools")
		}
	}
	result, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": input})
	if err != nil {
		return tools.Error(err.Error())
	}
	return formatToolCallResult(result)
}

func (c *stdioClient) readResource(ctx context.Context, uri string) tools.Result {
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
			return tools.Error("MCP resource was removed; restart to refresh available resources")
		}
	}
	result, err := c.request(ctx, "resources/read", map[string]any{"uri": uri})
	if err != nil {
		return tools.Error(err.Error())
	}
	return formatReadResourceResult(result)
}

func (c *stdioClient) getPrompt(ctx context.Context, name string, args map[string]string) tools.Result {
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
			return tools.Error("MCP prompt was removed; restart to refresh available prompts")
		}
	}
	result, err := c.request(ctx, "prompts/get", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return tools.Error(err.Error())
	}
	return formatPromptResult(result)
}

func (c *stdioClient) requestInto(ctx context.Context, method string, params any, target any) error {
	result, err := c.request(ctx, method, params)
	if err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (c *stdioClient) request(ctx context.Context, method string, params any) (any, error) {
	timeout := 30 * time.Second
	if strings.HasSuffix(method, "/list") {
		timeout = 3 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ctx = requestCtx
	c.mu.Lock()
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return nil, err
	}
	id := c.nextID
	c.nextID++
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.sendContext(ctx, jsonRPCMessage{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		cancelCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_ = c.sendContext(cancelCtx, jsonRPCMessage{JSONRPC: "2.0", Method: "notifications/cancelled", Params: map[string]any{"requestId": id, "reason": "request cancelled"}})
		cancel()
		// The server may ignore cancellation notifications. Stop its isolated
		// process so a timed-out tool cannot keep executing after this returns.
		_ = c.close()
		return nil, fmt.Errorf("MCP %s: request timed out or cancelled for %s: %w", c.serverName, method, ctx.Err())
	case result := <-ch:
		if result.err != nil {
			return nil, result.err
		}
		if result.msg.Error != nil {
			return nil, fmt.Errorf("MCP %s: %s", c.serverName, result.msg.Error.Message)
		}
		var decoded any
		if len(result.msg.Result) > 0 {
			if err := json.Unmarshal(result.msg.Result, &decoded); err != nil {
				return nil, err
			}
		}
		return decoded, nil
	}
}

func (c *stdioClient) notify(method string, params any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return c.sendContext(ctx, jsonRPCMessage{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *stdioClient) sendContext(ctx context.Context, msg jsonRPCMessage) error {
	c.lifecycleMu.Lock()
	stdin, protocol := c.stdin, c.protocol
	c.lifecycleMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- c.send(stdin, protocol, msg) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// close interrupts writers and pending readers and runs sandbox cleanup.
		_ = c.close()
		return ctx.Err()
	}
}

func (c *stdioClient) send(stdin io.WriteCloser, protocol string, msg jsonRPCMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(body) > maxMessageBytes {
		return fmt.Errorf("MCP request exceeds message size limit")
	}
	if stdin == nil {
		return fmt.Errorf("MCP stdin is closed")
	}
	if protocol == "newline-json" {
		_, err = fmt.Fprintln(stdin, string(body))
		return err
	}
	_, err = fmt.Fprintf(stdin, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

func readMessage(reader *bufio.Reader, protocol string) (jsonRPCMessage, error) {
	var data []byte
	var err error
	if protocol == "newline-json" {
		data, err = readBoundedLine(reader, maxMessageBytes)
	} else {
		data, err = readContentLengthMessage(reader)
	}
	if err != nil {
		return jsonRPCMessage{}, err
	}
	var msg jsonRPCMessage
	err = json.Unmarshal(bytes.TrimSpace(data), &msg)
	return msg, err
}

func (c *stdioClient) readLoop(reader *bufio.Reader, protocol string, generation int) {
	for {
		msg, err := readMessage(reader, protocol)
		if err != nil {
			c.failGeneration(generation, err)
			return
		}
		id, ok := messageID(msg.ID)
		if !ok {
			switch msg.Method {
			case "notifications/tools/list_changed":
				c.toolsChanged.Store(true)
			case "notifications/resources/list_changed":
				c.resourcesChanged.Store(true)
			case "notifications/prompts/list_changed":
				c.promptsChanged.Store(true)
			}
			continue
		}
		c.mu.Lock()
		if c.generation != generation {
			c.mu.Unlock()
			return
		}
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- rpcResponse{msg: msg}
		}
	}
}

func (c *stdioClient) failPending(err error) {
	c.failGeneration(0, err)
}

func (c *stdioClient) failGeneration(generation int, err error) {
	c.mu.Lock()
	if generation != 0 && generation != c.generation {
		c.mu.Unlock()
		return
	}
	c.readErr = err
	pending := c.pending
	c.pending = map[int]chan rpcResponse{}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- rpcResponse{err: err}
	}
}

func (c *stdioClient) captureStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		c.stderrMu.Lock()
		c.stderrLines = append(c.stderrLines, line)
		if len(c.stderrLines) > 8 {
			c.stderrLines = c.stderrLines[len(c.stderrLines)-8:]
		}
		c.stderrMu.Unlock()
	}
	// Scanner stops on an oversized line. Continue draining rather than leave
	// the server blocked on a full stderr pipe while it owes an RPC response.
	if scanner.Err() != nil {
		_, _ = io.Copy(io.Discard, stderr)
	}
}

func (c *stdioClient) stderrSuffix() string {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	if len(c.stderrLines) == 0 {
		return ""
	}
	return "\n" + strings.Join(c.stderrLines, "\n")
}

func (c *stdioClient) close() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
		c.stdin = nil
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	if c.cleanup != nil {
		c.cleanup()
		c.cleanup = nil
	}
	c.cmd = nil
	c.failPending(fmt.Errorf("MCP server %q is not running", c.serverName))
	return nil
}

func readContentLengthMessage(reader io.Reader) ([]byte, error) {
	header := []byte{}
	buf := make([]byte, 1)
	for !bytes.Contains(header, []byte("\r\n\r\n")) {
		if len(header) >= maxHeaderBytes {
			return nil, fmt.Errorf("MCP header exceeds size limit")
		}
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		header = append(header, buf[0])
	}
	parts := bytes.SplitN(header, []byte("\r\n\r\n"), 2)
	contentLength := 0
	for _, line := range strings.Split(string(parts[0]), "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			contentLength, _ = strconv.Atoi(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
			break
		}
	}
	if contentLength <= 0 || contentLength > maxMessageBytes {
		return nil, fmt.Errorf("missing or invalid Content-Length header (limit %d)", maxMessageBytes)
	}
	body := make([]byte, contentLength)
	_, err := io.ReadFull(reader, body)
	return body, err
}

type resourceEntry struct {
	serverName string
	resource   resourceDescriptor
	client     mcpClient
}

type promptEntry struct {
	serverName string
	prompt     promptDescriptor
	client     mcpClient
}

func resourceTools(index map[string]resourceEntry) []tools.Definition {
	if len(index) == 0 {
		return nil
	}
	return []tools.Definition{
		{
			Name:        "list_mcp_resources",
			Description: "List available MCP resources exposed by connected MCP servers.",
			InputSchema: objectSchema(map[string]any{"server": map[string]any{"type": "string"}}, nil),
			Run: func(_ context.Context, raw json.RawMessage, _ tools.Context) tools.Result {
				var input struct {
					Server string `json:"server"`
				}
				_ = json.Unmarshal(raw, &input)
				lines := []string{}
				for _, entry := range index {
					if input.Server != "" && input.Server != entry.serverName {
						continue
					}
					line := entry.serverName + ": " + entry.resource.URI
					if entry.resource.Name != "" {
						line += " (" + entry.resource.Name + ")"
					}
					if entry.resource.Description != "" {
						line += " - " + entry.resource.Description
					}
					lines = append(lines, line)
				}
				if len(lines) == 0 {
					return tools.Success("No MCP resources available.")
				}
				return tools.Success(strings.Join(lines, "\n"))
			},
		},
		{
			Name:        "read_mcp_resource",
			Description: "Read a resource exposed by a connected MCP server.",
			InputSchema: objectSchema(map[string]any{"server": map[string]any{"type": "string"}, "uri": map[string]any{"type": "string"}}, []string{"uri"}),
			Run: func(ctx context.Context, raw json.RawMessage, toolCtx tools.Context) tools.Result {
				var input struct {
					Server string `json:"server"`
					URI    string `json:"uri"`
				}
				if err := json.Unmarshal(raw, &input); err != nil {
					return tools.Error(err.Error())
				}
				entry, ok := findResource(index, input.Server, input.URI)
				if !ok {
					return tools.Error("Unknown MCP resource: " + input.URI)
				}
				if err := authorizeClientCall(ctx, toolCtx, entry.client, entry.serverName, "resources/read", raw); err != nil {
					return tools.Error(err.Error())
				}
				return entry.client.readResource(ctx, entry.resource.URI)
			},
		},
	}
}

func promptTools(index map[string]promptEntry) []tools.Definition {
	if len(index) == 0 {
		return nil
	}
	return []tools.Definition{
		{
			Name:        "list_mcp_prompts",
			Description: "List available MCP prompts exposed by connected MCP servers.",
			InputSchema: objectSchema(map[string]any{"server": map[string]any{"type": "string"}}, nil),
			Run: func(_ context.Context, raw json.RawMessage, _ tools.Context) tools.Result {
				var input struct {
					Server string `json:"server"`
				}
				_ = json.Unmarshal(raw, &input)
				lines := []string{}
				for _, entry := range index {
					if input.Server != "" && input.Server != entry.serverName {
						continue
					}
					line := entry.serverName + ": " + entry.prompt.Name
					if entry.prompt.Description != "" {
						line += " - " + entry.prompt.Description
					}
					lines = append(lines, line)
				}
				if len(lines) == 0 {
					return tools.Success("No MCP prompts available.")
				}
				return tools.Success(strings.Join(lines, "\n"))
			},
		},
		{
			Name:        "get_mcp_prompt",
			Description: "Get a prompt exposed by a connected MCP server.",
			InputSchema: objectSchema(map[string]any{"server": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}}, []string{"name"}),
			Run: func(ctx context.Context, raw json.RawMessage, toolCtx tools.Context) tools.Result {
				var input struct {
					Server    string            `json:"server"`
					Name      string            `json:"name"`
					Arguments map[string]string `json:"arguments"`
				}
				if err := json.Unmarshal(raw, &input); err != nil {
					return tools.Error(err.Error())
				}
				entry, ok := findPrompt(index, input.Server, input.Name)
				if !ok {
					return tools.Error("Unknown MCP prompt: " + input.Name)
				}
				if err := authorizeClientCall(ctx, toolCtx, entry.client, entry.serverName, "prompts/get", raw); err != nil {
					return tools.Error(err.Error())
				}
				return entry.client.getPrompt(ctx, entry.prompt.Name, input.Arguments)
			},
		},
	}
}

func findResource(index map[string]resourceEntry, server, uri string) (resourceEntry, bool) {
	if server != "" {
		entry, ok := index[server+":"+uri]
		return entry, ok
	}
	for _, entry := range index {
		if entry.resource.URI == uri {
			return entry, true
		}
	}
	return resourceEntry{}, false
}

func findPrompt(index map[string]promptEntry, server, name string) (promptEntry, bool) {
	if server != "" {
		entry, ok := index[server+":"+name]
		return entry, ok
	}
	for _, entry := range index {
		if entry.prompt.Name == name {
			return entry, true
		}
	}
	return promptEntry{}, false
}

func formatToolCallResult(result any) tools.Result {
	data, _ := json.Marshal(result)
	var parsed struct {
		Content           []map[string]any `json:"content"`
		StructuredContent any              `json:"structuredContent"`
		IsError           bool             `json:"isError"`
	}
	_ = json.Unmarshal(data, &parsed)
	parts := []string{}
	for _, block := range parsed.Content {
		if block["type"] == "text" {
			parts = append(parts, fmt.Sprint(block["text"]))
		} else {
			parts = append(parts, mustPretty(block))
		}
	}
	if parsed.StructuredContent != nil {
		parts = append(parts, "STRUCTURED_CONTENT:\n"+mustPretty(parsed.StructuredContent))
	}
	if len(parts) == 0 {
		parts = append(parts, mustPretty(result))
	}
	return tools.Result{OK: !parsed.IsError, Output: strings.TrimSpace(strings.Join(parts, "\n\n"))}
}

func formatReadResourceResult(result any) tools.Result {
	data, _ := json.Marshal(result)
	var parsed struct {
		Contents []struct {
			URI      string `json:"uri"`
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
			Blob     string `json:"blob"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return tools.Error(err.Error())
	}
	if len(parsed.Contents) == 0 {
		return tools.Success("No resource contents returned.")
	}
	parts := []string{}
	for _, item := range parsed.Contents {
		header := "URI: " + defaultString(item.URI, "(unknown)")
		if item.MimeType != "" {
			header += "\nMIME: " + item.MimeType
		}
		body := item.Text
		if body == "" && item.Blob != "" {
			body = "BLOB:\n" + item.Blob
		}
		parts = append(parts, header+"\n\n"+body)
	}
	return tools.Success(strings.Join(parts, "\n\n"))
}

func formatPromptResult(result any) tools.Result {
	data, _ := json.Marshal(result)
	var parsed struct {
		Description string `json:"description"`
		Messages    []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return tools.Error(err.Error())
	}
	parts := []string{}
	if parsed.Description != "" {
		parts = append(parts, "DESCRIPTION: "+parsed.Description)
	}
	for _, msg := range parsed.Messages {
		role := defaultString(msg.Role, "unknown")
		content := fmt.Sprint(msg.Content)
		if text, ok := msg.Content.(string); ok {
			content = text
		} else {
			content = mustPretty(msg.Content)
		}
		parts = append(parts, "["+role+"]\n"+content)
	}
	if len(parts) == 0 {
		return tools.Success(mustPretty(result))
	}
	return tools.Success(strings.Join(parts, "\n\n"))
}

func normalizeInputSchema(schema map[string]any) map[string]any {
	if schema != nil {
		return schema
	}
	return map[string]any{"type": "object", "additionalProperties": true}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties}
	if required != nil {
		schema["required"] = required
	}
	return schema
}

func protocolCandidates(protocol string) []string {
	switch protocol {
	case "content-length":
		return []string{"content-length"}
	case "newline-json":
		return []string{"newline-json"}
	default:
		return []string{"content-length", "newline-json"}
	}
}

func configuredProtocol(protocol string) string {
	if protocol == "" || protocol == "auto" {
		return ""
	}
	return protocol
}

func sameID(got any, want int) bool {
	switch typed := got.(type) {
	case float64:
		return int(typed) == want
	case int:
		return typed == want
	case json.Number:
		value, _ := typed.Int64()
		return int(value) == want
	default:
		return fmt.Sprint(got) == strconv.Itoa(want)
	}
}

func messageID(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	default:
		parsed, err := strconv.Atoi(fmt.Sprint(value))
		return parsed, err == nil
	}
}

func SanitizeToolSegment(value string) string {
	lower := strings.ToLower(value)
	re := regexp.MustCompile(`[^a-z0-9_-]+`)
	out := strings.Trim(re.ReplaceAllString(lower, "_"), "_")
	if out == "" {
		return "tool"
	}
	return out
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func mustPretty(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

func authorizeCall(ctx context.Context, toolCtx tools.Context, server, method string, raw json.RawMessage) error {
	if toolCtx.Permission == nil {
		return fmt.Errorf("MCP call requires an active permission manager")
	}
	return toolCtx.Permission.EnsureCommand(ctx, "mcp:"+server+":"+method, []string{safety.Redact(ctx, string(raw))}, toolCtx.CWD)
}

func authorizeClientCall(ctx context.Context, toolCtx tools.Context, client mcpClient, server, method string, raw json.RawMessage) error {
	if remote, ok := client.(*remoteClient); ok {
		if toolCtx.Permission == nil {
			return fmt.Errorf("remote MCP call requires an active permission manager")
		}
		approver, ok := toolCtx.Permission.(interface {
			EnsureRemoteMCP(context.Context, string, string, string, string) error
		})
		if !ok {
			return fmt.Errorf("remote MCP call requires operation approval support")
		}
		origin, _, err := egress.Origin(remote.config.URL)
		if err != nil {
			return err
		}
		return approver.EnsureRemoteMCP(ctx, server, method, origin, string(raw))
	}
	return authorizeCall(ctx, toolCtx, server, method, raw)
}

func readBoundedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var data []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(data)+len(part) > limit {
			return nil, fmt.Errorf("MCP line exceeds message size limit")
		}
		data = append(data, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return data, err
	}
}
