package session

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/agent"
	"github.com/BigSmartie/Coding-Agent/internal/brand"
	"github.com/BigSmartie/Coding-Agent/internal/commands"
	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/model"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

type Args struct {
	CWD        string
	Tools      *tools.Registry
	Model      message.Model
	Runtime    *config.Runtime
	Messages   []message.Message
	Permission tools.PermissionManager
	History    History
	Store      Store
	SessionID  string
	Journal    *Journal
	In         io.Reader
	Out        io.Writer
}

type Session struct {
	args      Args
	ownsInput bool
}

func New(args Args) *Session {
	if args.Store.Context == nil {
		args.Store.Context = context.Background()
	}
	if args.Runtime != nil {
		args.Store.Context = safety.WithSecrets(args.Store.Context, args.Runtime.APIKey, args.Runtime.AuthToken)
	}
	args.History.Context = args.Store.Context
	if args.In == nil {
		args.In = os.Stdin
	}
	if args.Out == nil {
		args.Out = os.Stdout
	}
	stdin, isFile := args.In.(*os.File)
	return &Session{args: args, ownsInput: isFile && stdin == os.Stdin}
}

func (s *Session) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.args.In)
	historyEntries, _ := s.args.History.Load()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprint(s.args.Out, brand.CommandName+"> ")
		// Request one line at a time so this reader does not compete with an
		// approval prompt while the agent is running. A caller-provided blocking
		// reader remains its owner's responsibility to close on cancellation.
		type scanResult struct {
			text string
			ok   bool
			err  error
		}
		read := make(chan scanResult, 1)
		go func() { ok := scanner.Scan(); read <- scanResult{text: scanner.Text(), ok: ok, err: scanner.Err()} }()
		var line scanResult
		select {
		case <-ctx.Done():
			if s.ownsInput {
				_ = os.Stdin.Close()
			}
			return ctx.Err()
		case line = <-read:
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !line.ok {
			return line.err
		}
		input := strings.TrimSpace(line.text)
		if input == "/exit" {
			return nil
		}
		if input != "" && (len(historyEntries) == 0 || historyEntries[len(historyEntries)-1] != input) {
			historyEntries = append(historyEntries, input)
			_ = s.args.History.Save(historyEntries)
		}
		if err := s.RunOnce(ctx, input); err != nil {
			fmt.Fprintln(s.args.Out, safety.EscapeTerminal(safety.Redact(ctx, err.Error())))
		}
	}
}

