package prompt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/tools"
	"github.com/BigSmartie/Coding-Agent/internal/trust"
)

func TestBuildIncludesSkillsMCPAndClaudeFiles(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "CLAUDE.md"), []byte("Project rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "CLAUDE.md"), []byte("Global rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	trusted := trust.Store{Dir: t.TempDir()}
	fingerprint, _, err := trust.WorkspaceFingerprint(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := trusted.Grant(cwd, "workspace", fingerprint, fingerprint); err != nil {
		t.Fatal(err)
	}
	snapshot, err := trusted.Workspace(cwd)
	if err != nil {
		t.Fatal(err)
	}

	got := Build(context.Background(), Args{
		CWD:               cwd,
		Home:              home,
		Project:           snapshot,
		PermissionSummary: []string{"cwd: " + cwd},
		Skills:            []tools.SkillSummary{{Name: "demo", Description: "Demo skill"}},
		MCPServers:        []tools.MCPServerSummary{{Name: "fs", Status: "connected", ToolCount: 2}},
	})

	for _, want := range []string{"You are mythoscode", "Demo skill", "fs: connected", "Global rule", "Project rule"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestBuildConsumesSnapshotAfterProjectFileChanges(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "CLAUDE.md")
	if err := os.WriteFile(path, []byte("reviewed instruction"), 0600); err != nil {
		t.Fatal(err)
	}
	trusted := trust.Store{Dir: t.TempDir()}
	fingerprint, _, err := trust.WorkspaceFingerprint(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := trusted.Grant(cwd, "workspace", fingerprint, fingerprint); err != nil {
		t.Fatal(err)
	}
	snapshot, err := trusted.Workspace(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unreviewed replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	got := Build(context.Background(), Args{CWD: cwd, Home: t.TempDir(), Project: snapshot})
	if !strings.Contains(got, "reviewed instruction") || strings.Contains(got, "unreviewed replacement") {
		t.Fatal("prompt did not use reviewed snapshot")
	}
	got = Build(context.Background(), Args{CWD: t.TempDir(), Home: t.TempDir(), Project: snapshot})
	if strings.Contains(got, "reviewed instruction") {
		t.Fatal("snapshot was accepted for another workspace")
	}
}

func TestGlobalInstructionLinkCannotImportUntrustedProject(t *testing.T) {
	for _, hardlink := range []bool{false, true} {
		t.Run(fmt.Sprint(hardlink), func(t *testing.T) {
			cwd, home := t.TempDir(), t.TempDir()
			project := filepath.Join(cwd, "CLAUDE.md")
			if err := os.WriteFile(project, []byte("untrusted project payload"), 0600); err != nil {
				t.Fatal(err)
			}
			global := filepath.Join(home, ".claude", "CLAUDE.md")
			if err := os.MkdirAll(filepath.Dir(global), 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			if hardlink {
				err = os.Link(project, global)
			} else {
				err = os.Symlink(project, global)
			}
			if err != nil {
				t.Skip("links unavailable")
			}
			got := Build(context.Background(), Args{CWD: cwd, Home: home})
			if strings.Contains(got, "untrusted project payload") {
				t.Fatal("global linked file bypassed project trust")
			}
		})
	}
}

func TestBuildDoesNotLoadUntrustedProjectInstructions(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "CLAUDE.md"), []byte("do not load this project instruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Build(context.Background(), Args{CWD: cwd, Home: t.TempDir()})
	if strings.Contains(got, "do not load this project instruction") {
		t.Fatal("untrusted instructions entered system prompt")
	}
}

func TestBuildIncludesTypeScriptParityBehaviorRules(t *testing.T) {
	out := Build(context.Background(), Args{CWD: t.TempDir()})
	for _, want := range []string{
		"If a missing preference would materially change the result",
		"When using read_file, pay attention to the header fields",
		"If the user names a skill or clearly asks for a workflow",
		"Do not stop after a progress update",
		"After you have used any tool in the current turn",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("prompt missing %q:\n%s", want, out)
		}
	}
}

func TestGlobalMemoryExpandsBoundedNestedInclude(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	root := filepath.Join(home, ".mythos-code")
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte("global rule\n@include notes/details.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes", "details.md"), []byte("nested global rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Build(context.Background(), Args{CWD: cwd, Home: home})
	if !strings.Contains(got, "global rule") || !strings.Contains(got, "nested global rule") {
		t.Fatalf("global memory include missing: %s", got)
	}
	if err := os.WriteFile(filepath.Join(root, "notes", "details.md"), []byte("@include details.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = Build(context.Background(), Args{CWD: cwd, Home: home})
	if strings.Contains(got, "global rule") {
		t.Fatal("cyclic global memory was partially imported")
	}
}

func TestReviewedProjectMemoryExpandsNestedIncludeInPrompt(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "MEMORY.md"), []byte("project memory\n@include notes/detail.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "notes", "detail.md"), []byte("reviewed nested memory"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := trust.Store{Dir: t.TempDir()}
	hash, _, err := trust.WorkspaceFingerprint(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Grant(cwd, "workspace", hash, hash); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Workspace(cwd)
	if err != nil {
		t.Fatal(err)
	}
	got := Build(context.Background(), Args{CWD: cwd, Home: home, Project: snapshot})
	if !strings.Contains(got, "project memory") || !strings.Contains(got, "reviewed nested memory") {
		t.Fatalf("reviewed memory missing from prompt: %s", got)
	}
}
