package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/testutil"
)

func TestUsageContainsManagementCommands(t *testing.T) {
	if usage() == "" {
		t.Fatal("usage should not be empty")
	}
}

func TestParseStartupArgsResume(t *testing.T) {
	parsed, err := parseStartupArgs([]string{"--resume", "latest"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ResumeID != "latest" || len(parsed.ManagementArgs) != 0 {
		t.Fatalf("unexpected parsed args: %#v", parsed)
	}
	parsed, err = parseStartupArgs([]string{"--recover-interrupted", "--resume", "session-1"})
	if err != nil || !parsed.RecoverInterrupted || parsed.ResumeID != "session-1" {
		t.Fatalf("unexpected recovery args: %#v, %v", parsed, err)
	}
	if _, err := parseStartupArgs([]string{"--recover-interrupted"}); err == nil {
		t.Fatal("recovery flag without session was accepted")
	}

	parsed, err = parseStartupArgs([]string{"sessions", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ResumeID != "" || len(parsed.ManagementArgs) != 2 {
		t.Fatalf("unexpected management args: %#v", parsed)
	}
}

func TestRunWithoutRuntimeConfigDoesNotImplicitlyUseMock(t *testing.T) {
	home := t.TempDir()
	testutil.IsolateEnv(t, home)
	t.Setenv("HOME", home)
	t.Setenv("MYTHOS_CODE_HOME", filepath.Join(home, ".mythos-code"))
	t.Setenv("MYTHOS_CODE_MODEL_MODE", "")
	t.Setenv("MYTHOS_CODE_PROVIDER", "")
	t.Setenv("MYTHOS_CODE_MODEL", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("OPENAI_API_KEY", "")

	stdin := os.Stdin
	stdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write.WriteString("hello\n/exit\n"); err != nil {
		t.Fatal(err)
	}
	_ = write.Close()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = read
	os.Stdout = outWrite
	defer func() {
		os.Stdin = stdin
		os.Stdout = stdout
	}()

	err = run(context.Background(), nil)
	_ = outWrite.Close()
	var out bytes.Buffer
	_, _ = io.Copy(&out, outRead)
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "model: not-configured") || strings.Contains(got, "Mock mode response") || !strings.Contains(got, "request failed: No model configured") {
		t.Fatalf("unexpected no-config behavior:\n%s", got)
	}
}
