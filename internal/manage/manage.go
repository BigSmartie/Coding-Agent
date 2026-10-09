package manage

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/brand"
	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/credentials"
	"github.com/BigSmartie/Coding-Agent/internal/install"
	"github.com/BigSmartie/Coding-Agent/internal/session"
	"github.com/BigSmartie/Coding-Agent/internal/skills"
	"golang.org/x/term"
)

var credentialStore credentials.Store = credentials.SystemStore{}
var readSecretInput = hiddenSecretInput

func Handle(ctx context.Context, cwd string, argv []string) (string, bool, error) {
	if len(argv) == 0 {
		return "", false, nil
	}
	switch argv[0] {
	case "help", "--help", "-h":
		return usage(), true, nil
	case "install-local":
		return handleInstallLocal(ctx, cwd, argv[1:])
	case "auth":
		return handleAuth(cwd, argv[1:])
	case "mcp":
		return handleMCP(ctx, cwd, argv[1:])
	case "sessions":
		return handleSessions(argv[1:])
	case "skills":
		return handleSkills(ctx, cwd, argv[1:])
	default:
		return "", false, nil
	}
}

func handleSessions(args []string) (string, bool, error) {
	if len(args) == 0 {
		return usage(), true, nil
	}
	switch args[0] {
	case "list":
		summaries, err := (session.Store{Dir: config.SessionsDir()}).List()
		if err != nil {
			return "", true, err
		}
		if len(summaries) == 0 {
			return "No saved sessions.", true, nil
		}
		lines := []string{}
		for _, summary := range summaries {
			lines = append(lines, fmt.Sprintf("%s  messages=%d  cwd=%s", summary.ID, summary.MessageCount, summary.CWD))
		}
		return strings.Join(lines, "\n"), true, nil
	default:
		return usage(), true, nil
	}
}

func handleInstallLocal(ctx context.Context, cwd string, args []string) (string, bool, error) {
	return handleInstallLocalWithIO(ctx, cwd, args, os.Stdin, os.Stdout, isTerminal(os.Stdin))
}

type installOptions struct {
	skipBuild bool
	provider  string
	model     string
	baseURL   string
	hasConfig bool
}

func handleInstallLocalWithIO(ctx context.Context, cwd string, args []string, input io.Reader, output io.Writer, interactive bool) (string, bool, error) {
	options, err := parseInstallArgs(args)
	if err != nil {
		return "", true, err
	}

	settings := config.Settings{Env: map[string]any{}}
	if options.hasConfig {
		settings = settingsFromInstallOptions(options)
	} else if interactive {
		prompted, err := promptInstallSettings(cwd, input, output)
		if err != nil {
			return "", true, err
		}
		settings = prompted
		options.hasConfig = true
	}
	if options.hasConfig {
		if err := config.SaveSettings(settings); err != nil {
			for _, ref := range settings.Credentials {
				_ = credentialStore.Delete(ref.ID)
			}
			return "", true, err
		}
	}

	home, _ := os.UserHomeDir()
	installOptions := install.Options{Home: home, RepoRoot: cwd, PathEnv: os.Getenv("PATH")}
	if options.skipBuild {
		installOptions.Build = func(_ context.Context, request install.BuildRequest) error {
			return os.WriteFile(request.OutputPath, []byte("# test binary\n"), 0o755)
		}
	}
	result, err := install.Install(ctx, installOptions)
	if err != nil {
		return "", true, err
	}

	lines := []string{
		"Installed " + brand.AppName + ".",
		"binary: " + result.BinaryPath,
		"launcher: " + result.LauncherPath,
	}
	if options.hasConfig {
		lines = append(lines, "settings: "+config.SettingsPath())
	}
	if result.NeedsPathHint {
		lines = append(lines, "PATH hint: "+result.PathExport)
	} else {
		lines = append(lines, "PATH already contains "+result.BinDir)
	}
	return strings.Join(lines, "\n"), true, nil
}

