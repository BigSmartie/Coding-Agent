package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/credentials"
	"github.com/BigSmartie/Coding-Agent/internal/workspace"
)

type CredentialRef struct {
	ID     string `json:"id"`
	Origin string `json:"origin"`
	Kind   string `json:"kind"` // api_key or auth_token
}

// CredentialOrigin canonicalizes the HTTPS origin independently of API path prefixes.
func CredentialOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("provider endpoint must be an absolute HTTPS URL without userinfo, query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && port != "443" {
		host += ":" + port
	}
	return "https://" + host, nil
}

func resolveCredential(provider, baseURL, authToken, apiKey string, env map[string]string, refs map[string]CredentialRef, store credentials.Store) (string, string, error) {
	origin, err := CredentialOrigin(baseURL)
	if err != nil {
		return "", "", err
	}
	if authToken != "" || apiKey != "" {
		// Process/global legacy BYOK is trusted only for the official origin unless
		// the user explicitly binds it via PROVIDER_CREDENTIAL_ORIGIN.
		prefix := "ANTHROPIC_"
		if provider == "openai" {
			prefix = "OPENAI_"
		}
		bound := firstNonEmpty(env[prefix+"CREDENTIAL_ORIGIN"], defaultBaseURL(provider))
		boundOrigin, err := CredentialOrigin(bound)
		if err != nil || boundOrigin != origin {
			return "", "", fmt.Errorf("credential origin mismatch: bind %sCREDENTIAL_ORIGIN to the intended HTTPS origin, or run mycode auth login", prefix)
		}
		return authToken, apiKey, nil
	}
	ref, ok := refs[provider]
	if !ok {
		return "", "", nil
	}
	boundOrigin, err := CredentialOrigin(ref.Origin)
	if err != nil || boundOrigin != origin {
		return "", "", errors.New("stored credential is bound to another origin; run mycode auth login for this endpoint")
	}
	if ref.ID == "" || (ref.Kind != "api_key" && ref.Kind != "auth_token") {
		return "", "", errors.New("invalid stored credential reference")
	}
	secret, err := store.Get(ref.ID)
	if err != nil {
		return "", "", err
	}
	if ref.Kind == "auth_token" {
		return secret, "", nil
	}
	return "", secret, nil
}

func validateProjectSettings(settings Settings) error {
	if settings.Provider != "" || len(settings.Env) != 0 || len(settings.Credentials) != 0 || settings.ContextWindowTokens != 0 || settings.WebSearchEndpoint != "" || settings.PromptCaching || settings.Pricing != nil {
		return errors.New("project settings may contain model, maxOutputTokens and MCP servers only; move provider, env, credentials, contextWindowTokens, webSearchEndpoint, promptCaching and pricing to user configuration")
	}
	return nil
}

func validateUserConfigLocation(cwd string) error {
	root, err := workspace.Canonical(cwd)
	if err != nil {
		return err
	}
	for _, directory := range []string{AppDir(), filepath.Dir(ClaudeSettingsPath())} {
		resolved, err := workspace.Canonical(directory)
		if err != nil {
			return err
		}
		if workspace.Within(root, resolved) {
			return errors.New("user configuration directory is inside the workspace; unset MY_CODE_HOME or choose user settings and Claude compatibility settings outside the workspace")
		}
	}
	return nil
}

func secretEnvName(name string) bool {
	u := strings.ToUpper(name)
	return strings.Contains(u, "API_KEY") || strings.HasSuffix(u, "TOKEN") || strings.HasSuffix(u, "PASSWORD") || strings.HasSuffix(u, "SECRET")
}

func containsSecrets(settings Settings) bool {
	for key, value := range settings.Env {
		if secretEnvName(key) && stringify(value) != "" {
			return true
		}
	}
	for _, server := range settings.MCPServers {
		for key, value := range server.Env {
			if secretEnvName(key) && stringify(value) != "" {
				return true
			}
		}
	}
	return false
}

// SaveCredential writes the secret first; settings hold only a random reference and origin.
func SaveCredential(provider, baseURL, kind, secret string, store credentials.Store) (CredentialRef, error) {
	if provider != "openai" && provider != "anthropic" {
		return CredentialRef{}, errors.New("provider must be openai or anthropic")
	}
	if kind != "api_key" && kind != "auth_token" {
		return CredentialRef{}, errors.New("credential kind must be api_key or auth_token")
	}
	origin, err := CredentialOrigin(baseURL)
	if err != nil {
		return CredentialRef{}, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return CredentialRef{}, err
	}
	ref := CredentialRef{ID: provider + "/" + hex.EncodeToString(id[:]), Origin: origin, Kind: kind}
	if err := store.Set(ref.ID, secret); err != nil {
		return CredentialRef{}, err
	}
	return ref, nil
}

// MigrateCredentials explicitly migrates this app's user settings only. It never
// reads a project or another application's settings and leaves files unchanged
// when the OS store is unavailable or any legacy credential is ambiguous.
func MigrateCredentials(store credentials.Store) error {
	settings, err := readSettings(SettingsPath())
	if err != nil {
		return err
	}
	settings, created, err := migrateSettingsCredentials(settings, store)
	if err != nil {
		return err
	}
	if err := writeJSON(SettingsPath(), settings); err != nil {
		for _, ref := range created {
			_ = store.Delete(ref.ID)
		}
		return err
	}
	return nil
}