func (s *Session) RunOnce(ctx context.Context, input string) error {
	previousOut := s.args.Out
	s.args.Out = plainWriter{out: previousOut, ctx: s.args.Store.Context}
	defer func() { s.args.Out = previousOut }()
	if strings.TrimSpace(input) == "" {
		return nil
	}
	if input == "/help" || input == "/" {
		fmt.Fprintln(s.args.Out, commands.FormatHelp())
		return nil
	}
	if input == "/tools" {
		for _, tool := range s.args.Tools.List() {
			fmt.Fprintf(s.args.Out, "%s: %s\n", tool.Name, tool.Description)
		}
		return nil
	}
	if input == "/config-paths" {
		fmt.Fprintln(s.args.Out, brand.AgentName+" settings: "+config.SettingsPath())
		fmt.Fprintln(s.args.Out, brand.AgentName+" permissions: "+config.PermissionsPath())
		fmt.Fprintln(s.args.Out, brand.AgentName+" mcp: "+config.MCPPath())
		fmt.Fprintln(s.args.Out, "compat fallback: "+config.ClaudeSettingsPath())
		return nil
	}
	if input == "/permissions" {
		fmt.Fprintln(s.args.Out, "permission store: "+config.PermissionsPath())
		return nil
	}
	if input == "/status" {
		if s.args.Runtime == nil {
			fmt.Fprintln(s.args.Out, "model: not-configured")
			return nil
		}
		fmt.Fprintln(s.args.Out, "provider: "+s.args.Runtime.Provider)
		fmt.Fprintln(s.args.Out, "model: "+s.args.Runtime.Model)
		fmt.Fprintln(s.args.Out, "baseUrl: "+s.args.Runtime.BaseURL)
		capabilities := model.CapabilitiesFor(*s.args.Runtime)
		if capabilities.ContextWindowTokens > 0 {
			fmt.Fprintf(s.args.Out, "context window: %d tokens (%s)\n", capabilities.ContextWindowTokens, capabilities.ContextWindowSource)
		} else {
			fmt.Fprintln(s.args.Out, "context window: unknown; set contextWindowTokens in user settings")
		}
		fmt.Fprintln(s.args.Out, "wire API: "+capabilities.WireAPI)
		auth := "API_KEY"
		if s.args.Runtime.AuthToken != "" {
			auth = "AUTH_TOKEN"
		}
		fmt.Fprintln(s.args.Out, "auth: "+auth)
		fmt.Fprintf(s.args.Out, "mcp servers: %d\n", len(s.args.Runtime.MCPServers))
		fmt.Fprintln(s.args.Out, s.args.Runtime.SourceSummary)
		return nil
	}
	if input == "/model" {
		if s.args.Runtime == nil {
			fmt.Fprintln(s.args.Out, "current model: not-configured")
			return nil
		}
		fmt.Fprintln(s.args.Out, "current model: "+s.args.Runtime.Model)
		return nil
	}
	if strings.HasPrefix(input, "/model ") {
		modelName := strings.TrimSpace(strings.TrimPrefix(input, "/model "))
		if modelName == "" {
			fmt.Fprintln(s.args.Out, "usage: /model <model-name>")
			return nil
		}
		if err := config.SaveSettings(config.Settings{Model: modelName}); err != nil {
			return err
		}
		fmt.Fprintln(s.args.Out, "saved model="+modelName+" to "+config.SettingsPath())
		return nil
	}
	if input == "/skills" {
		skills := s.args.Tools.Skills()
		if len(skills) == 0 {
			fmt.Fprintln(s.args.Out, "No skills discovered. Add skills under ~/"+brand.ConfigDirName+"/skills/<name>/SKILL.md, "+brand.ConfigDirName+"/skills/<name>/SKILL.md, .claude/skills/<name>/SKILL.md, or ~/.claude/skills/<name>/SKILL.md.")
			return nil
		}
		for _, skill := range skills {
			fmt.Fprintf(s.args.Out, "%s  %s  [%s]\n", skill.Name, skill.Description, skill.Source)
		}
		return nil
	}
	if input == "/mcp" {
		servers := s.args.Tools.MCPServers()
		if len(servers) == 0 {
			fmt.Fprintln(s.args.Out, "No MCP servers configured. Add mcpServers to ~/"+brand.ConfigDirName+"/settings.json, ~/"+brand.ConfigDirName+"/mcp.json, or project .mcp.json.")
			return nil
		}
		for _, server := range servers {
			line := fmt.Sprintf("%s  status=%s  tools=%d", server.Name, server.Status, server.ToolCount)
			if server.ResourceCount > 0 {
				line += fmt.Sprintf("  resources=%d", server.ResourceCount)
			}
			if server.PromptCount > 0 {
				line += fmt.Sprintf("  prompts=%d", server.PromptCount)
			}
			if server.Protocol != "" {
				line += "  protocol=" + server.Protocol
			}
			if server.Error != "" {
				line += "  error=" + server.Error
			}
			fmt.Fprintln(s.args.Out, line)
		}
		return nil
	}
	if call, ok := commands.ParseShortcut(input); ok {
		turnID, err := s.beginTurn()
		if err != nil {
			return err
		}
		if lifecycle, ok := s.args.Permission.(interface {
			BeginTurn()
			EndTurn()
		}); ok {
			lifecycle.BeginTurn()
			defer lifecycle.EndTurn()
		}
		if err := s.journalEvent(turnID, agent.Event{Kind: string(EventToolStarted), ToolName: call.ToolName}); err != nil {
			return err
		}
		result := s.args.Tools.Execute(ctx, call.ToolName, call.Input, tools.Context{CWD: s.args.CWD, Permission: s.args.Permission})
		if err := s.journalEvent(turnID, agent.Event{Kind: string(EventToolCompleted), ToolName: call.ToolName}); err != nil {
			return err
		}
		if s.args.Journal != nil {
			next := append(append([]message.Message(nil), s.args.Messages...), message.UserMessage(input), message.AssistantMessage(result.Output))
			if err := s.finishTurn(turnID, next, nil); err != nil {
				return err
			}
			s.args.Messages = next
		}
		if result.OK {
			fmt.Fprintln(s.args.Out, result.Output)
		} else {
			fmt.Fprintln(s.args.Out, "ERROR: "+result.Output)
		}
		return nil
	}
	if strings.HasPrefix(input, "/") {
		matches := commands.FindMatching(input)
		if len(matches) > 0 {
			fmt.Fprintln(s.args.Out, "Unknown command. Did you mean:")
			fmt.Fprintln(s.args.Out, strings.Join(matches, "\n"))
		} else {
			fmt.Fprintln(s.args.Out, "Unknown command. Type /help to see available commands.")
		}
		return nil
	}

	turnID, err := s.beginTurn()
	if err != nil {
		return err
	}
	messages := append(s.args.Messages, message.UserMessage(input))
	next, err := agent.RunTurn(ctx, agent.Args{
		Model:      s.args.Model,
		Tools:      s.args.Tools,
		Messages:   messages,
		CWD:        s.args.CWD,
		Permission: s.args.Permission,
		OnEvent:    func(event agent.Event) error { return s.journalEvent(turnID, event) },
		OnProgressMessage: func(content string) {
			fmt.Fprintln(s.args.Out, "progress: "+content)
		},
		OnToolStart: func(name string, _ any) {
			fmt.Fprintln(s.args.Out, "tool: "+name)
		},
	})
	if err != nil {
		content := "request failed: " + err.Error()
		s.args.Messages = append(next, message.AssistantMessage(content))
		if persistErr := s.finishTurn(turnID, s.args.Messages, err); persistErr != nil {
			return persistErr
		}
		fmt.Fprintln(s.args.Out, content)
		return nil
	}
	s.args.Messages = next
	if err := s.finishTurn(turnID, next, nil); err != nil {
		return err
	}
	for i := len(next) - 1; i >= 0; i-- {
		if next[i].Role == message.RoleAssistant {
			fmt.Fprintln(s.args.Out, next[i].Content)
			break
		}
	}
	return nil
}

