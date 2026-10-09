package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/workspace"
)

const (
	wslScratchEnv = "MYCODE_WSL_SCRATCH"
	wslTTYEnv     = "MYCODE_WSL_GUEST_TTY"
)

// prepareWSL uses WSL2 as a Windows-hosted Linux VM and Bubblewrap inside it.
// The guest sees only a filtered snapshot, a minimal read-only Linux runtime,
// and a bounded temporary workspace; DrvFs host mounts are not bound into it.
func prepareWSL(ctx context.Context, options Options) (*exec.Cmd, func(), error) {
	if runtime.GOOS != "windows" {
		return nil, nil, errors.New("WSL sandbox backend is available on Windows only")
	}
	root, err := workspace.Canonical(options.Workspace)
	if err != nil {
		return nil, nil, err
	}
	wsl, err := exec.LookPath("wsl.exe")
	if err != nil {
		return nil, nil, errors.New("WSL sandbox unavailable: wsl.exe is required")
	}
	wsl, err = filepath.Abs(wsl)
	if err != nil {
		return nil, nil, err
	}
	if workspace.Within(root, wsl) {
		return nil, nil, errors.New("sandbox refuses a WSL executable inside the untrusted workspace")
	}
	kernel, err := wslOutput(ctx, wsl, "/usr/bin/uname", "-r")
	if err != nil || !strings.Contains(strings.ToLower(kernel), "wsl2") {
		return nil, nil, errors.New("WSL sandbox requires a running WSL2 distribution")
	}
	for _, binary := range []string{"/usr/bin/bwrap", "/usr/bin/prlimit", "/usr/bin/wslpath"} {
		if _, err := wslOutput(ctx, wsl, "/usr/bin/test", "-x", binary); err != nil {
			return nil, nil, fmt.Errorf("WSL sandbox requires %s in the default distribution", binary)
		}
	}
	if options.TTY {
		if _, err := wslOutput(ctx, wsl, "/usr/bin/test", "-x", "/usr/bin/python3"); err != nil {
			return nil, nil, errors.New("WSL PTY jobs require /usr/bin/python3 in the default distribution")
		}
	}
	snapshot, err := makeSnapshot(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	removeSnapshot := func() { _ = os.RemoveAll(filepath.Dir(snapshot)) }
	snapshotPath, err := wslPath(ctx, wsl, snapshot)
	if err != nil {
		removeSnapshot()
		return nil, nil, err
	}
	if options.RetainContainer {
		if !validSandboxSessionID(options.SessionID) {
			removeSnapshot()
			return nil, nil, errors.New("retained sandbox job requires a valid session id")
		}
		options.jobRoot, err = os.MkdirTemp("", "mycode-jobs-"+options.SessionID+"-")
		if err == nil {
			options.scratch = filepath.Join(options.jobRoot, "workspace")
			err = os.Mkdir(options.scratch, 0o777)
		}
		if err != nil {
			_ = os.RemoveAll(options.jobRoot)
			removeSnapshot()
			return nil, nil, err
		}
	}
	var scratchPath string
	if options.scratch != "" {
		scratchPath, err = wslPath(ctx, wsl, options.scratch)
		if err != nil {
			_ = os.RemoveAll(options.jobRoot)
			removeSnapshot()
			return nil, nil, err
		}
	}
	args, err := wslBwrapArgs(options, root, snapshotPath, scratchPath)
	if err != nil {
		_ = os.RemoveAll(options.jobRoot)
		removeSnapshot()
		return nil, nil, err
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			removeSnapshot()
			if options.jobRoot != "" {
				_ = os.RemoveAll(options.jobRoot)
			}
		})
	}
	argv := append([]string{"--exec", "/usr/bin/prlimit", "--as=1073741824", "--cpu=120", "--fsize=536870912", "--nproc=128", "--", "/usr/bin/bwrap"}, args...)
	cmd := exec.CommandContext(ctx, wsl, argv...)
	cmd.Env = HostEnvironment()
	if options.scratch != "" {
		cmd.Env = append(cmd.Env, wslScratchEnv+"="+options.scratch)
	}
	if options.TTY {
		cmd.Env = append(cmd.Env, wslTTYEnv+"=1")
	}
	cmd.WaitDelay = 12 * time.Second
	return cmd, cleanup, nil
}

func wslOutput(ctx context.Context, executable string, args ...string) (string, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, executable, append([]string{"--exec"}, args...)...)
	cmd.Env = HostEnvironment()
	output := &LimitedBuffer{Limit: 4096}
	cmd.Stdout = output
	cmd.Stderr = &LimitedBuffer{Limit: 4096}
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

func wslPath(ctx context.Context, executable, hostPath string) (string, error) {
	converted, err := wslOutput(ctx, executable, "/usr/bin/wslpath", "-a", "-u", hostPath)
	if err != nil || !validWSLPath(converted) {
		return "", errors.New("WSL cannot access the sandbox snapshot through its mounted Windows filesystem")
	}
	return converted, nil
}

