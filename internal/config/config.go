package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/brand"
	"github.com/BigSmartie/Coding-Agent/internal/credentials"
	"github.com/BigSmartie/Coding-Agent/internal/egress"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"github.com/BigSmartie/Coding-Agent/internal/workspace"
)

type Settings struct {
	Env                 map[string]any             `json:"env,omitempty"`
	Provider            string                     `json:"provider,omitempty"`
	Model               string                     `json:"model,omitempty"`
	MaxOutputTokens     int                        `json:"maxOutputTokens,omitempty"`
	ContextWindowTokens int                        `json:"contextWindowTokens,omitempty"`
	MCPServers          map[string]MCPServerConfig `json:"mcpServers,omitempty"`
	Credentials         map[string]CredentialRef   `json:"credentials,omitempty"`
}

type MCPServerConfig struct {
	Command  string         `json:"command"`
	URL      string         `json:"url,omitempty"`
	Args     []string       `json:"args,omitempty"`
	Env      map[string]any `json:"env,omitempty"`
	CWD      string         `json:"cwd,omitempty"`
	Enabled  *bool          `json:"enabled,omitempty"`
	Protocol string         `json:"protocol,omitempty"`
}

type Runtime struct {
	Provider               string
	Model                  string
	BaseURL                string
	AuthToken              string
	APIKey                 string
	WireAPI                string
	ReasoningEffort        string
	DisableResponseStorage bool
	MaxOutputTokens        int
	ContextWindowTokens    int
	MCPServers             map[string]MCPServerConfig
	SourceSummary          string
}

func AppDir() string {
	if override := strings.TrimSpace(os.Getenv(brand.EnvName("HOME"))); override != "" {
		return override
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, brand.ConfigDirName)
}

func SettingsPath() string {
	return filepath.Join(AppDir(), "settings.json")
}

func ProjectAppDir(cwd string) string {
	return filepath.Join(cwd, brand.ConfigDirName)
}

func ProjectSettingsPath(cwd string) string {
	return filepath.Join(ProjectAppDir(cwd), "settings.json")
}

func MCPPath() string {
	return filepath.Join(AppDir(), "mcp.json")
}

func PermissionsPath() string {
	return filepath.Join(AppDir(), "permissions.json")
}

func HistoryPath() string {
	return filepath.Join(AppDir(), "history.json")
}

func SessionsDir() string {
	return filepath.Join(AppDir(), "sessions")
}

func ClaudeSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func ProjectMCPPath(cwd string) string {
	return filepath.Join(cwd, ".mcp.json")
}

func LoadRuntime(cwd string) (Runtime, error) {
	return LoadRuntimeWithStore(cwd, credentials.SystemStore{})
}

func LoadRuntimeWithStore(cwd string, store credentials.Store) (Runtime, error) {
	settings, err := LoadEffectiveSettings(cwd)
	if err != nil {
		return Runtime{}, err
	}

	env := map[string]string{}
	for key, value := range settings.Env {
		env[key] = stringify(value)
	}
	for _, item := range os.Environ() {
		for i := 0; i < len(item); i++ {
			if item[i] == '=' {
				if item[i+1:] != "" {
					env[item[:i]] = item[i+1:]
				}
				break
			}
		}
	}

	provider := normalizeProvider(firstNonEmpty(os.Getenv(brand.EnvName("PROVIDER")), env[brand.EnvName("PROVIDER")], settings.Provider, "anthropic"))
	model := firstNonEmpty(os.Getenv(brand.EnvName("MODEL")), settings.Model, providerModelEnv(provider, env))
	baseURL := firstNonEmpty(providerBaseURLEnv(provider, env), defaultBaseURL(provider))
	authToken := providerAuthTokenEnv(provider, env)
	apiKey := providerAPIKeyEnv(provider, env)
	if provider != "mock" {
		authToken, apiKey, err = resolveCredential(provider, baseURL, authToken, apiKey, env, settings.Credentials, store)
		if err != nil {
			return Runtime{}, err
		}
	}
	wireAPI := normalizeWireAPI(firstNonEmpty(env["OPENAI_WIRE_API"], os.Getenv("OPENAI_WIRE_API")))
	reasoningEffort := firstNonEmpty(os.Getenv(brand.EnvName("REASONING_EFFORT")), env[brand.EnvName("REASONING_EFFORT")])
	disableResponseStorage := parseBool(firstNonEmpty(os.Getenv(brand.EnvName("DISABLE_RESPONSE_STORAGE")), env[brand.EnvName("DISABLE_RESPONSE_STORAGE")]))
	maxTokens := settings.MaxOutputTokens
	if raw := firstNonEmpty(os.Getenv(brand.EnvName("MAX_OUTPUT_TOKENS")), env[brand.EnvName("MAX_OUTPUT_TOKENS")]); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			maxTokens = parsed
		}
	}
	contextWindow := settings.ContextWindowTokens
	if raw := firstNonEmpty(os.Getenv(brand.EnvName("CONTEXT_WINDOW_TOKENS")), env[brand.EnvName("CONTEXT_WINDOW_TOKENS")]); raw != "" {
		contextWindow, err = strconv.Atoi(raw)
		if err != nil {
			return Runtime{}, fmt.Errorf("invalid context window token count: %w", err)
		}
	}
	if contextWindow < 0 || contextWindow > 10_000_000 {
		return Runtime{}, errors.New("context window token count must be between 0 and 10000000")
	}

	if model == "" {
		return Runtime{}, errors.New("No model configured. Set " + filepath.Join("~", brand.ConfigDirName, "settings.json") + " or the matching provider model env var.")
	}
	if authToken == "" && apiKey == "" {
		authEnv := "ANTHROPIC_AUTH_TOKEN or ANTHROPIC_API_KEY"
		if provider == "openai" {
			authEnv = "OPENAI_AUTH_TOKEN or OPENAI_API_KEY"
		}
		return Runtime{}, errors.New("No auth configured. Run mycode auth login or set " + authEnv + " in process env.")
	}

	sourceSummary := "config: " + SettingsPath() + " > " + ProjectSettingsPath(cwd) + " > " + ClaudeSettingsPath() + " > process.env"
	if SettingsPath() == ProjectSettingsPath(cwd) {
		sourceSummary = "config: " + ProjectSettingsPath(cwd) + " > " + ClaudeSettingsPath() + " > process.env"
	}

	return Runtime{
		Provider:               provider,
		Model:                  model,
		BaseURL:                baseURL,
		AuthToken:              authToken,
		APIKey:                 apiKey,
		WireAPI:                wireAPI,
		ReasoningEffort:        reasoningEffort,
		DisableResponseStorage: disableResponseStorage,
		MaxOutputTokens:        maxTokens,
		ContextWindowTokens:    contextWindow,
		MCPServers:             settings.MCPServers,
		SourceSummary:          sourceSummary,
	}, nil
}

