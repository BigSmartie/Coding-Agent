package tui

import (
	"strings"
	"testing"
)

func TestUntrustedTerminalControlSequencesAreEscaped(t *testing.T) {
	attack := "before\x1b[2J\x1b]52;c;clipboard\a\rOVERWRITE\u202eafter"
	for _, output := range []string{RenderTranscript([]TranscriptEntry{{Kind: EntryTool, Body: attack, ToolName: attack}}, 0, 20), RenderUnifiedDiff("--- a\n+++ b\n@@ -1 +1 @@\n-old\n+" + attack), RenderInputPrompt(attack, len(attack))} {
		if strings.Contains(output, "\x1b[2J") || strings.Contains(output, "\x1b]52;") || strings.ContainsAny(output, "\a\r\u202e") {
			t.Fatalf("terminal injection: %q", output)
		}
	}
}
