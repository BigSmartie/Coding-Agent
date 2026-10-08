package filereview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type approvingPermission struct {
	target string
	diff   string
}

func (p *approvingPermission) EnsureEdit(_ context.Context, targetPath, diffPreview string) error {
	p.target = targetPath
	p.diff = diffPreview
	return nil
}

func TestApplyReviewedChangeWritesAfterApproval(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	permission := &approvingPermission{}

	result := ApplyReviewedChange(context.Background(), permission, dir, "note.txt", target, "new\n")
	if !result.OK {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !strings.Contains(permission.diff, "--- a/note.txt") || !strings.Contains(permission.diff, "+++ b/note.txt") {
		t.Fatalf("unexpected diff: %s", permission.diff)
	}
	content, _ := os.ReadFile(target)
	if string(content) != "new\n" {
		t.Fatalf("unexpected content: %q", string(content))
	}
}

func TestLargeDiffUsesBoundedFallback(t *testing.T) {
	diff := BuildUnifiedDiff("large.txt", strings.Repeat("old\n", 2000), strings.Repeat("new\n", 2000))
	if !strings.Contains(diff, "-old\n") || !strings.Contains(diff, "+new\n") {
		t.Fatal("bounded diff lost content")
	}
}

func TestBuildUnifiedDiffKeepsContextAroundChange(t *testing.T) {
	diff := BuildUnifiedDiff("note.txt", "a\nold\nc\n", "a\nnew\nc\n")
	for _, want := range []string{"--- a/note.txt", "+++ b/note.txt", "@@", " a\n", "-old\n", "+new\n", " c\n"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff missing %q:\n%s", want, diff)
		}
	}
}

func TestBuildUnifiedDiffSplitsDistantChangesIntoMultipleHunks(t *testing.T) {
	before := strings.Join([]string{
		"one",
		"same-2",
		"same-3",
		"same-4",
		"same-5",
		"same-6",
		"same-7",
		"same-8",
		"same-9",
		"ten",
		"",
	}, "\n")
	after := strings.Join([]string{
		"ONE",
		"same-2",
		"same-3",
		"same-4",
		"same-5",
		"same-6",
		"same-7",
		"same-8",
		"same-9",
		"TEN",
		"",
	}, "\n")

	diff := BuildUnifiedDiff("note.txt", before, after)
	if count := countHunkHeaders(diff); count != 2 {
		t.Fatalf("expected two hunks, got %d:\n%s", count, diff)
	}
	if strings.Contains(diff, " same-5\n") {
		t.Fatalf("middle unchanged context should be omitted between distant hunks:\n%s", diff)
	}
}

func countHunkHeaders(diff string) int {
	count := 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			count++
		}
	}
	return count
}
