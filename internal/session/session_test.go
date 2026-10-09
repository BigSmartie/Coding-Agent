package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/cost"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/model"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

type pricedModel struct{}

func (pricedModel) Next(context.Context, []message.Message) (message.Step, error) {
	return message.AssistantStep("done", message.ContentFinal, message.Diagnostics{Usage: message.TokenUsage{
		InputTokens: 1000, OutputTokens: 100, CacheReadTokens: 400,
	}}), nil
}

func TestRunOnceReportsUserPricedTokenCost(t *testing.T) {
	var out bytes.Buffer
	s := New(Args{
		CWD: t.TempDir(), Tools: tools.NewRegistry(nil, tools.Metadata{}), Model: pricedModel{},
		Runtime:  &config.Runtime{Pricing: &cost.Prices{InputPerMillion: 10, OutputPerMillion: 20, CacheReadPerMillion: 1, CacheWritePerMillion: 12}},
		Messages: []message.Message{message.SystemMessage("system")}, Out: &out,
	})
	if err := s.RunOnce(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "estimated cost (this turn): $0.008400") {
		t.Fatalf("cost estimate missing or double-counted cache: %s", out.String())
	}
}

func TestRunOnceExecutesShortcut(t *testing.T) {
	dir := t.TempDir()
	registry := tools.Builtins(dir, nil, nil)
	var out bytes.Buffer
	s := New(Args{
		CWD:      dir,
		Tools:    registry,
		Model:    model.Mock{},
		Messages: []message.Message{message.SystemMessage("system")},
		Out:      &out,
	})
	if err := s.RunOnce(context.Background(), "/ls"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(empty)") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRunOnceHandlesRuntimeSlashCommands(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	registry := tools.NewRegistry(nil, tools.Metadata{
		Skills:     []tools.SkillSummary{{Name: "demo", Description: "Demo skill", Source: "project"}},
		MCPServers: []tools.MCPServerSummary{{Name: "fs", Status: "connected", ToolCount: 2, Protocol: "content-length"}},
	})
	s := New(Args{
		CWD:      dir,
		Tools:    registry,
		Model:    model.Mock{},
		Runtime:  &config.Runtime{Provider: "anthropic", Model: "claude-test", BaseURL: "https://example.test", AuthToken: "token", SourceSummary: "test config"},
		Messages: []message.Message{message.SystemMessage("system")},
		Out:      &out,
	})

	for _, input := range []string{"/status", "/model", "/skills", "/mcp", "/permissions"} {
		if err := s.RunOnce(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	got := out.String()
	for _, want := range []string{"provider: anthropic", "model: claude-test", "current model: claude-test", "demo  Demo skill", "fs  status=connected", "permission store:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRunOncePersistsAgentMessages(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: t.TempDir()}
	var out bytes.Buffer
	s := New(Args{
		CWD:       dir,
		Tools:     tools.NewRegistry(nil, tools.Metadata{}),
		Model:     model.Mock{},
		Messages:  []message.Message{message.SystemMessage("system")},
		Store:     store,
		SessionID: "session-1",
		Out:       &out,
	})

	if err := s.RunOnce(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) < 3 || loaded.Messages[1].Role != message.RoleUser || loaded.Messages[len(loaded.Messages)-1].Role != message.RoleAssistant {
		t.Fatalf("unexpected persisted messages: %#v", loaded.Messages)
	}
}

func TestRunOnceTurnsModelErrorIntoAssistantFailureMessage(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: t.TempDir()}
	var out bytes.Buffer
	s := New(Args{
		CWD:       dir,
		Tools:     tools.NewRegistry(nil, tools.Metadata{}),
		Model:     failingModel{err: errors.New("No model configured")},
		Messages:  []message.Message{message.SystemMessage("system")},
		Store:     store,
		SessionID: "session-1",
		Out:       &out,
	})

	if err := s.RunOnce(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "request failed: No model configured") {
		t.Fatalf("expected request failure output, got %q", out.String())
	}
	loaded, err := store.Load("session-1")
	if err != nil {
		t.Fatal(err)
	}
	last := loaded.Messages[len(loaded.Messages)-1]
	if last.Role != message.RoleAssistant || !strings.Contains(last.Content, "request failed: No model configured") {
		t.Fatalf("expected persisted failure assistant message, got %#v", loaded.Messages)
	}
}

func TestRunOnceUsesResumedMessages(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: t.TempDir()}
	if err := store.Save(Record{
		ID:  "session-1",
		CWD: dir,
		Messages: []message.Message{
			message.SystemMessage("system"),
			message.UserMessage("previous"),
			message.AssistantMessage("earlier"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	record, err := store.Load("session-1")
	if err != nil {
		t.Fatal(err)
	}
	model := &recordingModel{}
	s := New(Args{
		CWD:       dir,
		Tools:     tools.NewRegistry(nil, tools.Metadata{}),
		Model:     model,
		Messages:  record.Messages,
		Store:     store,
		SessionID: record.ID,
		Out:       &bytes.Buffer{},
	})

	if err := s.RunOnce(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if len(model.seen) < 4 || model.seen[1].Content != "previous" || model.seen[len(model.seen)-1].Content != "next" {
		t.Fatalf("model did not receive resumed context: %#v", model.seen)
	}
}

func TestHistoryPersistsLastEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	h := History{Path: path}
	if err := h.Save([]string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	entries, err := h.Load()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(entries, ",") != "one,two" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

type recordingModel struct {
	seen []message.Message
}

func (m *recordingModel) Next(_ context.Context, messages []message.Message) (message.Step, error) {
	m.seen = append([]message.Message(nil), messages...)
	return message.AssistantStep("done", message.ContentFinal, message.Diagnostics{}), nil
}

type failingModel struct {
	err error
}

func (m failingModel) Next(context.Context, []message.Message) (message.Step, error) {
	return message.Step{}, m.err
}
