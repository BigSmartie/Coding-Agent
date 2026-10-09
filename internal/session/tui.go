package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BigSmartie/Coding-Agent/internal/agent"
	"github.com/BigSmartie/Coding-Agent/internal/brand"
	"github.com/BigSmartie/Coding-Agent/internal/commands"
	"github.com/BigSmartie/Coding-Agent/internal/cost"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/permissions"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"github.com/BigSmartie/Coding-Agent/internal/terminal"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
	"github.com/BigSmartie/Coding-Agent/internal/tui"
)

type transcriptEntry struct {
	kind           string
	body           string
	toolName       string
	status         string
	path           string
	editOperation  bool
	aggregateCount int
}

type tuiState struct {
	input           string
	cursor          int
	transcript      []transcriptEntry
	scroll          int
	status          string
	selected        int
	nextEntryID     int
	history         []string
	historyIndex    int
	historyDraft    string
	pendingApproval *approvalState
	tokens          message.TokenUsage
	estimatedCost   float64
	hasCost         bool
	costUnknown     bool
	busy            bool
	spinnerFrame    int
	streaming       *streamingReply
	pendingTools    map[string][]int
	cancelTurn      context.CancelFunc
}

type approvalState struct {
	request       permissions.Request
	selected      int
	feedbackMode  bool
	feedbackInput string
	expanded      bool
	scroll        int
	resultCh      chan permissions.PromptResult
}

type streamingReply struct {
	entryIndex int
}

type tuiAgentEvent struct {
	kind      string
	content   string
	toolName  string
	toolInput any
	isError   bool
	usage     message.TokenUsage
	request   permissions.Request
	resultCh  chan permissions.PromptResult
	next      []message.Message
	err       error
}

