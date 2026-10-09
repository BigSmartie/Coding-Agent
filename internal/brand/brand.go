package brand

const (
	AppName         = "MythosCode"
	AgentName       = "mythoscode"
	CommandName     = "mythoscode"
	ConfigDirName   = ".mythos-code"
	EnvPrefix       = "MYTHOS_CODE"
	BinaryName      = "mythoscode-go"
	LauncherName    = "mythoscode"
	Version         = "0.1.0-alpha.1"
	DefaultGPTModel = "gpt-5.5"
	LegacyAppName   = "MiniCode"
	LegacyAgentName = "mini-code"
)

func EnvName(suffix string) string {
	return EnvPrefix + "_" + suffix
}
