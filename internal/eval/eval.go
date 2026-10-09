// Package eval runs repository tasks against a disposable, pinned Git snapshot.
package eval

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/agent"
	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
	"github.com/BigSmartie/Coding-Agent/internal/workspace"
)

const maxSnapshotBytes = 50 << 20

var commitPattern = regexp.MustCompile(`^[a-fA-F0-9]{40,64}$`)

type Spec struct {
	SchemaVersion int               `json:"schemaVersion,omitempty"` // Omitted means version 1.
	ID            string            `json:"id"`
	Repository    string            `json:"repository"`
	Commit        string            `json:"commit"`
	Prompt        string            `json:"prompt"`
	ExpectedFiles map[string]string `json:"expectedFiles"` // SHA-256 hex or "absent"
}

type Report struct {
	SchemaVersion int      `json:"schemaVersion"`
	ID            string   `json:"id"`
	Commit        string   `json:"commit"`
	Passed        bool     `json:"passed"`
	Checks        []string `json:"checks"`
	ToolNames     []string `json:"toolNames"`
	Tokens        int      `json:"tokens"`
	DurationMS    int64    `json:"durationMs"`
}

// ModelFactory receives only the filtered evaluation registry. A live CLI
// adapter and deterministic scripted fixtures use the same runner.
type ModelFactory func(*tools.Registry) (message.Model, error)

