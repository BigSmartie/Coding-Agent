package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRuntimeConfigMergesSettingsAndEnv(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	IsolateTestEnv(t, home)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-token")
	t.Setenv("ANTHROPIC_CREDENTIAL_ORIGIN", "https://example.test")

	miniDir := AppDir()
	if err := os.MkdirAll(miniDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(miniDir, "settings.json"), []byte(`{
		"model": "claude-test",
		"env": {"ANTHROPIC_BASE_URL": "https://example.test"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".mcp.json"), []byte(`{
		"mcpServers": {"fs": {"command": "npx", "args": ["server"]}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime, err := LoadRuntime(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Model != "claude-test" || runtime.BaseURL != "https://example.test" {
		t.Fatalf("unexpected runtime: %#v", runtime)
	}
	if runtime.AuthToken != "env-token" {
		t.Fatalf("expected env auth token, got %q", runtime.AuthToken)
	}
	if runtime.MCPServers["fs"].Command != "npx" {
		t.Fatalf("project MCP config not loaded: %#v", runtime.MCPServers)
	}
}

func TestLoadRuntimeSupportsOpenAIProvider(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	IsolateTestEnv(t, home)
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("OPENAI_CREDENTIAL_ORIGIN", "https://openai.example.test")

	miniDir := AppDir()
	if err := os.MkdirAll(miniDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(miniDir, "settings.json"), []byte(`{
		"provider": "openai",
		"model": "gpt-test",
		"env": {"OPENAI_BASE_URL": "https://openai.example.test"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime, err := LoadRuntime(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Provider != "openai" || runtime.Model != "gpt-test" || runtime.BaseURL != "https://openai.example.test" || runtime.APIKey != "openai-key" {
		t.Fatalf("unexpected openai runtime: %#v", runtime)
	}
}

func TestLoadRuntimeParsesOpenAIResponsesSettings(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	IsolateTestEnv(t, home)
	t.Setenv("OPENAI_CREDENTIAL_ORIGIN", "https://api.psydo.top")

	appDir := AppDir()
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "settings.json"), []byte(`{
		"provider": "openai",
		"model": "gpt-5.5",
		"env": {
			"OPENAI_BASE_URL": "https://api.psydo.top",
			"OPENAI_API_KEY": "test-key",
			"OPENAI_WIRE_API": "responses",
			"MY_CODE_REASONING_EFFORT": "xhigh",
			"MY_CODE_DISABLE_RESPONSE_STORAGE": "true"
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime, err := LoadRuntime(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.WireAPI != "responses" || runtime.ReasoningEffort != "xhigh" || !runtime.DisableResponseStorage {
		t.Fatalf("unexpected responses runtime: %#v", runtime)
	}
}

func TestLoadRuntimePrefersProjectSettings(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	IsolateTestEnv(t, home)

	appDir := AppDir()
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "settings.json"), []byte(`{
		"provider": "anthropic",
		"model": "claude-global",
		"env": {"ANTHROPIC_AUTH_TOKEN": "global-token"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ProjectAppDir(cwd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectSettingsPath(cwd), []byte(`{
		"provider": "openai",
		"model": "gpt-5.5",
		"env": {
			"OPENAI_BASE_URL": "https://api.psydo.top",
			"OPENAI_API_KEY": "project-key",
			"OPENAI_WIRE_API": "responses"
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime, err := LoadRuntime(cwd)
	if err == nil {
		t.Fatalf("unsafe project override accepted: %#v", runtime)
	}
}
