//go:build !windows

package credentials

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelperEnvironmentOmitsCredentialsAndLoaderOverrides(t *testing.T) {
	env := helperEnv([]string{"HOME=/home/test", "OPENAI_API_KEY=fixture", "ANTHROPIC_AUTH_TOKEN=fixture", "LD_PRELOAD=evil", "DYLD_INSERT_LIBRARIES=evil", "PATH=/workspace", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1/bus"})
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"fixture", "LD_PRELOAD", "DYLD_", "/workspace"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("helper env contains %s", forbidden)
		}
	}
	if !strings.Contains(joined, "DBUS_SESSION_BUS_ADDRESS=") || !strings.Contains(joined, "HOME=") {
		t.Fatal("required OS credential session missing")
	}
}

func TestHelperPathRejectsWorkspaceExecutableAndSymlink(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "secret-tool")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := helperPath(helper, root); err == nil {
		t.Fatal("workspace helper accepted")
	}
	link := filepath.Join(t.TempDir(), "secret-tool")
	if err := os.Symlink(helper, link); err != nil {
		t.Fatal(err)
	}
	if _, err := helperPath(link, root); err == nil {
		t.Fatal("workspace helper accepted through symlink")
	}
}

func TestHelperOutputBoundCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := boundedOutput{cancel: cancel}
	if _, err := output.Write(make([]byte, 16*1024+1)); err == nil {
		t.Fatal("unbounded output accepted")
	}
	if ctx.Err() == nil || output.Len() != 0 {
		t.Fatal("oversized output was retained or helper not canceled")
	}
}

func TestSecurityQuoteKeepsSecretAsOneArgument(t *testing.T) {
	if got := securityQuote(`a"b\c`); got != `"a\"b\\c"` {
		t.Fatalf("unexpected quote: %q", got)
	}
	for _, secret := range []string{"secret\ncommand", "secret\rcommand", "secret\x00command", "secret\tcommand"} {
		if err := valid("test", secret); err == nil {
			t.Fatal("control character accepted")
		}
	}
}
