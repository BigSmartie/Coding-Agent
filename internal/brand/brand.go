package brand

const (
	AppName         = "MyCode"
	AgentName       = "mycode"
	CommandName     = "mycode"
	ConfigDirName   = ".my-code"
	EnvPrefix       = "MY_CODE"
	BinaryName      = "mycode-go"
	LauncherName    = "mycode"
	Version         = "0.1.0-alpha.1"
	DefaultGPTModel = "gpt-5.5"
	LegacyAppName   = "MiniCode"
	LegacyAgentName = "mini-code"
)

func EnvName(suffix string) string {
	return EnvPrefix + "_" + suffix
}