func Run(ctx context.Context, spec Spec, factory ModelFactory) (Report, error) {
	report := Report{SchemaVersion: 1, ID: spec.ID, Commit: spec.Commit}
	if (spec.SchemaVersion != 0 && spec.SchemaVersion != 1) || spec.ID == "" || len(spec.ID) > 64 || len(spec.Prompt) == 0 || len(spec.Prompt) > 8192 || !commitPattern.MatchString(spec.Commit) || len(spec.ExpectedFiles) == 0 || factory == nil {
		return report, fmt.Errorf("invalid repository evaluation spec")
	}
	for path, want := range spec.ExpectedFiles {
		if err := validRelative(path); err != nil {
			return report, err
		}
		if want != "absent" {
			if len(want) != 64 {
				return report, fmt.Errorf("expectedFiles entry must be SHA-256 hex or absent")
			}
			if _, err := hex.DecodeString(want); err != nil {
				return report, err
			}
		}
	}
	started := time.Now()
	snapshot, err := os.MkdirTemp("", "mycode-eval-*")
	if err != nil {
		return report, err
	}
	defer removeSnapshot(snapshot)
	if err := exportCommit(ctx, spec.Repository, spec.Commit, snapshot); err != nil {
		return report, err
	}
	canonicalSnapshot, err := workspace.Canonical(snapshot)
	if err != nil {
		return report, err
	}
	permission := snapshotPermission{root: canonicalSnapshot}
	definitions := []tools.Definition{}
	for _, definition := range tools.Builtins(snapshot, nil, nil).List() {
		switch definition.Name {
		case "list_files", "grep_files", "read_file", "write_file", "modify_file", "edit_file", "patch_file":
			definitions = append(definitions, definition)
		}
	}
	registry := tools.NewRegistry(definitions, tools.Metadata{})
	adapter, err := factory(registry)
	if err != nil {
		return report, err
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	_, err = agent.RunTurn(runCtx, agent.Args{
		Model: adapter, Tools: registry, CWD: snapshot, Permission: permission, MaxSteps: 40, ContextWindowTokens: 32768, MaxOutputTokens: 2048,
		CurrentUserPrompt: spec.Prompt,
		Messages:          []message.Message{message.SystemMessage("Complete the repository task in this disposable snapshot. Only scoped file tools are available. Do not claim a change is complete without inspecting it."), message.UserMessage(spec.Prompt)},
		OnToolStart: func(name string, _ any) {
			report.ToolNames = append(report.ToolNames, name)
			if len(report.ToolNames) >= 80 {
				cancel()
			}
		},
		OnUsage: func(usage message.TokenUsage) {
			amount := usage.TotalTokens
			if amount == 0 {
				amount = usage.InputTokens + usage.OutputTokens
			}
			report.Tokens += amount
			if report.Tokens > 100000 {
				cancel()
			}
		},
	})
	if err != nil {
		return report, err
	}
	if report.Tokens > 100000 {
		return report, fmt.Errorf("evaluation token budget exhausted")
	}
	paths := make([]string, 0, len(spec.ExpectedFiles))
	for path := range spec.ExpectedFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	passed := true
	for _, relative := range paths {
		full := filepath.Join(snapshot, filepath.FromSlash(relative))
		data, err := os.ReadFile(full)
		want := spec.ExpectedFiles[relative]
		if want == "absent" {
			if !os.IsNotExist(err) {
				passed = false
				report.Checks = append(report.Checks, relative+": expected absent")
			}
			continue
		}
		if err != nil {
			passed = false
			report.Checks = append(report.Checks, relative+": missing or unreadable")
			continue
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != strings.ToLower(want) {
			passed = false
			report.Checks = append(report.Checks, relative+": digest mismatch")
		}
	}
	report.Passed = passed
	report.DurationMS = time.Since(started).Milliseconds()
	return report, nil
}

type snapshotPermission struct{ root string }

func (p snapshotPermission) EnsurePathAccess(_ context.Context, target, _ string) error {
	canonical, err := workspace.Canonical(target)
	if err != nil || !workspace.Within(p.root, canonical) {
		return fmt.Errorf("evaluation path leaves disposable snapshot")
	}
	return nil
}
func (snapshotPermission) EnsureCommand(context.Context, string, []string, string) error {
	return fmt.Errorf("evaluation commands are disabled")
}
func (p snapshotPermission) EnsureEdit(ctx context.Context, target, _ string) error {
	return p.EnsurePathAccess(ctx, target, "edit")
}

func validRelative(path string) error {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") || filepath.ToSlash(filepath.Clean(path)) != path || path == ".." || strings.HasPrefix(path, "../") || workspace.Protected(path) {
		return fmt.Errorf("unsafe evaluation path %q", path)
	}
	return nil
}

func exportCommit(ctx context.Context, repo, commit, destination string) error {
	if repo == "" || !commitPattern.MatchString(commit) {
		return fmt.Errorf("evaluation requires a pinned commit")
	}
	command := exec.CommandContext(ctx, "git", "-C", repo, "archive", "--format=tar", commit)
	pipe, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(pipe, maxSnapshotBytes+1))
	if readErr != nil || len(data) > maxSnapshotBytes {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("repository snapshot exceeds limit or cannot be read")
	}
	if err := command.Wait(); err != nil {
		return fmt.Errorf("git archive failed: %s", strings.TrimSpace(stderr.String()))
	}
	reader := tar.NewReader(bytes.NewReader(data))
	total := int64(0)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if err := validRelative(name); err != nil {
			return err
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if !workspace.Within(destination, target) {
			return fmt.Errorf("archive path escapes snapshot")
		}
		switch header.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			total += header.Size
			if header.Size < 0 || header.Size > 2<<20 || total > maxSnapshotBytes {
				return fmt.Errorf("repository file exceeds snapshot limit")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("repository snapshot contains a link or special file (%q, type %d)", name, header.Typeflag)
		}
	}
	return nil
}

func LoadSpec(path string) (Spec, error) {
	var spec Spec
	data, err := os.ReadFile(path)
	if err != nil {
		return spec, err
	}
	if len(data) > 1<<20 {
		return spec, fmt.Errorf("evaluation manifest exceeds 1 MiB")
	}
	err = json.Unmarshal(data, &spec)
	return spec, err
}

func removeSnapshot(path string) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return
	}
	tempRoot, err := filepath.Abs(os.TempDir())
	if err != nil {
		return
	}
	if filepath.Dir(absolute) != tempRoot || !strings.HasPrefix(filepath.Base(absolute), "mycode-eval-") {
		return
	}
	_ = os.RemoveAll(absolute)
}