func parseInstallArgs(args []string) (installOptions, error) {
	options := installOptions{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--skip-build":
			options.skipBuild = true
		case "--provider":
			value, ok := nextArg(args, &i, "--provider")
			if !ok {
				return installOptions{}, fmt.Errorf("Missing value for --provider")
			}
			options.provider = value
			options.hasConfig = true
		case "--model":
			value, ok := nextArg(args, &i, "--model")
			if !ok {
				return installOptions{}, fmt.Errorf("Missing value for --model")
			}
			options.model = value
			options.hasConfig = true
		case "--base-url":
			value, ok := nextArg(args, &i, "--base-url")
			if !ok {
				return installOptions{}, fmt.Errorf("Missing value for --base-url")
			}
			options.baseURL = value
			options.hasConfig = true
		case "--auth-token":
			return installOptions{}, fmt.Errorf("--auth-token is disabled because command-line arguments expose secrets; use mycode auth login or process environment")
		case "--api-key":
			return installOptions{}, fmt.Errorf("--api-key is disabled because command-line arguments expose secrets; use mycode auth login or process environment")
		default:
			if strings.HasPrefix(args[i], "--api-key=") || strings.HasPrefix(args[i], "--auth-token=") {
				return installOptions{}, fmt.Errorf("credential command-line arguments are disabled; use mycode auth login")
			}
			return installOptions{}, fmt.Errorf("Unknown install-local argument: %s", args[i])
		}
	}
	return options, nil
}

func settingsFromInstallOptions(options installOptions) config.Settings {
	provider := options.provider
	if provider == "" {
		provider = "anthropic"
	}
	settings := config.Settings{Provider: provider, Model: options.model, Env: map[string]any{}}
	if options.model != "" {
		settings.Env[providerEnvName(provider, "MODEL")] = options.model
	}
	if options.baseURL != "" {
		settings.Env[providerEnvName(provider, "BASE_URL")] = options.baseURL
	}
	return settings
}

func promptInstallSettings(cwd string, input io.Reader, output io.Writer) (config.Settings, error) {
	reader := bufio.NewReader(input)
	existing, err := config.LoadUserSettings()
	if err != nil {
		return config.Settings{}, err
	}
	currentEnv := existing.Env

	fmt.Fprintln(output, brand.AgentName+" installer")
	fmt.Fprintf(output, "Settings will be written to %s\n", config.SettingsPath())
	fmt.Fprintln(output, "This uses an isolated app directory and will not modify other coding tools.")
	fmt.Fprintln(output)

	providerDefault := firstInstallValue(os.Getenv(brand.EnvName("PROVIDER")), existing.Provider, "anthropic")
	provider, err := askRequired(reader, output, "Provider (anthropic/openai)", providerDefault)
	if err != nil {
		return config.Settings{}, err
	}
	provider = normalizeProvider(provider)

	modelDefault := firstInstallValue(existing.Model, stringify(currentEnv[providerEnvName(provider, "MODEL")]))
	model, err := askRequired(reader, output, "Model name", modelDefault)
	if err != nil {
		return config.Settings{}, err
	}

	baseURLDefault := firstInstallValue(stringify(currentEnv[providerEnvName(provider, "BASE_URL")]), defaultProviderBaseURL(provider))
	baseURL, err := askRequired(reader, output, providerEnvName(provider, "BASE_URL"), baseURLDefault)
	if err != nil {
		return config.Settings{}, err
	}

	secretKey := providerEnvName(provider, "AUTH_TOKEN")
	if provider == "openai" {
		secretKey = providerEnvName(provider, "API_KEY")
	}
	if _, err := config.CredentialOrigin(baseURL); err != nil {
		return config.Settings{}, err
	}
	fmt.Fprintf(output, "%s (hidden; stored in OS credential store): ", secretKey)
	secret, err := readSecretInput(input, reader)
	fmt.Fprintln(output)
	if err != nil {
		return config.Settings{}, err
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return config.Settings{}, fmt.Errorf("%s cannot be empty", secretKey)
	}

	options := installOptions{provider: provider, model: model, baseURL: baseURL, hasConfig: true}
	kind := "auth_token"
	if provider == "openai" {
		kind = "api_key"
	}
	ref, err := config.SaveCredential(provider, baseURL, kind, secret, credentialStore)
	if err != nil {
		return config.Settings{}, err
	}
	settings := settingsFromInstallOptions(options)
	settings.Credentials = map[string]config.CredentialRef{provider: ref}
	return settings, nil
}

func hiddenSecretInput(input io.Reader, reader *bufio.Reader) (string, error) {
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", fmt.Errorf("credential entry requires an interactive terminal; use process environment for noninteractive BYOK")
	}
	if reader.Buffered() != 0 {
		return "", fmt.Errorf("enter the credential only after the hidden prompt appears")
	}
	secret, err := term.ReadPassword(int(file.Fd()))
	defer func() {
		for i := range secret {
			secret[i] = 0
		}
	}()
	return string(secret), err
}

