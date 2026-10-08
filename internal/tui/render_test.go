package tui

import (
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/message"
)

func TestRenderPanelWrapsTitleAndBody(t *testing.T) {
	out := RenderPanel("session", "hello\nworld", PanelOptions{Width: 24})
	for _, want := range []string{"\u250c", "\u2510", "\u2514", "\u2518", "session", "hello", "world"} {
		if !strings.Contains(out, want) {
			t.Fatalf("panel missing %q:\n%s", want, out)
		}
	}
}

func TestRenderPanelAccountsForWideCharacters(t *testing.T) {
	out := RenderPanel("\u4f1a\u8bdd", "\u4f60\u597d\u4e16\u754cabc", PanelOptions{Width: 24})
	plain := ansiPattern.ReplaceAllString(out, "")
	lines := strings.Split(plain, "\n")
	for _, line := range lines {
		if displayWidth(line) != 24 {
			t.Fatalf("panel line width=%d want=24 line=%q\n%s", displayWidth(line), line, plain)
		}
	}
}

func TestRenderTranscriptShowsScrolledEntries(t *testing.T) {
	entries := []TranscriptEntry{
		{ID: 1, Kind: EntryUser, Body: "first"},
		{ID: 2, Kind: EntryAssistant, Body: "second"},
		{ID: 3, Kind: EntryTool, ToolName: "read_file", Status: ToolSuccess, Body: "third", Collapsed: true, CollapsedSummary: "read ok"},
	}
	out := RenderTranscript(entries, 0, 20)
	plain := ansiPattern.ReplaceAllString(out, "")
	for _, want := range []string{"you", "assistant", "tool read_file", "read ok"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("transcript missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSlashMenuHighlightsSelection(t *testing.T) {
	out := RenderSlashMenu([]SlashCommand{{Usage: "/help", Description: "Help"}, {Usage: "/tools", Description: "Tools"}}, 1)
	if !strings.Contains(out, "> ") || !strings.Contains(out, "/tools") || !strings.Contains(out, "\x1b[7m") {
		t.Fatalf("selection not highlighted:\n%s", out)
	}
}

func TestRenderUnifiedDiffHighlightsSyntaxAndChangedSpan(t *testing.T) {
	out := RenderUnifiedDiff("--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,3 @@\n func demo() {\n-oldName := 1\n+newName := 1\n }\n")
	for _, want := range []string{
		"\x1b[36m--- a/main.go",
		"\x1b[36m+++ b/main.go",
		"\x1b[35m@@ -1,3 +1,3 @@",
		"\x1b[31m-",
		"\x1b[32m+",
		"\x1b[1mold",
		"\x1b[1mnew",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered diff missing %q:\n%q", want, out)
		}
	}
}

func TestRenderPanelKeepsAnsiDecoratedContentVisible(t *testing.T) {
	out := RenderPanel("diff", RenderUnifiedDiff("-old\n+new"), PanelOptions{Width: 24})
	visible := ansiPattern.ReplaceAllString(out, "")
	for _, want := range []string{"-old", "+new"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("panel lost ansi-decorated text %q:\n%q", want, out)
		}
	}
}

func TestRenderTranscriptFormatsMarkdownishAssistantText(t *testing.T) {
	out := RenderTranscript([]TranscriptEntry{{
		ID:   1,
		Kind: EntryAssistant,
		Body: "# Title\n- item with `code` and **bold**\n```go\nfmt.Println(1)\n```",
	}}, 0, 20)
	for _, want := range []string{
		"\x1b[36m\x1b[1mTitle\x1b[0m",
		"\x1b[33m\u2022\x1b[0m item with \x1b[35mcode\x1b[0m and \x1b[1mbold\x1b[0m",
		"\x1b[2m```go\x1b[0m",
		"\x1b[2mfmt.Println(1)\x1b[0m",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("transcript missing markdownish rendering %q:\n%q", want, out)
		}
	}
}

func TestRenderTranscriptShowsToolCollapsePhaseAndDefaultSummary(t *testing.T) {
	collapsing := RenderTranscript([]TranscriptEntry{{
		ID:            1,
		Kind:          EntryTool,
		ToolName:      "run_command",
		Status:        ToolSuccess,
		Body:          "long output",
		CollapsePhase: 2,
	}}, 0, 20)
	if !strings.Contains(collapsing, "collapsing..") {
		t.Fatalf("collapse phase not rendered:\n%q", collapsing)
	}

	collapsed := RenderTranscript([]TranscriptEntry{{
		ID:        2,
		Kind:      EntryTool,
		ToolName:  "run_command",
		Status:    ToolSuccess,
		Collapsed: true,
	}}, 0, 20)
	if !strings.Contains(collapsed, "output collapsed") {
		t.Fatalf("default collapsed summary not rendered:\n%q", collapsed)
	}
}

func TestRenderTranscriptPreviewsLargeToolOutput(t *testing.T) {
	lines := make([]string, 40)
	for index := range lines {
		lines[index] = "line " + itoa(index+1)
	}
	out := RenderTranscript([]TranscriptEntry{{
		ID:       1,
		Kind:     EntryTool,
		ToolName: "read_file",
		Status:   ToolSuccess,
		Body:     strings.Join(lines, "\n"),
	}}, 0, 80)
	if strings.Contains(out, "line 40") || !strings.Contains(out, "output truncated in transcript") {
		t.Fatalf("large read_file output was not previewed:\n%q", out)
	}
}

func TestRenderHomeScreenShowsSectionsAndComposer(t *testing.T) {
	out := RenderHomeScreen("MyCode", "v0.1.0", "D:\\MiniCode", "gpt-5.5", []InfoSection{
		{Title: "Tips for getting started", Body: "Run /help"},
		{Title: "What's new", Body: "Fresh paint"},
	}, "Banner text", "", 0, nil, 0, message.TokenUsage{TotalTokens: 321}, 100, 32)
	plain := ansiPattern.ReplaceAllString(out, "")
	for _, want := range []string{"MyCode", "Welcome back!", "Tips for getting started", "What's new", "Banner text", `Try "fix typecheck errors"`, "tok 321"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("home screen missing %q:\n%s", want, plain)
		}
	}
}

func TestRenderComposerShowsPromptAndFooter(t *testing.T) {
	out := RenderComposer("hello", 5, ComposerOptions{
		Width:      60,
		Prompt:     ">",
		RightText:  "Enter send",
		FooterText: "? shortcuts | / commands",
	})
	plain := ansiPattern.ReplaceAllString(out, "")
	for _, want := range []string{"> hello", "Enter send", "? shortcuts | / commands"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("composer missing %q:\n%s", want, plain)
		}
	}
}

func TestFormatUsageCompact(t *testing.T) {
	got := formatUsageCompact(message.TokenUsage{InputTokens: 120, OutputTokens: 45, TotalTokens: 165})
	if got != "tok 165 (120/45)" {
		t.Fatalf("unexpected compact usage: %q", got)
	}
}
