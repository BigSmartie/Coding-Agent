package permissions

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestDangerousCommandRequiresPrompt(t *testing.T) {
	cwd := t.TempDir()
	var seen bool
	pm, err := New(cwd, filepath.Join(t.TempDir(), "permissions.json"), func(_ context.Context, request Request) (PromptResult, error) {
		seen = true
		if request.Kind != KindCommand || !strings.Contains(request.Summary, "dangerous") {
			t.Fatalf("unexpected request: %#v", request)
		}
		return PromptResult{Decision: DecisionAllowOnce}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.EnsureCommand(context.Background(), "git", []string{"reset", "--hard"}, cwd); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("expected prompt")
	}
}

func TestEditRejectWithFeedback(t *testing.T) {
	pm, err := New(t.TempDir(), filepath.Join(t.TempDir(), "permissions.json"), func(_ context.Context, _ Request) (PromptResult, error) {
		return PromptResult{Decision: DecisionDenyWithFeedback, Feedback: "try again"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = pm.EnsureEdit(context.Background(), filepath.Join(t.TempDir(), "a.txt"), "diff")
	if err == nil || !strings.Contains(err.Error(), "User guidance: try again") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClassifyDangerousGitOverwriteCommands(t *testing.T) {
	cases := []struct {
		command string
		args    []string
		want    string
	}{
		{"git", []string{"checkout", "--", "main.go"}, "git checkout -- can overwrite working tree files"},
		{"git", []string{"restore", "--source=HEAD", "main.go"}, "git restore --source can overwrite local files"},
	}
	for _, tc := range cases {
		got := ClassifyDangerousCommand(tc.command, tc.args)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("expected %q in classification, got %q", tc.want, got)
		}
	}
}

func TestSummaryIncludesPersistentAllowlists(t *testing.T) {
	cwd := t.TempDir()
	outside := filepath.Join(t.TempDir(), "notes.txt")
	storePath := filepath.Join(t.TempDir(), "permissions.json")
	pm, err := New(cwd, storePath, func(_ context.Context, request Request) (PromptResult, error) {
		switch request.Kind {
		case KindPath, KindCommand, KindEdit:
			return PromptResult{Decision: DecisionAllowAlways}, nil
		default:
			t.Fatalf("unexpected request kind: %s", request.Kind)
			return PromptResult{}, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.EnsurePathAccess(context.Background(), outside, "read"); err != nil {
		t.Fatal(err)
	}
	if err := pm.EnsureCommand(context.Background(), "git", []string{"reset", "--hard"}, cwd); err != nil {
		t.Fatal(err)
	}
	if err := pm.EnsureEdit(context.Background(), filepath.Join(cwd, "a.txt"), "diff"); err != nil {
		t.Fatal(err)
	}

	summary := strings.Join(pm.Summary(), "\n")
	for _, want := range []string{"cwd: " + cwd, "extra allowed dirs:", "approved command ids: command-v1:", "trusted edit targets:"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}
