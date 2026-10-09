package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWSLArgumentsKeepHostMountsOutAndValidateInputs(t *testing.T) {
	root := t.TempDir()
	args, err := wslBwrapArgs(Options{Command: "/bin/echo", Args: []string{"hello"}}, root, "/mnt/c/private/snapshot", "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{"--unshare-all", "--disable-userns", "--ro-bind /mnt/c/private/snapshot /input", "--tmpfs /workspace", "--clearenv"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("WSL sandbox misses %s: %s", required, joined)
		}
	}
	if strings.Contains(joined, "--ro-bind /mnt/c /mnt/c") {
		t.Fatal("host drive was exposed")
	}
	if _, err := wslBwrapArgs(Options{Command: "/bin/echo", Env: map[string]string{"OPENAI_API_KEY": "secret"}}, root, "/mnt/c/private/snapshot", ""); err == nil {
		t.Fatal("credential environment was accepted")
	}
	if _, err := wslBwrapArgs(Options{Command: "/bin/echo", CWD: filepath.Dir(root)}, root, "/mnt/c/private/snapshot", ""); err == nil {
		t.Fatal("cwd escape was accepted")
	}
}

func TestWSLTTYUsesGuestPTYWithoutInterpolatingCommand(t *testing.T) {
	root := t.TempDir()
	options := Options{Command: "/bin/echo", Args: []string{"hello'; touch /tmp/escaped; '"}, TTY: true}
	args, err := wslBwrapArgs(options, root, "/mnt/c/private/snapshot", "/mnt/c/private/scratch")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "pty.spawn(sys.argv[1:])") || args[len(args)-1] != options.Args[0] {
		t.Fatalf("guest PTY arguments missing or changed: %#v", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, "pty.spawn") && strings.Contains(arg, options.Args[0]) {
			t.Fatal("command argument was interpolated into PTY source")
		}
	}
}

func TestWSLBackendIsolation(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("MY_CODE_WSL_INTEGRATION") != "1" {
		t.Skip("opt-in Windows WSL2 integration")
	}
	t.Setenv("MY_CODE_SANDBOX_BACKEND", "wsl")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("snapshot-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	const script = `set -eu
id -u
cat visible.txt
test ! -e /mnt/c
test ! -e /workspace/.env
test -z "${HTTP_PROXY:-}"
test "$(ulimit -v)" -le 1048576
python3 -c "import socket; s=socket.socket(); s.settimeout(1); assert s.connect_ex(('1.1.1.1',80)) != 0"
echo scratch > scratch.txt`
	command, cleanup, err := Prepare(ctx, Options{Workspace: root, Command: "/bin/sh", Args: []string{"-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	output, runErr := command.CombinedOutput()
	cleanup()
	if runErr != nil || !strings.Contains(string(output), "65534") || !strings.Contains(string(output), "snapshot-data") {
		t.Fatalf("WSL sandbox did not isolate command: %v, %s", runErr, output)
	}
	if _, err := os.Stat(filepath.Join(root, "scratch.txt")); !os.IsNotExist(err) {
		t.Fatal("temporary command write reached host workspace")
	}

	job, jobCleanup, err := Prepare(ctx, Options{Workspace: root, Command: "/bin/sh", Args: []string{"-c", "echo artifact > exported.txt"}, RetainContainer: true, SessionID: "wsl-integration"})
	if err != nil {
		t.Fatal(err)
	}
	defer jobCleanup()
	if output, err := job.CombinedOutput(); err != nil {
		t.Fatalf("WSL retained job failed: %v, %s", err, output)
	}
	if err := CheckJobScratchBounds(job); err != nil {
		t.Fatal(err)
	}
	data, err := CopyArtifact(ctx, job, "exported.txt")
	if err != nil || strings.TrimSpace(string(data)) != "artifact" {
		t.Fatalf("WSL artifact export failed: %v, %q", err, data)
	}

	cancelCtx, stop := context.WithCancel(context.Background())
	defer stop()
	sleeper, sleeperCleanup, err := Prepare(cancelCtx, Options{Workspace: root, Command: "/bin/sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(2*time.Second, stop)
	defer timer.Stop()
	started := time.Now()
	err = sleeper.Run()
	sleeperCleanup()
	if err == nil || time.Since(started) > 10*time.Second {
		t.Fatalf("WSL command did not stop on cancellation: %v", err)
	}
}