func handleAuth(cwd string, args []string) (string, bool, error) {
	if len(args) == 2 && args[0] == "migrate" && args[1] == "--from-project" {
		if err := config.MigrateProjectCredentials(cwd, credentialStore); err != nil {
			return "", true, err
		}
		return "Project provider credentials migrated to the OS store and user settings; plaintext provider fields removed from project settings.", true, nil
	}
	if len(args) != 1 {
		return "", true, fmt.Errorf("use mycode auth login or mycode auth migrate [--from-project]")
	}
	switch args[0] {
	case "login":
		settings, err := promptInstallSettings(cwd, os.Stdin, os.Stdout)
		if err != nil {
			return "", true, err
		}
		if err := config.SaveSettings(settings); err != nil {
			for _, ref := range settings.Credentials {
				_ = credentialStore.Delete(ref.ID)
			}
			return "", true, err
		}
		return "Credential saved in the operating system store; settings contain only its reference and HTTPS origin.", true, nil
	case "migrate":
		if err := config.MigrateCredentials(credentialStore); err != nil {
			return "", true, err
		}
		return "User settings credentials migrated to the operating system store. Project and other application files were not modified.", true, nil
	default:
		return "", true, fmt.Errorf("use mycode auth login or mycode auth migrate")
	}
}

func nextArg(args []string, index *int, flag string) (string, bool) {
	if *index+1 >= len(args) || strings.HasPrefix(args[*index+1], "--") {
		return "", false
	}
	*index++
	return args[*index], true
}

func providerEnvName(provider, suffix string) string {
	if normalizeProvider(provider) == "openai" {
		return "OPENAI_" + suffix
	}
	return "ANTHROPIC_" + suffix
}

func normalizeProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai", "openai-compatible":
		return "openai"
	default:
		return "anthropic"
	}
}

func defaultProviderBaseURL(provider string) string {
	if normalizeProvider(provider) == "openai" {
		return "https://api.openai.com"
	}
	return "https://api.anthropic.com"
}