func LoadEffectiveSettings(cwd string) (Settings, error) {
	if err := validateUserConfigLocation(cwd); err != nil {
		return Settings{}, err
	}
	claude, err := readUserSettings(cwd, ClaudeSettingsPath())
	if err != nil {
		return Settings{}, err
	}
	globalMCP, err := readMCPConfig(MCPPath(), cwd)
	if err != nil {
		return Settings{}, err
	}
	projectMCP, err := ReadMCPConfig(ProjectMCPPath(cwd))
	if err != nil {
		return Settings{}, err
	}
	mini, err := readUserSettings(cwd, SettingsPath())
	if err != nil {
		return Settings{}, err
	}
	projectSettings, err := readSettings(ProjectSettingsPath(cwd))
	if err != nil {
		return Settings{}, err
	}
	if err := validateProjectSettings(projectSettings); err != nil {
		return Settings{}, err
	}

	merged := mergeSettings(claude, Settings{MCPServers: globalMCP})
	merged = mergeSettings(merged, mini)
	merged = mergeSettings(merged, Settings{MCPServers: projectMCP})
	return mergeSettings(merged, projectSettings), nil
}

func ReadMCPConfig(path string) (map[string]MCPServerConfig, error) {
	return readMCPConfig(path, "")
}

func readMCPConfig(path, outsideWorkspace string) (map[string]MCPServerConfig, error) {
	var parsed struct {
		MCPServers map[string]MCPServerConfig `json:"mcpServers"`
	}
	if err := readJSONAt(path, &parsed, outsideWorkspace); err != nil {
		return nil, err
	}
	if parsed.MCPServers == nil {
		return map[string]MCPServerConfig{}, nil
	}
	for name, server := range parsed.MCPServers {
		if err := ValidateMCPServerConfig(server); err != nil {
			return nil, fmt.Errorf("MCP server %q: %w", name, err)
		}
	}
	return parsed.MCPServers, nil
}

func ValidateMCPServerConfig(server MCPServerConfig) error {
	if server.URL == "" {
		return nil
	}
	_, parsed, err := egress.Origin(server.URL)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("remote MCP URL must be HTTPS without credentials, query or fragment")
	}
	if server.Command != "" || len(server.Args) > 0 || len(server.Env) > 0 || server.CWD != "" {
		return fmt.Errorf("remote MCP cannot combine URL with command, arguments, environment or cwd")
	}
	if server.Protocol != "" && server.Protocol != "auto" && server.Protocol != "streamable-http" {
		return fmt.Errorf("remote MCP protocol must be streamable-http")
	}
	return nil
}

func SaveMCPConfig(path string, servers map[string]MCPServerConfig) error {
	for name, server := range servers {
		if err := ValidateMCPServerConfig(server); err != nil {
			return fmt.Errorf("MCP server %q: %w", name, err)
		}
	}
	if containsSecrets(Settings{MCPServers: servers}) {
		return errors.New("plaintext MCP secrets cannot be saved; use server-managed OS credentials")
	}
	return writeJSON(path, map[string]any{"mcpServers": servers})
}

func SaveSettings(updates Settings) error {
	if containsSecrets(updates) {
		return errors.New("raw secrets cannot be written to settings; run mycode auth login")
	}
	existing, err := readSettings(SettingsPath())
	if err != nil {
		return err
	}
	if containsSecrets(existing) {
		return errors.New("legacy plaintext credentials found; run mycode auth migrate before saving settings")
	}
	return writeJSON(SettingsPath(), mergeSettings(existing, updates))
}

