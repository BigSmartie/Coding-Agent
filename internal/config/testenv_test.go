package config

import (
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/testutil"
)

func IsolateTestEnv(t *testing.T, home string) {
	t.Helper()
	testutil.IsolateEnv(t, home)
}