func (s *Session) RunTUI(ctx context.Context) error {
	defer s.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	historyEntries, _ := s.args.History.Load()
	state := tuiState{
		cursor:       0,
		nextEntryID:  1,
		history:      historyEntries,
		historyIndex: len(historyEntries),
		status:       "Ready",
	}
	rawState, err := terminal.MakeRaw(os.Stdin)
	if err != nil {
		return s.Run(ctx)
	}
	defer rawState.Restore()
	fmt.Fprint(s.args.Out, "\x1b[?1049h\x1b[?25l")
	defer fmt.Fprint(s.args.Out, "\x1b[?25h\x1b[?1049l")

	rest := ""
	agentEvents := make(chan tuiAgentEvent, 64)
	inputEvents := make(chan []tui.InputEvent, 8)
	readErrCh := make(chan error, 1)
	ticker := time.NewTicker(45 * time.Millisecond)
	defer ticker.Stop()
	var render func()
	render = func() {
		fmt.Fprint(s.args.Out, "\x1b[H\x1b[2J")
		width, height := terminalSize()
		fmt.Fprint(s.args.Out, s.renderTUIScreen(state, width, height))
	}
	if setter, ok := s.args.Permission.(interface{ SetPrompt(permissions.Prompt) }); ok {
		setter.SetPrompt(func(ctx context.Context, request permissions.Request) (permissions.PromptResult, error) {
			return s.queueApprovalPrompt(ctx, request, agentEvents)
		})
	}
	render()

	buffer := make([]byte, 64)
	go func() {
		for {
			events, nextRest, err := readTUIEvents(s.args.In, rest, buffer)
			if err != nil {
				readErrCh <- err
				return
			}
			rest = nextRest
			select {
			case inputEvents <- events:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErrCh:
			if err == io.EOF {
				return nil
			}
			return err
		case events := <-inputEvents:
			for _, event := range events {
				shouldExit, err := s.handleTUIEvent(ctx, &state, event, agentEvents)
				if err != nil {
					appendAssistantEntry(&state, err.Error())
					state.status = "Error"
					state.busy = false
				}
				if shouldExit {
					return nil
				}
			}
		case event := <-agentEvents:
			s.applyTUIAgentEvent(&state, event)
		case <-ticker.C:
			if state.busy {
				state.spinnerFrame++
				if state.pendingApproval == nil {
					state.status = busyStatusText(state.spinnerFrame)
				}
			}
		}
		render()
	}
}

func (s *Session) handleTUIEvent(ctx context.Context, state *tuiState, event tui.InputEvent, agentEvents chan<- tuiAgentEvent) (bool, error) {
	if event.Kind == tui.EventText && event.Ctrl && event.Text == "c" {
		if state.busy && state.cancelTurn != nil {
			state.cancelTurn()
			state.pendingApproval = nil
			state.status = "Cancelling..."
			return false, nil
		}
		return true, nil
	}
	if state.pendingApproval != nil {
		done, result := state.pendingApproval.handle(event)
		if done && state.pendingApproval.resultCh != nil {
			state.pendingApproval.resultCh <- result
			close(state.pendingApproval.resultCh)
			state.pendingApproval = nil
			if state.busy {
				state.status = busyStatusText(state.spinnerFrame)
			} else {
				state.status = "Ready"
			}
		}
		return false, nil
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyEscape {
		state.input = ""
		state.cursor = 0
		state.selected = 0
		return false, nil
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyBackspace {
		if state.cursor > 0 {
			start := previousRuneStart(state.input, state.cursor)
			state.input = state.input[:start] + state.input[state.cursor:]
			state.cursor = start
		}
		return false, nil
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyLeft {
		if state.cursor > 0 {
			state.cursor = previousRuneStart(state.input, state.cursor)
		}
		return false, nil
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyRight {
		if state.cursor < len(state.input) {
			_, size := utf8.DecodeRuneInString(state.input[state.cursor:])
			if size > 0 {
				state.cursor += size
			}
		}
		return false, nil
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyPageUp {
		state.scroll += 8
		return false, nil
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyPageDown {
		state.scroll -= 8
		if state.scroll < 0 {
			state.scroll = 0
		}
		return false, nil
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyUp {
		visible := s.visibleSlashCommands(state.input)
		if len(visible) > 0 {
			state.selected = (state.selected - 1 + len(visible)) % len(visible)
		} else if historyUp(state) {
			return false, nil
		} else {
			state.scroll++
		}
		return false, nil
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyDown {
		visible := s.visibleSlashCommands(state.input)
		if len(visible) > 0 {
			state.selected = (state.selected + 1) % len(visible)
		} else if historyDown(state) {
			return false, nil
		} else if state.scroll > 0 {
			state.scroll--
		}
		return false, nil
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyTab {
		visible := s.visibleSlashCommands(state.input)
		if len(visible) > 0 {
			selected := visible[min(state.selected, len(visible)-1)]
			state.input = selected.Usage
			state.cursor = len(state.input)
			state.selected = 0
		}
		return false, nil
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyReturn {
		input := strings.TrimSpace(state.input)
		if input == "/exit" {
			return true, nil
		}
		if input != "" && !state.busy {
			state.transcript = append(state.transcript, transcriptEntry{kind: "user", body: input})
			if len(state.history) == 0 || state.history[len(state.history)-1] != input {
				state.history = append(state.history, input)
				_ = s.args.History.Save(state.history)
			}
			state.historyIndex = len(state.history)
			state.historyDraft = ""
			state.input = ""
			state.cursor = 0
			if strings.HasPrefix(input, "/") && !isShortcutInput(input) {
				var err error
				var out strings.Builder
				previousOut := s.args.Out
				s.args.Out = &out
				err = s.RunOnce(ctx, input)
				s.args.Out = previousOut
				if strings.TrimSpace(out.String()) != "" {
					state.transcript = append(state.transcript, transcriptEntry{kind: "assistant", body: strings.TrimSpace(out.String())})
				}
				if err == nil {
					state.status = "Ready"
				}
				state.scroll = 0
				return false, err
			} else {
				state.busy = true
				state.spinnerFrame = 0
				state.streaming = nil
				state.status = busyStatusText(state.spinnerFrame)
				state.pendingTools = map[string][]int{}
				state.cancelTurn = s.startAgentTurn(ctx, input, agentEvents)
			}
			state.scroll = 0
			return false, nil
		}
	}
	if event.Kind == tui.EventText && !event.Ctrl {
		state.input = state.input[:state.cursor] + event.Text + state.input[state.cursor:]
		state.cursor += len(event.Text)
		state.selected = 0
		state.historyIndex = len(state.history)
	}
	return false, nil
}

func (s *Session) runAgentForTUI(ctx context.Context, input string, send func(tuiAgentEvent)) error {
	turnID, err := s.beginTurn()
	if err != nil {
		return err
	}
	messages := append(s.args.Messages, message.UserMessage(input))
	next, err := agent.RunTurn(ctx, agent.Args{
		ContextWindowTokens: s.contextWindowTokens(),
		MaxOutputTokens:     s.maxOutputTokens(),
		CurrentUserPrompt:   input,
		Model:               s.args.Model,
		Tools:               s.args.Tools,
		Messages:            messages,
		CWD:                 s.args.CWD,
		Permission:          s.args.Permission,
		Tasks:               s.tasks,
		Jobs:                s.jobs,
		Network:             s.network,
		OnEvent:             func(event agent.Event) error { return s.journalEvent(turnID, event) },
		OnModelStart:        func() { send(tuiAgentEvent{kind: "model_start"}) },
		OnTextDelta:         func(content string) { send(tuiAgentEvent{kind: "text_delta", content: safety.Redact(ctx, content)}) },
		OnProgressMessage: func(content string) {
			send(tuiAgentEvent{kind: "progress", content: safety.Redact(ctx, content)})
		},
		OnToolStart: func(name string, input any) {
			send(tuiAgentEvent{kind: "tool_start", toolName: name, toolInput: input})
		},
		OnToolResult: func(name, output string, isError bool) {
			send(tuiAgentEvent{kind: "tool_result", toolName: name, content: safety.Redact(ctx, output), isError: isError})
		},
		OnAssistant: func(content string) {
			send(tuiAgentEvent{kind: "assistant", content: safety.Redact(ctx, content)})
		},
		OnUsage: func(usage message.TokenUsage) {
			send(tuiAgentEvent{kind: "usage", usage: usage})
		},
	})
	if persistErr := s.finishTurn(turnID, next, err); persistErr != nil {
		return persistErr
	}
	send(tuiAgentEvent{kind: "done", next: next, err: err})
	return nil
}

func (s *Session) renderTUIScreen(state tuiState, width, height int) string {
	if width < 60 {
		width = 60
	}
	if height < 20 {
		height = 20
	}
	if state.pendingApproval != nil {
		approval := tui.RenderPanel("approval", renderApprovalPrompt(*state.pendingApproval, height-8), tui.PanelOptions{Width: width, MinBodyLines: max(8, height-12)})
		return strings.Join([]string{
			ansiStatusLine(s.currentModelName(), s.args.CWD, "approval required", width),
			"",
			approval,
			"",
			ansiDimLine("Waiting for approval...", width),
		}, "\n")
	}
	entries := make([]tui.TranscriptEntry, 0, len(state.transcript))
	for index, entry := range state.transcript {
		entries = append(entries, tui.TranscriptEntry{
			ID:               index + 1,
			Kind:             mapEntryKind(entry.kind),
			Body:             safety.Redact(s.args.Store.Context, entry.body),
			ToolName:         entry.toolName,
			Status:           mapToolStatus(entry.status),
			Collapsed:        entry.status == "success",
			CollapsedSummary: safety.Redact(s.args.Store.Context, summarizeSuccessfulToolEntry(entry)),
		})
	}
	transcriptHeight := height - 13
	if transcriptHeight < 6 {
		transcriptHeight = 6
	}
	maxScroll := tui.GetTranscriptMaxScrollOffset(entries, transcriptHeight)
	if state.scroll > maxScroll {
		state.scroll = maxScroll
	}
	feed := tui.RenderTranscript(entries, state.scroll, transcriptHeight)
	visible := s.visibleSlashCommands(state.input)
	if len(entries) == 0 {
		return tui.RenderHomeScreen(
			brand.AppName,
			"v"+brand.Version,
			s.args.CWD,
			s.modelStatusWithCost(state),
			s.homeSections(),
			s.homeBanner(),
			state.input,
			state.cursor,
			visible,
			state.selected,
			state.tokens,
			width,
			height,
		)
	}
	return tui.RenderChatScreen(
		brand.AppName,
		s.args.CWD,
		s.modelStatusWithCost(state),
		feed,
		state.input,
		state.cursor,
		visible,
		state.selected,
		state.tokens,
		width,
		height,
		len(entries),
	)
}

func (s *Session) modelStatusWithCost(state tuiState) string {
	status := s.currentModelName() + " | " + state.status
	if state.costUnknown {
		return status + " | cost unavailable"
	}
	if state.hasCost {
		return status + fmt.Sprintf(" | est $%.6f this run", state.estimatedCost)
	}
	return status
}

func (s *Session) renderHeaderBody(eventCount int) string {
	model := "mock/offline"
	if s.args.Runtime != nil {
		model = s.args.Runtime.Model
	}
	return strings.Join([]string{
		"Terminal coding assistant with a card-style session layout.",
		"",
		s.args.CWD,
		fmt.Sprintf("[session] local  [model] %s  [messages] %d  [events] %d  [skills] %d  [mcp] %d", model, len(s.args.Messages), eventCount, len(s.args.Tools.Skills()), len(s.args.Tools.MCPServers())),
	}, "\n")
}

func (s *Session) currentModelName() string {
	if s.args.Runtime != nil && s.args.Runtime.Model != "" {
		return s.args.Runtime.Model
	}
	return "mock/offline"
}

func (s *Session) homeSections() []tui.InfoSection {
	return []tui.InfoSection{
		{
			Title: "Tips for getting started",
			Body: strings.Join([]string{
				"Run /help to browse commands and shortcuts.",
				"Review the approval diff before applying edits.",
				"Ask directly for fixes, refactors, or explanations.",
			}, "\n"),
		},
		{
			Title: "What's new",
			Body: strings.Join([]string{
				"Windows TUI input now handles Chinese text and punctuation.",
				"OpenAI responses API support is wired into MythosCode.",
				"Tool output collapses automatically for cleaner sessions.",
			}, "\n"),
		},
		{
			Title: "Workspace",
			Body: strings.Join([]string{
				s.args.CWD,
				"Project-local config: .mythos-code",
			}, "\n"),
		},
		{
			Title: "Live usage",
			Body:  "token usage updates as the model responds.",
		},
	}
}

func (s *Session) homeBanner() string {
	if s.args.Runtime == nil || s.args.Runtime.Model == "" {
		return "No runtime model is active yet. Configure your provider, then start with a task like \"fix failing tests\"."
	}
	return ""
}

func ansiStatusLine(model, cwd, status string, width int) string {
	model, cwd, status = safety.EscapeTerminal(model), safety.EscapeTerminal(cwd), safety.EscapeTerminal(status)
	leftPlain := brand.AppName + "  " + cwd
	rightPlain := model + " | " + status
	gap := width - len([]rune(leftPlain)) - len([]rune(rightPlain))
	if gap < 1 {
		gap = 1
	}
	left := "\x1b[36m\x1b[1m" + brand.AppName + "\x1b[0m" + "\x1b[2m  " + cwd + "\x1b[0m"
	right := "\x1b[2m" + rightPlain + "\x1b[0m"
	return left + strings.Repeat(" ", gap) + right
}

func ansiDimLine(text string, width int) string {
	text = safety.EscapeTerminal(text)
	if len([]rune(text)) >= width {
		return "\x1b[2m" + text + "\x1b[0m"
	}
	return "\x1b[2m" + text + strings.Repeat(" ", width-len([]rune(text))) + "\x1b[0m"
}

func (s *Session) visibleSlashCommands(input string) []tui.SlashCommand {
	if !strings.HasPrefix(input, "/") {
		return nil
	}
	matches := commands.SlashCommands
	if input != "/" {
		usages := map[string]bool{}
		for _, usage := range commands.FindMatching(input) {
			usages[usage] = true
		}
		matches = nil
		for _, command := range commands.SlashCommands {
			if usages[command.Usage] {
				matches = append(matches, command)
			}
		}
	}
	out := make([]tui.SlashCommand, 0, len(matches))
	for _, command := range matches {
		out = append(out, tui.SlashCommand{Usage: command.Usage, Description: command.Description})
	}
	return out
}

func mapEntryKind(kind string) tui.EntryKind {
	switch kind {
	case "user":
		return tui.EntryUser
	case "assistant":
		return tui.EntryAssistant
	case "progress":
		return tui.EntryProgress
	case "tool":
		return tui.EntryTool
	default:
		return tui.EntryAssistant
	}
}

func mapToolStatus(status string) tui.ToolStatus {
	switch status {
	case "running":
		return tui.ToolRunning
	case "error":
		return tui.ToolError
	default:
		return tui.ToolSuccess
	}
}

func terminalSize() (int, int) {
	width, height, err := terminal.Size(os.Stdout)
	if err != nil || width <= 0 || height <= 0 {
		return 100, 40
	}
	return width, height
}

func (s *Session) runApprovalPrompt(ctx context.Context, state *tuiState, request permissions.Request, render func()) (permissions.PromptResult, error) {
	approval := newApprovalState(request, nil)
	state.pendingApproval = approval
	render()
	defer func() {
		state.pendingApproval = nil
		render()
	}()

	rest := ""
	buffer := make([]byte, 64)
	for {
		select {
		case <-ctx.Done():
			return permissions.PromptResult{}, ctx.Err()
		default:
		}
		events, nextRest, err := readTUIEvents(s.args.In, rest, buffer)
		if err != nil {
			if err == io.EOF {
				return permissions.PromptResult{Decision: permissions.DecisionDenyOnce}, nil
			}
			return permissions.PromptResult{}, err
		}
		rest = nextRest
		for _, event := range events {
			done, result := approval.handle(event)
			render()
			if done {
				return result, nil
			}
		}
	}
}

func (s *Session) queueApprovalPrompt(ctx context.Context, request permissions.Request, events chan<- tuiAgentEvent) (permissions.PromptResult, error) {
	if s.args.Journal != nil {
		if _, err := s.args.Journal.Append(Event{Kind: EventApprovalRequested}); err != nil {
			return permissions.PromptResult{}, err
		}
	}
	resultCh := make(chan permissions.PromptResult, 1)
	select {
	case events <- tuiAgentEvent{kind: "approval_start", request: request, resultCh: resultCh}:
	case <-ctx.Done():
		return permissions.PromptResult{}, ctx.Err()
	}

	select {
	case result, ok := <-resultCh:
		if !ok {
			return permissions.PromptResult{Decision: permissions.DecisionDenyOnce}, nil
		}
		if s.args.Journal != nil {
			if _, err := s.args.Journal.Append(Event{Kind: EventApprovalDecided, Decision: string(result.Decision)}); err != nil {
				return permissions.PromptResult{}, err
			}
		}
		select {
		case events <- tuiAgentEvent{kind: "approval_done"}:
		case <-ctx.Done():
			return permissions.PromptResult{}, ctx.Err()
		}
		return result, nil
	case <-ctx.Done():
		return permissions.PromptResult{}, ctx.Err()
	}
}

func (s *Session) startAgentTurn(ctx context.Context, input string, agentEvents chan<- tuiAgentEvent) context.CancelFunc {
	turnCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		send := func(event tuiAgentEvent) {
			select {
			case agentEvents <- event:
			case <-ctx.Done():
			}
		}
		var err error
		if isShortcutInput(input) {
			err = s.runShortcutForTUI(turnCtx, input, send)
		} else {
			err = s.runAgentForTUI(turnCtx, input, send)
		}
		if err != nil {
			select {
			case agentEvents <- tuiAgentEvent{kind: "error", err: err}:
			case <-ctx.Done():
			}
		}
	}()
	return cancel
}

func (s *Session) runShortcutForTUI(ctx context.Context, input string, send func(tuiAgentEvent)) error {
	call, ok := commands.ParseShortcut(input)
	if !ok {
		return fmt.Errorf("invalid tool shortcut")
	}
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
	send(tuiAgentEvent{kind: "tool_start", toolName: call.ToolName, toolInput: call.Input})
	result := s.args.Tools.Execute(ctx, call.ToolName, call.Input, tools.Context{CWD: s.args.CWD, Permission: s.args.Permission, Tasks: s.tasks, Jobs: s.jobs, Network: s.network})
	if err := s.journalEvent(turnID, agent.Event{Kind: string(EventToolCompleted), ToolName: call.ToolName}); err != nil {
		return err
	}
	send(tuiAgentEvent{kind: "tool_result", toolName: call.ToolName, content: result.Output, isError: !result.OK})
	next := append(append([]message.Message(nil), s.args.Messages...), message.UserMessage(input), message.AssistantMessage(result.Output))
	if err := s.finishTurn(turnID, next, ctx.Err()); err != nil {
		return err
	}
	send(tuiAgentEvent{kind: "done", next: next, err: ctx.Err()})
	return nil
}

func (s *Session) applyTUIAgentEvent(state *tuiState, event tuiAgentEvent) {
	switch event.kind {
	case "model_start":
		state.streaming = nil
	case "text_delta":
		if state.streaming == nil {
			state.transcript = append(state.transcript, transcriptEntry{kind: "assistant"})
			state.streaming = &streamingReply{entryIndex: len(state.transcript) - 1}
		}
		state.transcript[state.streaming.entryIndex].body += event.content
	case "progress":
		finishAssistantEntry(state, "progress", event.content)
	case "tool_start":
		if state.pendingTools == nil {
			state.pendingTools = map[string][]int{}
		}
		index := appendToolStart(state, event.toolName, event.toolInput)
		state.pendingTools[event.toolName] = append(state.pendingTools[event.toolName], index)
	case "tool_result":
		finishToolResult(state, state.pendingTools, event.toolName, event.content, event.isError)
	case "assistant":
		finishAssistantEntry(state, "assistant", event.content)
	case "usage":
		state.tokens = event.usage
		if s.args.Runtime != nil && s.args.Runtime.Pricing != nil {
			amount, err := cost.Estimate(event.usage, *s.args.Runtime.Pricing)
			if err != nil {
				state.costUnknown = true
			} else {
				state.estimatedCost += amount
				state.hasCost = true
			}
		}
	case "approval_start":
		event.request.Summary = safety.Redact(s.args.Store.Context, event.request.Summary)
		for i := range event.request.Details {
			event.request.Details[i] = safety.Redact(s.args.Store.Context, event.request.Details[i])
		}
		state.pendingApproval = newApprovalState(event.request, event.resultCh)
		state.status = "Waiting for approval"
	case "approval_done":
		state.pendingApproval = nil
		if state.busy {
			state.status = busyStatusText(state.spinnerFrame)
		} else {
			state.status = "Ready"
		}
	case "done":
		state.busy = false
		state.streaming = nil
		state.pendingApproval = nil
		state.cancelTurn = nil
		s.args.Messages = event.next
		if event.err != nil {
			appendAssistantEntry(state, safety.Redact(s.args.Store.Context, event.err.Error()))
		}
		if s.args.Journal == nil {
			if err := s.persistMessages(); err != nil {
				appendAssistantEntry(state, err.Error())
				state.status = "Persist failed"
				return
			}
		}
		if event.err != nil {
			state.status = "Request stopped"
		} else {
			state.status = "Ready"
		}
	case "error":
		state.busy = false
		state.streaming = nil
		if event.err != nil {
			appendAssistantEntry(state, event.err.Error())
		}
		state.status = "Request failed"
	}
}

func newApprovalState(request permissions.Request, resultCh chan permissions.PromptResult) *approvalState {
	a := &approvalState{request: request, resultCh: resultCh}
	for i, choice := range request.Choices {
		if choice.Decision == permissions.DecisionDenyOnce {
			a.selected = i
			break
		}
	}
	return a
}

func busyStatusText(frame int) string {
	frames := []string{"|", "/", "-", "\\"}
	return frames[frame%len(frames)] + " Working (Ctrl+C cancel)"
}

func appendAssistantEntry(state *tuiState, content string) {
	state.transcript = append(state.transcript, transcriptEntry{kind: "assistant", body: content})
}

func finishAssistantEntry(state *tuiState, kind, content string) {
	if state.streaming != nil && state.streaming.entryIndex < len(state.transcript) {
		state.transcript[state.streaming.entryIndex] = transcriptEntry{kind: kind, body: content}
	} else {
		state.transcript = append(state.transcript, transcriptEntry{kind: kind, body: content})
	}
	state.streaming = nil
}

func (a *approvalState) handle(event tui.InputEvent) (bool, permissions.PromptResult) {
	if event.Kind == tui.EventKey && event.Name == tui.KeyEscape {
		if a.feedbackMode {
			a.feedbackMode = false
			a.feedbackInput = ""
			return false, permissions.PromptResult{}
		}
		return true, permissions.PromptResult{Decision: permissions.DecisionDenyOnce}
	}
	if event.Kind == tui.EventText && event.Ctrl && event.Text == "o" {
		a.expanded = !a.expanded
		a.scroll = 0
		return false, permissions.PromptResult{}
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyPageUp {
		a.scroll -= 8
		if a.scroll < 0 {
			a.scroll = 0
		}
		return false, permissions.PromptResult{}
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyPageDown {
		a.scroll += 8
		return false, permissions.PromptResult{}
	}
	if a.feedbackMode {
		if event.Kind == tui.EventKey && event.Name == tui.KeyBackspace {
			if len(a.feedbackInput) > 0 {
				a.feedbackInput = a.feedbackInput[:previousRuneStart(a.feedbackInput, len(a.feedbackInput))]
			}
			return false, permissions.PromptResult{}
		}
		if event.Kind == tui.EventText && !event.Ctrl {
			a.feedbackInput += event.Text
			return false, permissions.PromptResult{}
		}
		if event.Kind == tui.EventKey && event.Name == tui.KeyReturn {
			return true, permissions.PromptResult{Decision: permissions.DecisionDenyWithFeedback, Feedback: strings.TrimSpace(a.feedbackInput)}
		}
		return false, permissions.PromptResult{}
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyUp {
		if len(a.request.Choices) > 0 {
			a.selected = (a.selected - 1 + len(a.request.Choices)) % len(a.request.Choices)
		}
		return false, permissions.PromptResult{}
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyDown {
		if len(a.request.Choices) > 0 {
			a.selected = (a.selected + 1) % len(a.request.Choices)
		}
		return false, permissions.PromptResult{}
	}
	if event.Kind == tui.EventKey && event.Name == tui.KeyReturn {
		if len(a.request.Choices) == 0 {
			return true, permissions.PromptResult{Decision: permissions.DecisionDenyOnce}
		}
		choice := a.request.Choices[min(a.selected, len(a.request.Choices)-1)]
		if choice.Decision == permissions.DecisionDenyWithFeedback {
			a.feedbackMode = true
			a.feedbackInput = ""
			return false, permissions.PromptResult{}
		}
		return true, permissions.PromptResult{Decision: choice.Decision}
	}
	return false, permissions.PromptResult{}
}

func renderApprovalPrompt(state approvalState, height int) string {
	lines := []string{
		"Approval Required",
		safety.EscapeTerminal(state.request.Summary),
	}
	details := flattenDetails(state.request.Details)
	limit := 16
	if state.expanded {
		limit = max(8, height-10)
	}
	start := state.scroll
	if start > len(details) {
		start = len(details)
	}
	end := start + limit
	if end > len(details) {
		end = len(details)
	}
	lines = append(lines, details[start:end]...)
	if !state.expanded && len(details) > end {
		lines = append(lines, fmt.Sprintf("... %d more line(s) hidden", len(details)-end), "Ctrl+O expand full details")
	} else if state.expanded {
		lines = append(lines, "Ctrl+O collapse | PgUp/PgDn scroll")
	}
	lines = append(lines, "")
	if state.feedbackMode {
		lines = append(lines, "Reject With Guidance", "Type feedback for model, Enter submit, Esc back", "> "+safety.EscapeTerminal(state.feedbackInput))
	} else {
		for index, choice := range state.request.Choices {
			prefix := "  "
			if index == state.selected {
				prefix = "> "
			}
			lines = append(lines, prefix+safety.EscapeTerminal(choice.Label))
		}
		lines = append(lines, "", "Use Up/Down to select, Enter confirm, Esc deny once")
	}
	return strings.Join(lines, "\n")
}

func flattenDetails(details []string) []string {
	lines := []string{}
	for index, detail := range details {
		detail = safety.EscapeTerminal(detail)
		if index > 0 {
			lines = append(lines, "")
		}
		if looksLikeUnifiedDiff(detail) {
			detail = tui.RenderUnifiedDiff(detail)
		}
		lines = append(lines, strings.Split(detail, "\n")...)
	}
	return lines
}

func looksLikeUnifiedDiff(detail string) bool {
	return strings.Contains(detail, "\n+++ ") && strings.Contains(detail, "\n@@ ")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func historyUp(state *tuiState) bool {
	if len(state.history) == 0 || state.historyIndex <= 0 {
		return false
	}
	if state.historyIndex == len(state.history) {
		state.historyDraft = state.input
	}
	state.historyIndex--
	state.input = state.history[state.historyIndex]
	state.cursor = len(state.input)
	return true
}

func historyDown(state *tuiState) bool {
	if state.historyIndex >= len(state.history) {
		return false
	}
	state.historyIndex++
	if state.historyIndex == len(state.history) {
		state.input = state.historyDraft
	} else {
		state.input = state.history[state.historyIndex]
	}
	state.cursor = len(state.input)
	return true
}

func isShortcutInput(input string) bool {
	_, ok := commands.ParseShortcut(input)
	return ok
}

func appendToolStart(state *tuiState, toolName string, input any) int {
	summary, path, isEdit := summarizeToolInputDetails(toolName, input)
	state.transcript = append(state.transcript, transcriptEntry{
		kind:           "tool",
		toolName:       toolName,
		status:         "running",
		body:           summary,
		path:           path,
		editOperation:  isEdit,
		aggregateCount: boolToCount(isEdit),
	})
	return len(state.transcript) - 1
}

func finishToolResult(state *tuiState, pending map[string][]int, toolName, output string, isError bool) {
	status := "success"
	if isError {
		status = "error"
	}
	index := -1
	if queue := pending[toolName]; len(queue) > 0 {
		index = queue[0]
		pending[toolName] = queue[1:]
		if len(pending[toolName]) == 0 {
			delete(pending, toolName)
		}
	}
	if index < 0 || index >= len(state.transcript) {
		state.transcript = append(state.transcript, transcriptEntry{kind: "tool", toolName: toolName, status: status, body: output})
		return
	}
	state.transcript[index].status = status
	state.transcript[index].body = output
	if status == "success" {
		aggregateConsecutiveEdit(state, index)
	}
}

func aggregateConsecutiveEdit(state *tuiState, index int) {
	if index <= 0 || index >= len(state.transcript) {
		return
	}
	current := state.transcript[index]
	previous := state.transcript[index-1]
	if !current.editOperation || !previous.editOperation || current.path == "" || previous.path != current.path || previous.status != "success" || current.status != "success" {
		return
	}
	count := previous.aggregateCount
	if count <= 0 {
		count = 1
	}
	currentCount := current.aggregateCount
	if currentCount <= 0 {
		currentCount = 1
	}
	previous.aggregateCount = count + currentCount
	previous.toolName = "file edits"
	previous.body = fmt.Sprintf("%d edit operations applied to %s", previous.aggregateCount, previous.path)
	state.transcript[index-1] = previous
	state.transcript = append(state.transcript[:index], state.transcript[index+1:]...)
}

func summarizeToolInput(toolName string, input any) string {
	summary, _, _ := summarizeToolInputDetails(toolName, input)
	return summary
}

func summarizeToolInputDetails(toolName string, input any) (string, string, bool) {
	if typed, ok := input.(map[string]any); ok {
		if path, ok := typed["path"].(string); ok && path != "" {
			return toolName + " path=" + path, path, isEditTool(toolName)
		}
		if command, ok := typed["command"].(string); ok && command != "" {
			return toolName + " command=" + command, "", false
		}
	}
	return toolName, "", false
}

func summarizeSuccessfulToolEntry(entry transcriptEntry) string {
	if entry.aggregateCount > 1 && entry.path != "" {
		return fmt.Sprintf("%d edit operations applied to %s", entry.aggregateCount, entry.path)
	}
	firstLine := strings.TrimSpace(strings.Split(entry.body, "\n")[0])
	if firstLine == "" {
		firstLine = "completed"
	}
	if len(firstLine) > 96 {
		firstLine = firstLine[:93] + "..."
	}
	return entry.toolName + " completed: " + firstLine
}

func isEditTool(toolName string) bool {
	switch toolName {
	case "write_file", "modify_file", "edit_file", "patch_file":
		return true
	default:
		return false
	}
}

func boolToCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func previousRuneStart(text string, cursor int) int {
	if cursor <= 0 {
		return 0
	}
	if cursor > len(text) {
		cursor = len(text)
	}
	for cursor > 0 && !utf8.RuneStart(text[cursor-1]) {
		cursor--
	}
	if cursor > 0 {
		cursor--
		for cursor > 0 && !utf8.RuneStart(text[cursor]) {
			cursor--
		}
	}
	return cursor
}