func readSettings(path string) (Settings, error) {
	return readUserSettings("", path)
}

func readUserSettings(cwd, path string) (Settings, error) {
	var settings Settings
	if err := readJSONAt(path, &settings, cwd); err != nil {
		return Settings{}, err
	}
	if settings.Env == nil {
		settings.Env = map[string]any{}
	}
	if settings.MCPServers == nil {
		settings.MCPServers = map[string]MCPServerConfig{}
	}
	return settings, nil
}

func readJSON(path string, target any) error {
	return readJSONAt(path, target, "")
}

func readJSONAt(path string, target any, outsideWorkspace string) error {
	// Check before opening as well as after opening so FIFOs/devices cannot
	// block startup and a replaced regular file cannot bypass the size limit.
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("configuration must be a regular file no larger than 1 MiB")
	}
	parent, err := workspace.Canonical(filepath.Dir(path))
	if err != nil {
		return err
	}
	expected := filepath.Join(parent, filepath.Base(path))
	var forbiddenRoot string
	if outsideWorkspace != "" {
		forbiddenRoot, err = workspace.Canonical(outsideWorkspace)
		if err != nil {
			return err
		}
		if workspace.Within(forbiddenRoot, expected) {
			return errors.New("user configuration file is inside the workspace")
		}
	}
	access, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer access.Close()
	file, err := workspace.OpenNoFollow(access, filepath.Base(path))
	if err != nil {
		return err
	}
	defer file.Close()
	actual, err := workspace.OpenedPath(file)
	if err != nil {
		return err
	}
	equal := filepath.Clean(actual) == filepath.Clean(expected)
	if runtime.GOOS == "windows" {
		equal = strings.EqualFold(filepath.Clean(actual), filepath.Clean(expected))
	}
	if !equal || (forbiddenRoot != "" && workspace.Within(forbiddenRoot, actual)) {
		return errors.New("configuration file changed location or points inside the workspace")
	}
	info, err = file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("configuration must be a regular file")
	}
	bytes, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(bytes) > 1<<20 {
		return errors.New("configuration exceeds 1 MiB")
	}
	return json.Unmarshal(bytes, target)
}

func writeJSON(path string, value any) error {
	bytes, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return safety.PrivateWrite(path, append(bytes, '\n'))
}

func mergeSettings(base, override Settings) Settings {
	out := base
	if override.Provider != "" {
		out.Provider = override.Provider
	}
	if override.Model != "" {
		out.Model = override.Model
	}
	if override.MaxOutputTokens != 0 {
		out.MaxOutputTokens = override.MaxOutputTokens
	}
	if override.ContextWindowTokens != 0 {
		out.ContextWindowTokens = override.ContextWindowTokens
	}
	if out.Credentials == nil {
		out.Credentials = map[string]CredentialRef{}
	}
	for provider, ref := range override.Credentials {
		out.Credentials[provider] = ref
	}
	if out.Env == nil {
		out.Env = map[string]any{}
	}
	for key, value := range override.Env {
		out.Env[key] = value
	}
	if out.MCPServers == nil {
		out.MCPServers = map[string]MCPServerConfig{}
	}
	for name, server := range override.MCPServers {
		// Server definitions are indivisible: a project must not inherit a user's server secrets.
		out.MCPServers[name] = server
	}
	return out
}

func providerModelEnv(provider string, env map[string]string) string {
	switch normalizeProvider(provider) {
	case "openai":
		return env["OPENAI_MODEL"]
	default:
		return env["ANTHROPIC_MODEL"]
	}
}

func providerBaseURLEnv(provider string, env map[string]string) string {
	switch normalizeProvider(provider) {
	case "openai":
		return env["OPENAI_BASE_URL"]
	default:
		return env["ANTHROPIC_BASE_URL"]
	}
}

func providerAuthTokenEnv(provider string, env map[string]string) string {
	switch normalizeProvider(provider) {
	case "openai":
		return env["OPENAI_AUTH_TOKEN"]
	default:
		return env["ANTHROPIC_AUTH_TOKEN"]
	}
}

func providerAPIKeyEnv(provider string, env map[string]string) string {
	switch normalizeProvider(provider) {
	case "openai":
		return env["OPENAI_API_KEY"]
	default:
		return env["ANTHROPIC_API_KEY"]
	}
}

func defaultBaseURL(provider string) string {
	switch normalizeProvider(provider) {
	case "openai":
		return "https://api.openai.com"
	default:
		return "https://api.anthropic.com"
	}
}

func normalizeProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai", "openai-compatible":
		return "openai"
	case "mock":
		return "mock"
	default:
		return "anthropic"
	}
}

func normalizeWireAPI(wireAPI string) string {
	switch strings.ToLower(strings.TrimSpace(wireAPI)) {
	case "responses", "response":
		return "responses"
	case "chat_completions", "chat-completions", "chat/completions", "chat":
		return "chat_completions"
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func stringify(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	default:
		return ""
	}
}

func parseBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
