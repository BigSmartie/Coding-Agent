package manage

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/brand"
	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/session"
	"github.com/BigSmartie/Coding-Agent/internal/testutil"
)

func TestHelpCommand(t *testing.T) {
	out, handled, err := Handle(context.Background(), t.TempDir(), []string{"help"})
	if err != nil {
		t.Fatal(err)
	}
	if !handled || !strings.Contains(out, "mycode management commands") {
		t.Fatalf("unexpected result: %q handled=%v", out, handled)
	}
}

func TestSkillsList(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	testutil.IsolateEnv(t, home)
	root := filepath.Join(home, brand.ConfigDirName, "skills", "demo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("# Demo\n\nDemo skill."), 0o644); err != nil {
		t.Fatal(err)
	}

	out, handled, err := Handle(context.Background(), cwd, []string{"skills", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !handled || !strings.Contains(out, "demo: Demo skill.") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestMCPAddParsesProtocolAndEnv(t *testing.T) {
	cwd := t.TempDir()
	_, handled, err := Handle(context.Background(), cwd, []string{
		"mcp", "add", "fs", "--project", "--protocol", "newline-json", "--env", "A=B", "--", "npx", "server",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected command handled")
	}

	servers, err := config.ReadMCPConfig(filepath.Join(cwd, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	server := servers["fs"]
	if server.Protocol != "newline-json" || server.Env["A"] != "B" {
		t.Fatalf("unexpected server: %#v", server)
	}
}

func TestMCPAddURLRejectsEmbeddedCredentials(t *testing.T) {
	cwd := t.TempDir()
	out, handled, err := Handle(context.Background(), cwd, []string{"mcp", "add-url", "remote", "https://example.com/mcp", "--project"})
	if err != nil || !handled || !strings.Contains(out, "trust mcp remote") {
		t.Fatalf("remote MCP setup failed: %q, %v", out, err)
	}
	servers, err := config.ReadMCPConfig(filepath.Join(cwd, ".mcp.json"))
	if err != nil || servers["remote"].URL != "https://example.com/mcp" {
		t.Fatalf("remote URL was not saved: %#v, %v", servers, err)
	}
	for _, unsafe := range []string{"http://example.com/mcp", "https://user:secret@example.com/mcp", "https://example.com/mcp?token=secret"} {
		if _, _, err := Handle(context.Background(), cwd, []string{"mcp", "add-url", "bad", unsafe, "--project"}); err == nil {
			t.Fatalf("unsafe URL accepted: %s", unsafe)
		}
	}
}

func TestInstallLocalCommand(t *testing.T) {
	home := t.TempDir()
	testutil.IsolateEnv(t, home)
	cwd := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "key")
	t.Setenv("OPENAI_CREDENTIAL_ORIGIN", "https://openai.example.test")
	out, handled, err := Handle(context.Background(), cwd, []string{
		"install-local",
		"--skip-build",
		"--provider", "openai",
		"--model", "gpt-test",
		"--base-url", "https://openai.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected command handled")
	}
	for _, want := range []string{"Installed MyCode", filepath.Join(home, ".local", "bin", brand.LauncherName), "PATH"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", brand.LauncherName)); err != nil {
		t.Fatal(err)
	}
	runtime, err := config.LoadRuntime(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Provider != "openai" || runtime.Model != "gpt-test" || runtime.BaseURL != "https://openai.example.test" || runtime.APIKey != "key" {
		t.Fatalf("settings not written from install-local: %#v", runtime)
	}
}

func TestInstallLocalInteractivePromptsForSettings(t *testing.T) {
	home := t.TempDir()
	testutil.IsolateEnv(t, home)
	cwd := t.TempDir()
	store := &fakeCredentialStore{values: map[string]string{}}
	previousStore, previousInput := credentialStore, readSecretInput
	credentialStore = store
	readSecretInput = func(_ io.Reader, reader *bufio.Reader) (string, error) { return readLine(reader) }
	t.Cleanup(func() { credentialStore, readSecretInput = previousStore, previousInput })
	input := strings.NewReader("\nclaude-interactive\nhttps://anthropic.interactive.test\nsecret-token\n")
	var prompts bytes.Buffer

	out, handled, err := handleInstallLocalWithIO(context.Background(), cwd, []string{"--skip-build"}, input, &prompts, true)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected command handled")
	}
	if !strings.Contains(prompts.String(), "mycode installer") || !strings.Contains(out, "settings: "+config.SettingsPath()) {
		t.Fatalf("expected installer prompts and settings output, prompts=%q out=%q", prompts.String(), out)
	}
	runtime, err := config.LoadRuntimeWithStore(cwd, store)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Provider != "anthropic" || runtime.Model != "claude-interactive" || runtime.BaseURL != "https://anthropic.interactive.test" || runtime.AuthToken != "secret-token" {
		t.Fatalf("interactive settings not written: %#v", runtime)
	}
}

func TestInstallLocalFlagOrderDoesNotChangeProviderEnvNames(t *testing.T) {
	home := t.TempDir()
	testutil.IsolateEnv(t, home)
	cwd := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "key")
	t.Setenv("OPENAI_CREDENTIAL_ORIGIN", "https://openai.order.test")

	_, handled, err := Handle(context.Background(), cwd, []string{
		"install-local",
		"--skip-build",
		"--base-url", "https://openai.order.test",
		"--provider", "openai",
		"--model", "gpt-order",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected command handled")
	}
	runtime, err := config.LoadRuntime(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Provider != "openai" || runtime.BaseURL != "https://openai.order.test" || runtime.APIKey != "key" {
		t.Fatalf("provider env names depended on flag order: %#v", runtime)
	}
}

type fakeCredentialStore struct{ values map[string]string }

func (f *fakeCredentialStore) Get(id string) (string, error) { return f.values[id], nil }
func (f *fakeCredentialStore) Set(id, value string) error    { f.values[id] = value; return nil }
func (f *fakeCredentialStore) Delete(id string) error        { delete(f.values, id); return nil }

func TestCredentialFlagsRejectedWithoutEchoingValues(t *testing.T) {
	for _, args := range [][]string{{"--api-key", "highly-sensitive"}, {"--auth-token", "highly-sensitive"}, {"--api-key=highly-sensitive"}, {"--auth-token=highly-sensitive"}} {
		_, err := parseInstallArgs(args)
		if err == nil || strings.Contains(err.Error(), "highly-sensitive") {
			t.Fatalf("credential flag rejection leaked or accepted a secret: %v", err)
		}
	}
}

func TestHiddenSecretInputRefusesUnprotectedPipes(t *testing.T) {
	input := strings.NewReader("secret\n")
	if _, err := hiddenSecretInput(input, bufio.NewReader(input)); err == nil {
		t.Fatal("unprotected input accepted")
	}
}

func TestSessionsListCommand(t *testing.T) {
	home := t.TempDir()
	testutil.IsolateEnv(t, home)
	store := session.Store{Dir: config.SessionsDir()}
	if err := store.Save(session.Record{ID: "session-1", CWD: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}

	out, handled, err := Handle(context.Background(), t.TempDir(), []string{"sessions", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !handled || !strings.Contains(out, "session-1") || !strings.Contains(out, "/tmp/project") {
		t.Fatalf("unexpected sessions list: %q handled=%v", out, handled)
	}
}
