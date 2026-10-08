package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandPlanCannotInjectDockerArguments(t *testing.T) {
	cwd := t.TempDir()
	snapshot := filepath.Join(t.TempDir(), "workspace")
	input := Options{Workspace: cwd, CWD: cwd, Command: "/bin/sh", Args: []string{"-c", "echo hello", "--privileged", "--mount=type=bind,src=/,dst=/host"}, Env: map[string]string{"EXPLICIT_SETTING": "ok"}}
	argv, err := commandArgs(input, cwd, snapshot, "sha256:trusted", "mycode-test")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, "\n")
	for _, required := range []string{"--pull=never", "--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "--log-driver=none", "--tmpfs=/workspace:rw,nosuid,nodev,size=512m,mode=1777", ",dst=/input,readonly", "--pids-limit=128", "--memory=1g", "--cpus=2", "--user=65534:65534", "--entrypoint\n/bin/sh\nsha256:trusted"} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing security constraint %q", required)
		}
	}
	imageIndex := -1
	for i, value := range argv {
		if value == "sha256:trusted" {
			imageIndex = i
			break
		}
	}
	if imageIndex < 0 || strings.Join(argv[len(argv)-len(input.Args):], "\x00") != strings.Join(input.Args, "\x00") {
		t.Fatal("executable arguments escaped the image boundary")
	}
	for _, arg := range argv[:imageIndex] {
		if arg == "--privileged" || strings.Contains(arg, "dst=/host") {
			t.Fatal("untrusted argument became a Docker flag")
		}
	}
}

func TestCommandPlanRejectsOutsideCWDAndReservedEnvironment(t *testing.T) {
	cwd := t.TempDir()
	for _, name := range []string{"PATH", "LD_PRELOAD", "HOME", "NODE_OPTIONS", "PYTHONPATH", "OPENAI_API_KEY", "ANTHROPIC_AUTH_TOKEN", "DOCKER_HOST", "BAD=NAME"} {
		_, err := commandArgs(Options{Workspace: cwd, Command: "env", Env: map[string]string{name: "secret"}}, cwd, t.TempDir(), "sha256:trusted", "test")
		if err == nil {
			t.Errorf("reserved env accepted: %s", name)
		}
	}
	_, err := commandArgs(Options{Workspace: cwd, CWD: t.TempDir(), Command: "cat"}, cwd, t.TempDir(), "sha256:trusted", "test")
	if err == nil {
		t.Fatal("outside cwd accepted")
	}
}

func TestNoSandboxConfigurationNeverFallsBackToHost(t *testing.T) {
	t.Setenv("MY_CODE_SANDBOX_IMAGE", "")
	cmd, cleanup, err := Prepare(context.Background(), Options{Workspace: t.TempDir(), Command: "echo", Args: []string{"hello"}})
	if err == nil || cmd != nil || cleanup != nil || !strings.Contains(err.Error(), "host execution is disabled") {
		t.Fatalf("unexpected fallback: cmd=%v err=%v", cmd, err)
	}
}

func TestHostEnvironmentDoesNotForwardCredentialsOrLoaderSettings(t *testing.T) {
	for _, name := range []string{"OPENAI_API_KEY", "ANTHROPIC_AUTH_TOKEN", "HTTP_PROXY", "HTTPS_PROXY", "LD_PRELOAD", "NODE_OPTIONS", "DOCKER_HOST"} {
		t.Setenv(name, "sentinel-private-value")
	}
	for _, entry := range HostEnvironment() {
		if strings.Contains(entry, "sentinel-private-value") {
			t.Fatalf("host environment leaked: %s", strings.SplitN(entry, "=", 2)[0])
		}
	}
}

func TestSnapshotExcludesProtectedFilesAndDoesNotModifySource(t *testing.T) {
	cwd := t.TempDir()
	for name, value := range map[string]string{"main.go": "package main", "nested/file.txt": "source", ".env": "secret", ".my-code/settings.json": "private", ".git/config": "private", "nested/.env.local": "private"} {
		target := filepath.Join(cwd, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := makeSnapshot(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(snapshot))
	for _, name := range []string{".env", ".my-code", ".git", "nested/.env.local"} {
		if _, err := os.Stat(filepath.Join(snapshot, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Fatalf("protected file copied: %s", name)
		}
	}
	if err := os.WriteFile(filepath.Join(snapshot, "nested", "file.txt"), []byte("sandbox edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "nested", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "source" {
		t.Fatal("snapshot changed source")
	}
}

func TestSnapshotRejectsHardlinkAlias(t *testing.T) {
	cwd := t.TempDir()
	secret := filepath.Join(cwd, ".env")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, filepath.Join(cwd, "ordinary.txt")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	snapshot, err := makeSnapshot(context.Background(), cwd)
	if err == nil {
		os.RemoveAll(filepath.Dir(snapshot))
		t.Fatal("snapshot admitted hardlink to protected content")
	}
}

func TestOutputBound(t *testing.T) {
	buffer := &LimitedBuffer{Limit: 16}
	input := strings.Repeat("x", 100000)
	n, err := buffer.Write([]byte(input))
	if err != nil || n != len(input) {
		t.Fatal("bounded writer broke child writes")
	}
	output := buffer.String()
	if !strings.HasPrefix(output, strings.Repeat("x", 16)) || !strings.HasSuffix(output, "[output truncated]") || len(output) > 64 {
		t.Fatalf("unexpected bounded output %q", output)
	}
}
