package session

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/model"
	"github.com/BigSmartie/Coding-Agent/internal/permissions"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
	"github.com/BigSmartie/Coding-Agent/internal/tui"
)

func handleTUIForTest(s *Session, ctx context.Context, state *tuiState, event tui.InputEvent) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	events := make(chan tuiAgentEvent, 64)
	exit, err := s.handleTUIEvent(ctx, state, event, events)
	if err != nil {
		return exit, err
	}
	for state.busy {
		select {
		case event := <-events:
			s.applyTUIAgentEvent(state, event)
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return exit, nil
}

func TestTUIApprovalDefaultsToDenial(t *testing.T) {
	a := newApprovalState(permissions.Request{Choices: []permissions.Choice{{Decision: permissions.DecisionAllowOnce}, {Decision: permissions.DecisionDenyOnce}}}, nil)
	done, result := a.handle(tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyReturn})
	if !done || result.Decision != permissions.DecisionDenyOnce {
		t.Fatal("Enter approved by default")
	}
}

func TestTUIShortcutApprovalDoesNotBlockEventLoop(t *testing.T) {
	cwd := t.TempDir()
	pm, err := permissions.New(cwd, filepath.Join(t.TempDir(), "permissions.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Args{CWD: cwd, Tools: tools.Builtins(cwd, nil, nil), Model: model.Mock{}, Permission: pm})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	events := make(chan tuiAgentEvent, 64)
	pm.SetPrompt(func(ctx context.Context, request permissions.Request) (permissions.PromptResult, error) {
		return s.queueApprovalPrompt(ctx, request, events)
	})
	state := tuiState{input: "/write a.txt::new"}
	if _, err := s.handleTUIEvent(ctx, &state, tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyReturn}, events); err != nil {
		t.Fatal(err)
	}
	sawApproval := false
	for state.busy {
		select {
		case event := <-events:
			s.applyTUIAgentEvent(&state, event)
			if state.pendingApproval != nil {
				sawApproval = true
				if _, err := s.handleTUIEvent(ctx, &state, tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyReturn}, events); err != nil {
					t.Fatal(err)
				}
			}
		case <-ctx.Done():
			t.Fatal("shortcut approval blocked the UI", ctx.Err())
		}
	}
	if !sawApproval {
		t.Fatal("shortcut skipped approval")
	}
	if _, err := os.Stat(filepath.Join(cwd, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("default denial still wrote a file")
	}
}

type cancellableModel struct{ started chan struct{} }

func (m cancellableModel) Next(ctx context.Context, _ []message.Message) (message.Step, error) {
	close(m.started)
	<-ctx.Done()
	return message.Step{}, ctx.Err()
}

func TestTUICancelRetainsUserMessage(t *testing.T) {
	cwd := t.TempDir()
	started := make(chan struct{})
	record := NewRecord(cwd, []message.Message{message.SystemMessage("system")})
	s := New(Args{CWD: cwd, Tools: tools.NewRegistry(nil, tools.Metadata{}), Model: cancellableModel{started}, Messages: record.Messages, SessionID: record.ID, Store: Store{Dir: t.TempDir()}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	state := tuiState{input: "cancel this task"}
	events := make(chan tuiAgentEvent, 64)
	if _, err := s.handleTUIEvent(ctx, &state, tui.InputEvent{Kind: tui.EventKey, Name: tui.KeyReturn}, events); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if exit, err := s.handleTUIEvent(ctx, &state, tui.InputEvent{Kind: tui.EventText, Ctrl: true, Text: "c"}, events); err != nil || exit {
		t.Fatal("cancel exited TUI", err)
	}
	for state.busy {
		select {
		case event := <-events:
			s.applyTUIAgentEvent(&state, event)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	saved, err := s.args.Store.Load(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Messages) != 2 || saved.Messages[1].Content != "cancel this task" {
		t.Fatal("cancelled input lost")
	}
}

type streamingTestModel struct{}

func (streamingTestModel) Next(context.Context, []message.Message) (message.Step, error) {
	return message.Step{}, fmt.Errorf("streaming interface was not used")
}
func (streamingTestModel) NextStream(_ context.Context, _ []message.Message, delta func(string)) (message.Step, error) {
	delta("<final>hel")
	delta("lo")
	return message.AssistantStep("hello", message.ContentFinal, message.Diagnostics{}), nil
}

func TestTUIConsumesRealDeltasWithoutDuplicatingFinal(t *testing.T) {
	s := New(Args{CWD: t.TempDir(), Model: streamingTestModel{}, Tools: tools.NewRegistry(nil, tools.Metadata{})})
	state := tuiState{}
	sawDelta := false
	err := s.runAgentForTUI(context.Background(), "hello", func(event tuiAgentEvent) {
		s.applyTUIAgentEvent(&state, event)
		if event.kind == "text_delta" {
			sawDelta = true
			if len(state.transcript) != 1 {
				t.Fatal("delta not visible immediately")
			}
		}
	})
	if err != nil || !sawDelta || len(state.transcript) != 1 || state.transcript[0].body != "hello" {
		t.Fatalf("bad stream: %#v %v", state.transcript, err)
	}
}

func TestStoreAndHistoryRedactSecretsAndRejectInvalidIDs(t *testing.T) {
	ctx := safety.WithSecrets(context.Background(), "sample-exact-credential")
	store := Store{Dir: t.TempDir(), Context: ctx}
	record := NewRecord(t.TempDir(), []message.Message{message.UserMessage("credential sample-exact-credential")})
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store.Dir, record.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("sample-exact-credential")) {
		t.Fatal("credential persisted")
	}
	for _, id := range []string{"..", "../escape", `..\escape`, "name:ads", "."} {
		if _, err := store.Load(id); err == nil {
			t.Fatal("invalid session ID accepted")
		}
	}
	history := History{Path: filepath.Join(t.TempDir(), "history.json"), Context: ctx}
	if err := history.Save([]string{"sample-exact-credential"}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(history.Path)
	if bytes.Contains(data, []byte("sample-exact-credential")) {
		t.Fatal("credential in history")
	}
	if NewRecord("", nil).ID == NewRecord("", nil).ID {
		t.Fatal("session ID collision")
	}
}

func TestLineSessionEscapesControlsAndCredentials(t *testing.T) {
	var out bytes.Buffer
	s := New(Args{Tools: tools.NewRegistry(nil, tools.Metadata{}), Model: model.Mock{}, Out: &out, Runtime: &config.Runtime{APIKey: "runtime-credential-value"}})
	w := plainWriter{out: &out, ctx: s.args.Store.Context}
	_, _ = w.Write([]byte("runtime-credential-value\x1b]52;c;malicious\a"))
	if strings.Contains(out.String(), "runtime-credential-value") || strings.ContainsAny(out.String(), "\x1b\a") {
		t.Fatal("unsafe terminal output")
	}
}