type plainWriter struct {
	out io.Writer
	ctx context.Context
}

func (w plainWriter) Write(data []byte) (int, error) {
	_, err := io.WriteString(w.out, safety.EscapeTerminal(safety.Redact(w.ctx, string(data))))
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

func (s *Session) persistMessages() error {
	if s.args.Store.Dir == "" || s.args.SessionID == "" {
		return nil
	}
	return s.args.Store.Save(Record{
		ID:       s.args.SessionID,
		CWD:      s.args.CWD,
		Messages: s.args.Messages,
	})
}

func (s *Session) beginTurn() (string, error) {
	if s.args.Journal == nil {
		return "", nil
	}
	id := rand.Text()
	_, err := s.args.Journal.Append(Event{Kind: EventTurnStarted, TurnID: id})
	return id, err
}

func (s *Session) journalEvent(turnID string, event agent.Event) error {
	if s.args.Journal == nil {
		return nil
	}
	_, err := s.args.Journal.Append(Event{Kind: EventKind(event.Kind), TurnID: turnID, CallID: event.CallID, ToolName: event.ToolName})
	return err
}

func (s *Session) finishTurn(turnID string, messages []message.Message, turnErr error) error {
	if s.args.Store.Dir == "" || s.args.SessionID == "" {
		return nil
	}
	if s.args.Journal == nil {
		return s.args.Store.Save(Record{ID: s.args.SessionID, CWD: s.args.CWD, Messages: messages})
	}
	kind := EventTurnCompleted
	if turnErr != nil {
		kind = EventTurnFailed
	}
	if _, err := s.args.Journal.Append(Event{Kind: kind, TurnID: turnID}); err != nil {
		return err
	}
	record, err := s.args.Journal.AppendCheckpoint(Record{ID: s.args.SessionID, CWD: s.args.CWD, Messages: messages}, turnID)
	if err != nil {
		return err
	}
	if err := s.args.Store.Save(record); err != nil {
		return err
	}
	return s.args.Journal.CompactIfNeeded()
}