func validWSLPath(value string) bool {
	return strings.HasPrefix(value, "/") && path.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n") && len(value) <= 4096
}

func wslBwrapArgs(options Options, root, snapshotPath, scratchPath string) ([]string, error) {
	if !validWSLPath(snapshotPath) || (options.RetainContainer && !validWSLPath(scratchPath)) {
		return nil, errors.New("invalid WSL sandbox mount path")
	}
	canonicalRoot, err := workspace.Canonical(root)
	if err != nil {
		return nil, err
	}
	if options.Command == "" || strings.HasPrefix(options.Command, "-") || strings.ContainsAny(options.Command, "\x00\r\n") {
		return nil, errors.New("command must be an explicit executable; pass arguments separately")
	}
	for _, arg := range options.Args {
		if strings.ContainsRune(arg, '\x00') {
			return nil, errors.New("command argument contains NUL")
		}
	}
	if options.SessionID != "" && !validSandboxSessionID(options.SessionID) {
		return nil, errors.New("invalid sandbox session id")
	}
	cwd := options.CWD
	if cwd == "" {
		cwd = canonicalRoot
	}
	target, err := workspace.Resolve(context.Background(), canonicalRoot, cwd, "command_cwd", nil)
	if err != nil {
		return nil, err
	}
	if !workspace.Within(canonicalRoot, target) {
		return nil, errors.New("command cwd escapes workspace")
	}
	relative, err := filepath.Rel(canonicalRoot, target)
	if err != nil {
		return nil, err
	}
	args := []string{"--unshare-all", "--unshare-user", "--disable-userns", "--die-with-parent", "--new-session", "--cap-drop", "ALL",
		"--uid", "65534", "--gid", "65534", "--ro-bind", "/usr", "/usr", "--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib", "/lib", "--ro-bind", "/lib64", "/lib64", "--proc", "/proc", "--dev", "/dev",
		"--size", "268435456", "--tmpfs", "/tmp", "--ro-bind", snapshotPath, "/input"}
	if options.RetainContainer {
		args = append(args, "--bind", scratchPath, "/workspace")
	} else {
		args = append(args, "--size", "536870912", "--tmpfs", "/workspace")
	}
	args = append(args, "--clearenv", "--setenv", "HOME", "/tmp", "--setenv", "TMPDIR", "/tmp",
		"--setenv", "PATH", "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "--setenv", "GOCACHE", "/tmp/go-build",
		"--setenv", "GOTOOLCHAIN", "local", "--chdir", "/workspace")
	keys := make([]string, 0, len(options.Env))
	for key := range options.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(key) || strings.ContainsRune(options.Env[key], '\x00') {
			return nil, errors.New("invalid explicit sandbox environment variable")
		}
		upper := strings.ToUpper(key)
		if upper == "PATH" || upper == "HOME" || upper == "TMPDIR" || strings.HasPrefix(upper, "LD_") || strings.HasPrefix(upper, "DYLD_") || upper == "NODE_OPTIONS" || upper == "PYTHONPATH" || strings.Contains(upper, "API_KEY") || upper == "OPENAI_API_BASE" || upper == "ANTHROPIC_AUTH_TOKEN" || strings.HasPrefix(upper, "DOCKER_") {
			return nil, fmt.Errorf("sandbox environment variable %s is reserved", key)
		}
		args = append(args, "--setenv", key, options.Env[key])
	}
	bootstrap := `set -eu; cp -R /input/. /workspace/; cd "$1"; shift; exec "$@"`
	if options.TTY {
		// pty.spawn passes the executable and arguments as an argv vector. No
		// model-controlled command text enters the fixed shell or Python source.
		bootstrap = `set -eu; cp -R /input/. /workspace/; cd "$1"; shift; exec /usr/bin/python3 -c 'import os,pty,sys; sys.exit(os.waitstatus_to_exitcode(pty.spawn(sys.argv[1:])))' "$@"`
	}
	args = append(args, "--", "/bin/sh", "-c", bootstrap, "mycode-sandbox", path.Join("/workspace", filepath.ToSlash(relative)), options.Command)
	return append(args, options.Args...), nil
}

func validSandboxSessionID(id string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`).MatchString(id)
}

func wslJobScratch(prepared *exec.Cmd) string {
	if prepared == nil || !strings.EqualFold(filepath.Base(prepared.Path), "wsl.exe") {
		return ""
	}
	for _, item := range prepared.Env {
		if strings.HasPrefix(item, wslScratchEnv+"=") {
			return strings.TrimPrefix(item, wslScratchEnv+"=")
		}
	}
	return ""
}

// GuestTTY reports that a prepared WSL command creates its PTY inside the
// isolated guest, so the Windows caller should connect ordinary pipes.
func GuestTTY(prepared *exec.Cmd) bool {
	if prepared == nil || !strings.EqualFold(filepath.Base(prepared.Path), "wsl.exe") {
		return false
	}
	for _, item := range prepared.Env {
		if item == wslTTYEnv+"=1" {
			return true
		}
	}
	return false
}
