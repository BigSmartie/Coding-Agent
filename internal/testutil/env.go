package testutil

import (
	"path/filepath"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/brand"
)

func IsolateEnv(t *testing.T, home string) {
	t.Helper()

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(brand.EnvName("HOME"), filepath.Join(home, brand.ConfigDirName))
	t.Setenv(brand.EnvName("MODEL_MODE"), "")
	t.Setenv(brand.EnvName("PROVIDER"), "")
	t.Setenv(brand.EnvName("MODEL"), "")
	t.Setenv(brand.EnvName("MAX_OUTPUT_TOKENS"), "")

	for _, key := range []string{
		"ANTHROPIC_MODEL",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_API_KEY",
		"OPENAI_MODEL",
		"OPENAI_BASE_URL",
		"OPENAI_AUTH_TOKEN",
		"OPENAI_API_KEY",
		"OPENAI_WIRE_API",
		"OPENAI_CREDENTIAL_ORIGIN",
		"ANTHROPIC_CREDENTIAL_ORIGIN",
	} {
		t.Setenv(key, "")
	}

	t.Setenv(brand.EnvName("REASONING_EFFORT"), "")
	t.Setenv(brand.EnvName("DISABLE_RESPONSE_STORAGE"), "")
}
