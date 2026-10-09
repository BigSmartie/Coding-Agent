package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func integrationEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("MYTHOS_CODE_SANDBOX_INTEGRATION") != "1" {
		t.Skip("set MYTHOS_CODE_SANDBOX_INTEGRATION=1 with a locally installed Linux /bin/sh image")
	}
	if os.Getenv("MYTHOS_CODE_SANDBOX_IMAGE") == "" {
		t.Fatal("MYTHOS_CODE_SANDBOX_IMAGE is required for integration tests")
	}
}

func TestIntegrationIsolation(t *testing.T) {
	integrationEnabled(t)
	cwd := t.TempDir()
	for name, value := range map[string]string{"source.txt": "original", "nested/child.txt": "child", ".env": "workspace-secret", ".mythos-code/settings.json": "configuration-secret", ".git/config": "git-secret"} {
		target := filepath.Join(cwd, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OPENAI_API_KEY", "host-secret-must-not-enter-container")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "host-token-must-not-enter-container")
	script := `set -eu
test "$(cat source.txt)" = original
test ! -e .env
test ! -e .mythos-code
test ! -e .git
test ! -S /var/run/docker.sock
if touch /input/forbidden-input-write 2>/dev/null; then exit 21; fi
grep -q ' /workspace tmpfs ' /proc/mounts
grep -q 'size=524288k' /proc/mounts
test -z "${OPENAI_API_KEY:-}"
test -z "${ANTHROPIC_AUTH_TOKEN:-}"
test "$(ls /sys/class/net)" = lo
test "$(id -u)" = 65534
if touch /forbidden-root-write 2>/dev/null; then exit 20; fi
printf changed > source.txt
printf childchanged > nested/child.txt
printf generated > generated.txt
echo isolation-ok`
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd, cleanup, err := Prepare(ctx, Options{Workspace: cwd, Command: "/bin/sh", Args: []string{"-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox isolation command: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "isolation-ok") {
		t.Fatalf("missing success marker: %s", output)
	}
	for name, want := range map[string]string{"source.txt": "original", "nested/child.txt": "child"} {
		data, err := os.ReadFile(filepath.Join(cwd, filepath.FromSlash(name)))
		if err != nil || string(data) != want {
			t.Fatalf("host source changed: %s, %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cwd, "generated.txt")); !os.IsNotExist(err) {
		t.Fatal("generated file escaped disposable snapshot")
	}
}

type readyBuffer struct {
	LimitedBuffer
	ready chan struct{}
	once  sync.Once
}

func (b *readyBuffer) Write(p []byte) (int, error) {
	n, err := b.LimitedBuffer.Write(p)
	if strings.Contains(b.String(), "children-ready") {
		b.once.Do(func() { close(b.ready) })
	}
	return n, err
}

func TestIntegrationCancellationRemovesContainerAndChildren(t *testing.T) {
	integrationEnabled(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, cleanup, err := Prepare(ctx, Options{Workspace: t.TempDir(), Command: "/bin/sh", Args: []string{"-c", "sleep 120 & echo children-ready; wait"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	output := &readyBuffer{LimitedBuffer: LimitedBuffer{Limit: 4096}, ready: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-output.ready:
	case err := <-done:
		t.Fatalf("container failed before child start: %v\n%s", err, output.String())
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("container child startup timed out")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(25 * time.Second):
		t.Fatal("cancellation did not terminate process tree")
	}
	cleanup()
	name := ""
	for i, arg := range cmd.Args {
		if arg == "--name" && i+1 < len(cmd.Args) {
			name = cmd.Args[i+1]
			break
		}
	}
	if name == "" {
		t.Fatal("missing sandbox container identity")
	}
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer checkCancel()
	inspect := exec.CommandContext(checkCtx, cmd.Path, "container", "inspect", name)
	inspect.Env = HostEnvironment()
	if err := inspect.Run(); err == nil {
		t.Fatal("cancelled container still exists")
	}
}