func migrateSettingsCredentials(settings Settings, store credentials.Store) (Settings, []CredentialRef, error) {
	for key, value := range settings.Env {
		if secretEnvName(key) && stringify(value) != "" && key != "OPENAI_API_KEY" && key != "OPENAI_AUTH_TOKEN" && key != "ANTHROPIC_API_KEY" && key != "ANTHROPIC_AUTH_TOKEN" {
			return Settings{}, nil, errors.New("settings contain additional plaintext secrets; migrate these separately before saving")
		}
	}
	if settings.Credentials == nil {
		settings.Credentials = map[string]CredentialRef{}
	}
	created := []CredentialRef{}
	committed := false
	defer func() {
		if !committed {
			for _, ref := range created {
				_ = store.Delete(ref.ID)
			}
		}
	}()
	for _, provider := range []string{"openai", "anthropic"} {
		prefix := strings.ToUpper(provider) + "_"
		apiKey, token := stringify(settings.Env[prefix+"API_KEY"]), stringify(settings.Env[prefix+"AUTH_TOKEN"])
		if apiKey != "" && token != "" {
			return Settings{}, nil, errors.New("both API key and auth token configured for one provider; select one before migration")
		}
		if apiKey == "" && token == "" {
			continue
		}
		if _, exists := settings.Credentials[provider]; exists {
			return Settings{}, nil, errors.New("both an OS credential reference and a plaintext credential are configured; resolve this conflict before migration")
		}
		kind, secret := "api_key", apiKey
		if token != "" {
			kind, secret = "auth_token", token
		}
		baseURL := firstNonEmpty(stringify(settings.Env[prefix+"BASE_URL"]), defaultBaseURL(provider))
		ref, err := SaveCredential(provider, baseURL, kind, secret, store)
		if err != nil {
			return Settings{}, nil, err
		}
		created = append(created, ref)
		settings.Credentials[provider] = ref
		delete(settings.Env, prefix+"API_KEY")
		delete(settings.Env, prefix+"AUTH_TOKEN")
	}
	if containsSecrets(settings) {
		return Settings{}, nil, errors.New("MCP plaintext secrets require separate migration")
	}
	committed = true
	return settings, created, nil
}

// MigrateProjectCredentials is an explicit CLI-only operation. The secure user
// configuration is committed before removing secrets from the project file.
// Existing user provider settings cause a conflict instead of being overwritten.
func MigrateProjectCredentials(cwd string, store credentials.Store) error {
	if err := validateUserConfigLocation(cwd); err != nil {
		return err
	}
	user, err := readSettings(SettingsPath())
	if err != nil {
		return err
	}
	if user.Provider != "" || user.Model != "" || len(user.Env) != 0 || len(user.Credentials) != 0 {
		return errors.New("user provider settings already exist; project migration will not overwrite them; choose an empty MY_CODE_HOME outside the workspace or resolve the conflict manually")
	}
	project, err := readSettings(ProjectSettingsPath(cwd))
	if err != nil {
		return err
	}
	if project.Provider == "" && len(project.Env) == 0 && len(project.Credentials) == 0 {
		return errors.New("no legacy provider settings found in the project")
	}
	if len(project.Credentials) != 0 {
		return errors.New("project credential references cannot be imported; use mycode auth login")
	}
	if project.Provider != "" && project.Provider != "openai" && project.Provider != "anthropic" {
		return errors.New("project provider must be openai or anthropic before migration")
	}
	for name := range project.Env {
		if !migratableProviderEnv(name) {
			return errors.New("project env contains unsupported settings; migration only imports provider credentials and model options")
		}
	}
	for _, provider := range []string{"openai", "anthropic"} {
		if raw := stringify(project.Env[strings.ToUpper(provider)+"_BASE_URL"]); raw != "" {
			if _, err := CredentialOrigin(raw); err != nil {
				return err
			}
		}
	}
	var projectJSON map[string]any
	if err := readJSON(ProjectSettingsPath(cwd), &projectJSON); err != nil {
		return err
	}
	user.Provider, user.Model = project.Provider, project.Model
	user.Env = project.Env
	if project.MaxOutputTokens != 0 {
		user.MaxOutputTokens = project.MaxOutputTokens
	}
	user, created, err := migrateSettingsCredentials(user, store)
	if err != nil {
		return err
	}
	if err := writeJSON(SettingsPath(), user); err != nil {
		for _, ref := range created {
			_ = store.Delete(ref.ID)
		}
		return err
	}
	delete(projectJSON, "provider")
	delete(projectJSON, "env")
	delete(projectJSON, "credentials")
	if err := writeJSON(ProjectSettingsPath(cwd), projectJSON); err != nil {
		return errors.New("credentials are safely saved in user configuration, but project cleanup failed; remove provider, env and credentials fields from project settings before continuing")
	}
	return nil
}

func migratableProviderEnv(name string) bool {
	switch name {
	case "OPENAI_API_KEY", "OPENAI_AUTH_TOKEN", "OPENAI_BASE_URL", "OPENAI_MODEL", "OPENAI_WIRE_API", "OPENAI_CREDENTIAL_ORIGIN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_CREDENTIAL_ORIGIN", "MY_CODE_PROVIDER", "MY_CODE_MODEL", "MY_CODE_REASONING_EFFORT", "MY_CODE_DISABLE_RESPONSE_STORAGE", "MY_CODE_MAX_OUTPUT_TOKENS":
		return true
	default:
		return false
	}
}

func LoadUserSettings() (Settings, error) { return readSettings(SettingsPath()) }
