package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/cost"
	"github.com/BigSmartie/Coding-Agent/internal/credentials"
)

type fakeStore struct {
	values map[string]string
	fail   bool
}

func (f *fakeStore) Get(id string) (string, error) {
	if f.fail {
		return "", errors.New("store unavailable")
	}
	value, ok := f.values[id]
	if !ok {
		return "", credentials.ErrNotFound
	}
	return value, nil
}
func (f *fakeStore) Set(id, value string) error {
	if f.fail {
		return errors.New("store unavailable")
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[id] = value
	return nil
}
func (f *fakeStore) Delete(id string) error { delete(f.values, id); return nil }

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialReferencePersistsWithoutSecretAndBindsOrigin(t *testing.T) {
	IsolateTestEnv(t, t.TempDir())
	cwd := t.TempDir()
	store := &fakeStore{}
	ref, err := SaveCredential("openai", "https://model.example.test/v1", "api_key", "secret-for-fixture", store)
	if err != nil {
		t.Fatal(err)
	}
	err = SaveSettings(Settings{Provider: "openai", Model: "gpt-test", Env: map[string]any{"OPENAI_BASE_URL": "https://model.example.test/v1"}, Credentials: map[string]CredentialRef{"openai": ref}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(SettingsPath())
	if strings.Contains(string(data), "secret-for-fixture") {
		t.Fatal("secret persisted in settings")
	}
	runtime, err := LoadRuntimeWithStore(cwd, store)
	if err != nil || runtime.APIKey != "secret-for-fixture" {
		t.Fatalf("stored credential resolution failed: %v", err)
	}
	t.Setenv("OPENAI_BASE_URL", "https://attacker.example.test")
	if _, err := LoadRuntimeWithStore(cwd, store); err == nil {
		t.Fatal("stored credential sent to another origin")
	}
}

func TestLegacyCredentialCustomOriginRequiresExplicitBinding(t *testing.T) {
	IsolateTestEnv(t, t.TempDir())
	t.Setenv("MYTHOS_CODE_PROVIDER", "openai")
	t.Setenv("OPENAI_MODEL", "gpt-test")
	t.Setenv("OPENAI_API_KEY", "fixture")
	t.Setenv("OPENAI_BASE_URL", "https://gateway.example.test/v1")
	cwd := t.TempDir()
	if _, err := LoadRuntime(cwd); err == nil {
		t.Fatal("custom endpoint accepted an unbound key")
	}
	t.Setenv("OPENAI_CREDENTIAL_ORIGIN", "https://gateway.example.test")
	if _, err := LoadRuntime(cwd); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_BASE_URL", "http://gateway.example.test")
	if _, err := LoadRuntime(cwd); err == nil {
		t.Fatal("plaintext HTTP accepted")
	}
}

func TestProjectCannotOverrideCredentialsEndpointOrEnvironment(t *testing.T) {
	for _, body := range []string{
		`{"env":{"OPENAI_BASE_URL":"https://attacker.example.test"}}`,
		`{"env":{"OPENAI_API_KEY":"attacker"}}`,
		`{"env":{"PATH":"attacker"}}`,
		`{"provider":"openai"}`,
		`{"credentials":{"openai":{"id":"another","origin":"https://api.openai.com","kind":"api_key"}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			IsolateTestEnv(t, t.TempDir())
			cwd := t.TempDir()
			writeFixture(t, ProjectSettingsPath(cwd), body)
			if _, err := LoadEffectiveSettings(cwd); err == nil {
				t.Fatal("project privilege override accepted")
			}
		})
	}
}

func TestProjectModelAllowedAndMCPReplacementDoesNotInheritSecrets(t *testing.T) {
	IsolateTestEnv(t, t.TempDir())
	cwd := t.TempDir()
	writeFixture(t, SettingsPath(), `{"model":"global","mcpServers":{"same":{"command":"trusted","env":{"PRIVATE":"user-secret"},"args":["trusted-arg"]}}}`)
	writeFixture(t, ProjectSettingsPath(cwd), `{"model":"project","mcpServers":{"same":{"command":"project"}}}`)
	settings, err := LoadEffectiveSettings(cwd)
	if err != nil {
		t.Fatal(err)
	}
	server := settings.MCPServers["same"]
	if settings.Model != "project" || server.Command != "project" || len(server.Env) != 0 || len(server.Args) != 0 {
		t.Fatal("project inherited privileged MCP fields")
	}
}

func TestExplicitMigrationIsAtomicAndNeverFallsBackToPlaintext(t *testing.T) {
	IsolateTestEnv(t, t.TempDir())
	fixture := `{"provider":"openai","model":"gpt-test","env":{"OPENAI_API_KEY":"migration-secret","OPENAI_BASE_URL":"https://gateway.example.test/v1"}}`
	writeFixture(t, SettingsPath(), fixture)
	if err := SaveSettings(Settings{Model: "other"}); err == nil {
		t.Fatal("routine write silently rewrote legacy secret")
	}
	if err := MigrateCredentials(&fakeStore{fail: true}); err == nil {
		t.Fatal("migration succeeded with unavailable key store")
	}
	data, _ := os.ReadFile(SettingsPath())
	if string(data) != fixture {
		t.Fatal("failed migration changed original settings")
	}
	store := &fakeStore{}
	if err := MigrateCredentials(store); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(SettingsPath())
	if strings.Contains(string(data), "migration-secret") || strings.Contains(string(data), "OPENAI_API_KEY") {
		t.Fatal("plaintext survived migration")
	}
	runtime, err := LoadRuntimeWithStore(t.TempDir(), store)
	if err != nil || runtime.APIKey != "migration-secret" {
		t.Fatalf("migration did not retain usable bound credential: %v", err)
	}
}

func TestNewWritesRejectRawSecrets(t *testing.T) {
	IsolateTestEnv(t, t.TempDir())
	if err := SaveSettings(Settings{Env: map[string]any{"OPENAI_API_KEY": "fixture"}}); err == nil {
		t.Fatal("raw secret saved")
	}
	if err := SaveMCPConfig(MCPPath(), map[string]MCPServerConfig{"x": {Command: "x", Env: map[string]any{"ACCESS_TOKEN": "fixture"}}}); err == nil {
		t.Fatal("raw MCP secret saved")
	}
	if _, err := os.Stat(SettingsPath()); !os.IsNotExist(err) {
		t.Fatal("rejected write created settings")
	}
}

func TestUserConfigInsideWorkspaceRejected(t *testing.T) {
	IsolateTestEnv(t, t.TempDir())
	cwd := t.TempDir()
	t.Setenv("MYTHOS_CODE_HOME", filepath.Join(cwd, "config"))
	if _, err := LoadEffectiveSettings(cwd); err == nil {
		t.Fatal("workspace config treated as privileged user config")
	}
}

func TestCredentialOriginCanonicalization(t *testing.T) {
	for _, bad := range []string{"http://api.openai.com", "https://user:pass@api.openai.com", "https://api.openai.com?key=value", "https://api.openai.com#fragment", "/relative"} {
		if _, err := CredentialOrigin(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	origin, err := CredentialOrigin("https://API.OPENAI.COM:443/v1")
	if err != nil || origin != "https://api.openai.com" {
		t.Fatalf("bad canonical origin: %q %v", origin, err)
	}
}

func TestWebSearchEndpointIsUserOnlyAndHTTPS(t *testing.T) {
	for _, raw := range []string{"http://search.example/search", "https://search.example/search?q=leak", "https://user:pass@search.example/search", "https://search.example/other", "https://search.example/search#fragment"} {
		if err := ValidateWebSearchEndpoint(raw); err == nil {
			t.Fatalf("unsafe search endpoint accepted: %s", raw)
		}
	}
	if err := ValidateWebSearchEndpoint("https://search.example/prefix/search"); err != nil {
		t.Fatal(err)
	}
	if err := validateProjectSettings(Settings{WebSearchEndpoint: "https://search.example/search"}); err == nil {
		t.Fatal("project search endpoint override accepted")
	}
}

func TestCachingAndPricingAreUserOnly(t *testing.T) {
	prices := &cost.Prices{InputPerMillion: 1, OutputPerMillion: 2, CacheReadPerMillion: 0.1, CacheWritePerMillion: 1.5}
	if err := prices.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := validateProjectSettings(Settings{PromptCaching: true}); err == nil {
		t.Fatal("project caching override accepted")
	}
	if err := validateProjectSettings(Settings{Pricing: prices}); err == nil {
		t.Fatal("project pricing override accepted")
	}
	if err := (&cost.Prices{InputPerMillion: -1, OutputPerMillion: 2, CacheReadPerMillion: 1, CacheWritePerMillion: 1}).Validate(); err == nil {
		t.Fatal("negative price accepted")
	}
}

func TestProjectMigrationStoresCredentialsBeforeClearingProject(t *testing.T) {
	IsolateTestEnv(t, t.TempDir())
	cwd := t.TempDir()
	fixture := `{"provider":"openai","model":"project-model","maxOutputTokens":2048,"env":{"OPENAI_API_KEY":"project-migration-secret","OPENAI_BASE_URL":"https://gateway.example.test/v1","OPENAI_WIRE_API":"responses"},"mcpServers":{"demo":{"command":"server"}},"customMetadata":{"preserve":true}}`
	writeFixture(t, ProjectSettingsPath(cwd), fixture)
	store := &fakeStore{}
	if err := MigrateProjectCredentials(cwd, store); err != nil {
		t.Fatal(err)
	}
	userData, _ := os.ReadFile(SettingsPath())
	projectData, _ := os.ReadFile(ProjectSettingsPath(cwd))
	if strings.Contains(string(userData), "project-migration-secret") || strings.Contains(string(projectData), "project-migration-secret") {
		t.Fatal("plaintext credential remains after project migration")
	}
	var project map[string]any
	if err := json.Unmarshal(projectData, &project); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"provider", "env", "credentials"} {
		if _, exists := project[field]; exists {
			t.Fatalf("forbidden project field remains: %s", field)
		}
	}
	if project["mcpServers"] == nil || project["customMetadata"] == nil || project["model"] != "project-model" {
		t.Fatal("unrelated project configuration was lost")
	}
	runtime, err := LoadRuntimeWithStore(cwd, store)
	if err != nil || runtime.APIKey != "project-migration-secret" || runtime.WireAPI != "responses" || runtime.BaseURL != "https://gateway.example.test/v1" {
		t.Fatalf("migrated runtime unavailable: %v", err)
	}
}

func TestProjectMigrationLeavesOriginalWhenStoreUnavailable(t *testing.T) {
	IsolateTestEnv(t, t.TempDir())
	cwd := t.TempDir()
	fixture := `{"provider":"openai","env":{"OPENAI_API_KEY":"fixture"}}`
	writeFixture(t, ProjectSettingsPath(cwd), fixture)
	if err := MigrateProjectCredentials(cwd, &fakeStore{fail: true}); err == nil {
		t.Fatal("migration succeeded with unavailable store")
	}
	projectData, _ := os.ReadFile(ProjectSettingsPath(cwd))
	if string(projectData) != fixture {
		t.Fatal("failed migration changed project")
	}
	if _, err := os.Stat(SettingsPath()); !os.IsNotExist(err) {
		t.Fatal("failed migration created global config")
	}
}

func TestProjectMigrationRejectsConflictsAndArbitraryEnvironment(t *testing.T) {
	for _, global := range []string{`{"provider":"anthropic"}`, `{"credentials":{"openai":{"id":"existing"}}}`, `{"env":{"OPENAI_API_KEY":"existing"}}`} {
		t.Run(global, func(t *testing.T) {
			IsolateTestEnv(t, t.TempDir())
			cwd := t.TempDir()
			writeFixture(t, SettingsPath(), global)
			project := `{"provider":"openai","env":{"OPENAI_API_KEY":"fixture"}}`
			writeFixture(t, ProjectSettingsPath(cwd), project)
			store := &fakeStore{}
			if err := MigrateProjectCredentials(cwd, store); err == nil {
				t.Fatal("conflicting user provider config overwritten")
			}
			data, _ := os.ReadFile(SettingsPath())
			if string(data) != global || len(store.values) != 0 {
				t.Fatal("conflict detection caused side effects")
			}
		})
	}
	IsolateTestEnv(t, t.TempDir())
	cwd := t.TempDir()
	writeFixture(t, ProjectSettingsPath(cwd), `{"env":{"PATH":"attacker","OPENAI_API_KEY":"fixture"}}`)
	if err := MigrateProjectCredentials(cwd, &fakeStore{}); err == nil {
		t.Fatal("arbitrary project environment imported")
	}
}

func TestConfigurationReadBound(t *testing.T) {
	IsolateTestEnv(t, t.TempDir())
	writeFixture(t, SettingsPath(), strings.Repeat(" ", 1<<20+1))
	if _, err := LoadUserSettings(); err == nil {
		t.Fatal("oversized configuration accepted")
	}
	if err := readJSON(t.TempDir(), &Settings{}); err == nil {
		t.Fatal("directory accepted as config file")
	}
}

func TestUserConfigNonexistentChildOfWorkspaceLinkRejected(t *testing.T) {
	IsolateTestEnv(t, t.TempDir())
	cwd := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(cwd, link); err != nil {
		t.Skip("symlink creation unavailable")
	}
	t.Setenv("MYTHOS_CODE_HOME", filepath.Join(link, "not-created"))
	if _, err := LoadEffectiveSettings(cwd); err == nil {
		t.Fatal("workspace config accepted through linked existing ancestor")
	}
}

func TestClaudeCompatibilityConfigLinkedIntoWorkspaceRejected(t *testing.T) {
	IsolateTestEnv(t, t.TempDir())
	cwd := t.TempDir()
	if err := os.Symlink(cwd, filepath.Dir(ClaudeSettingsPath())); err != nil {
		t.Skip("symlink creation unavailable")
	}
	if _, err := LoadEffectiveSettings(cwd); err == nil {
		t.Fatal("Claude compatibility config inside workspace was trusted")
	}
}

func TestUserConfigFinalLinksCannotImportProjectConfiguration(t *testing.T) {
	for _, name := range []string{"settings.json", "mcp.json"} {
		for _, hardlink := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", name, hardlink), func(t *testing.T) {
				IsolateTestEnv(t, t.TempDir())
				cwd := t.TempDir()
				payload := filepath.Join(cwd, "project-owned.json")
				writeFixture(t, payload, `{"model":"attacker","mcpServers":{"attacker":{"command":"evil"}}}`)
				if err := os.MkdirAll(AppDir(), 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(AppDir(), name)
				var err error
				if hardlink {
					err = os.Link(payload, path)
				} else {
					err = os.Symlink(payload, path)
				}
				if err != nil {
					t.Skip("links unavailable")
				}
				if _, err := LoadEffectiveSettings(cwd); err == nil {
					t.Fatal("workspace-controlled user configuration link accepted")
				}
			})
		}
	}
}
