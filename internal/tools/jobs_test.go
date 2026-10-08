package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/jobs"
	"github.com/BigSmartie/Coding-Agent/internal/permissions"
)

type artifactJob struct{ data []byte }

func (a artifactJob) Start(context.Context, jobs.Spec) (jobs.Snapshot, error) {
	return jobs.Snapshot{}, nil
}
func (a artifactJob) Poll(string) (jobs.Snapshot, error)               { return jobs.Snapshot{}, nil }
func (a artifactJob) List() []jobs.Snapshot                            { return nil }
func (a artifactJob) Read(string, int64, int) (jobs.ReadResult, error) { return jobs.ReadResult{}, nil }
func (a artifactJob) Write(context.Context, string, string) error      { return nil }
func (a artifactJob) Cancel(string) error                              { return nil }
func (a artifactJob) ReadArtifact(context.Context, string, string) ([]byte, error) {
	return a.data, nil
}

func TestJobExportRequiresReviewedWriteAndRejectsBinary(t *testing.T) {
	dir := t.TempDir()
	reg := Builtins(dir, nil, nil)
	input := map[string]any{"id": "job", "source": "generated.txt", "destination": "generated.txt"}
	ctx := Context{CWD: dir, Jobs: artifactJob{data: []byte("generated text\n")}}
	result := reg.Execute(context.Background(), "job_export", input, ctx)
	if result.OK || !strings.Contains(result.Output, "approval") {
		t.Fatalf("export bypassed review: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "generated.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied export changed workspace: %v", err)
	}
	var reviewed string
	permission, err := permissions.New(dir, filepath.Join(t.TempDir(), "permissions.json"), func(_ context.Context, request permissions.Request) (permissions.PromptResult, error) {
		reviewed = strings.Join(request.Details, "\n")
		return permissions.PromptResult{Decision: permissions.DecisionAllowOnce}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx.Permission = permission
	result = reg.Execute(context.Background(), "job_export", input, ctx)
	if !result.OK || !strings.Contains(reviewed, "+generated text") {
		t.Fatalf("reviewed export failed: %#v, diff=%q", result, reviewed)
	}
	data, err := os.ReadFile(filepath.Join(dir, "generated.txt"))
	if err != nil || string(data) != "generated text\n" {
		t.Fatalf("exported wrong data: %q, %v", data, err)
	}
	ctx.Jobs = artifactJob{data: []byte{'a', 0, 'b'}}
	result = reg.Execute(context.Background(), "job_export", input, ctx)
	if result.OK || !strings.Contains(result.Output, "UTF-8 text") {
		t.Fatalf("binary artifact passed export: %#v", result)
	}
}