func askRequired(reader *bufio.Reader, output io.Writer, label, defaultValue string) (string, error) {
	for {
		if defaultValue != "" {
			fmt.Fprintf(output, "%s [%s]: ", label, defaultValue)
		} else {
			fmt.Fprintf(output, "%s: ", label)
		}
		line, err := readLine(reader)
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(line)
		if value == "" {
			value = defaultValue
		}
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(output, "This value cannot be empty. Please try again.")
	}
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	if err == io.EOF && line == "" {
		return "", nil
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func secretPromptSuffix(secret string) string {
	if secret == "" {
		return " [not set]"
	}
	return " [saved]"
}

func firstInstallValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringify(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func isTerminal(file *os.File) bool {
	return term.IsTerminal(int(file.Fd()))
}

func handleSkills(ctx context.Context, cwd string, args []string) (string, bool, error) {
	if len(args) == 0 {
		return usage(), true, nil
	}
	scope, rest := parseScope(args)
	if len(rest) == 0 {
		return usage(), true, nil
	}
	home, _ := os.UserHomeDir()
	store := skills.NewStore(cwd, home)
	switch rest[0] {
	case "list":
		discovered, err := store.Discover(ctx)
		if err != nil {
			return "", true, err
		}
		if len(discovered) == 0 {
			return "No skills discovered.", true, nil
		}
		lines := []string{}
		for _, skill := range discovered {
			lines = append(lines, skill.Name+": "+skill.Description+" ("+skill.Path+")")
		}
		return strings.Join(lines, "\n"), true, nil
	case "add":
		if len(rest) < 2 {
			return "", true, fmt.Errorf("Missing skill source path.")
		}
		name := ""
		for i := 2; i < len(rest)-1; i++ {
			if rest[i] == "--name" {
				name = rest[i+1]
			}
		}
		target, err := store.Install(ctx, rest[1], name, scope)
		if err != nil {
			return "", true, err
		}
		return "Installed skill at " + target, true, nil
	case "remove":
		if len(rest) < 2 {
			return "", true, fmt.Errorf("Missing skill name.")
		}
		target, _, err := store.Remove(ctx, rest[1], scope)
		if err != nil {
			return "", true, err
		}
		return "Removed skill " + rest[1] + " from " + target, true, nil
	}
	return usage(), true, nil
}

func handleMCP(_ context.Context, cwd string, args []string) (string, bool, error) {
	if len(args) == 0 {
		return usage(), true, nil
	}
	scope, rest := parseScope(args)
	if len(rest) == 0 {
		return usage(), true, nil
	}
	path := config.MCPPath()
	if scope == "project" {
		path = config.ProjectMCPPath(cwd)
	}
	switch rest[0] {
	case "list":
		servers, err := config.ReadMCPConfig(path)
		if err != nil {
			return "", true, err
		}
		if len(servers) == 0 {
			return "No MCP servers configured in " + path + ".", true, nil
		}
		lines := []string{}
		for name, server := range servers {
			target := strings.TrimSpace(server.Command + " " + strings.Join(server.Args, " "))
			if server.URL != "" {
				target = server.URL
			}
			lines = append(lines, name+": "+target)
		}
		return strings.Join(lines, "\n"), true, nil
	case "remove":
		if len(rest) < 2 {
			return "", true, fmt.Errorf("Missing MCP server name.")
		}
		servers, err := config.ReadMCPConfig(path)
		if err != nil {
			return "", true, err
		}
		delete(servers, rest[1])
		return "Removed MCP server " + rest[1] + " from " + path, true, config.SaveMCPConfig(path, servers)
	case "add":
		separator := -1
		for i, value := range rest {
			if value == "--" {
				separator = i
				break
			}
		}
		if separator == -1 || separator+1 >= len(rest) || separator < 2 {
			return "", true, fmt.Errorf("Use `--` before the MCP command.")
		}
		head := append([]string(nil), rest[1:separator]...)
		name := head[0]
		protocol := ""
		env := map[string]any{}
		for i := 1; i < len(head); i++ {
			switch head[i] {
			case "--protocol":
				if i+1 >= len(head) {
					return "", true, fmt.Errorf("Missing value for --protocol")
				}
				protocol = head[i+1]
				i++
			case "--env":
				if i+1 >= len(head) {
					return "", true, fmt.Errorf("Missing value for --env")
				}
				key, value, ok := strings.Cut(head[i+1], "=")
				if !ok || strings.TrimSpace(key) == "" {
					return "", true, fmt.Errorf("Invalid --env value; expected NAME=VALUE")
				}
				env[strings.TrimSpace(key)] = value
				i++
			default:
				return "", true, fmt.Errorf("Unknown arguments: %s", head[i])
			}
		}
		command := rest[separator+1]
		commandArgs := rest[separator+2:]
		servers, err := config.ReadMCPConfig(path)
		if err != nil {
			return "", true, err
		}
		server := config.MCPServerConfig{Command: command, Args: commandArgs, Protocol: protocol}
		if len(env) > 0 {
			server.Env = env
		}
		servers[name] = server
		return "Added MCP server " + name + " to " + path, true, config.SaveMCPConfig(path, servers)
	case "add-url":
		if len(rest) != 3 {
			return "", true, fmt.Errorf("Use mcp add-url <name> <https-url>.")
		}
		server := config.MCPServerConfig{URL: rest[2], Protocol: "streamable-http"}
		if err := config.ValidateMCPServerConfig(server); err != nil {
			return "", true, err
		}
		servers, err := config.ReadMCPConfig(path)
		if err != nil {
			return "", true, err
		}
		servers[rest[1]] = server
		return "Added remote MCP server " + rest[1] + " to " + path + "; review with mycode trust mcp " + rest[1], true, config.SaveMCPConfig(path, servers)
	}
	return usage(), true, nil
}

func parseScope(args []string) (string, []string) {
	out := []string{}
	scope := "user"
	for _, arg := range args {
		if arg == "--project" {
			scope = "project"
			continue
		}
		out = append(out, arg)
	}
	return scope, out
}

func usage() string {
	return brand.CommandName + ` management commands

` + brand.CommandName + ` install-local
` + brand.CommandName + ` auth login
` + brand.CommandName + ` auth migrate [--from-project]
` + brand.CommandName + ` sessions list

` + brand.CommandName + ` mcp list [--project]
` + brand.CommandName + ` mcp add <name> [--project] -- <command> [args...]
` + brand.CommandName + ` mcp add-url <name> <https-url> [--project]
` + brand.CommandName + ` mcp remove <name> [--project]

` + brand.CommandName + ` skills list
` + brand.CommandName + ` skills add <path-to-skill-or-dir> [--name <name>] [--project]
` + brand.CommandName + ` skills remove <name> [--project]`
}
