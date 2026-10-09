// Package sandbox executes untrusted programs over a filtered, disposable
// workspace snapshot in an explicit Docker or Windows WSL2/Bubblewrap backend.
// It never falls back to host execution or downloads an image automatically.
package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

const MaxOutputBytes = 64 * 1024
const maxSnapshotBytes = 256 * 1024 * 1024
const maxSnapshotFiles = 25000
const SnapshotNotice = "Sandbox workspace changes are temporary. Use reviewed file tools to persist edits. Network access is disabled."

type Options struct {
	Workspace string
	CWD       string
	Command   string
	Args      []string
	// Env is explicit server configuration. Host environment is never forwarded.
	Env             map[string]string
	TTY             bool
	RetainContainer bool
	SessionID       string
	scratch         string
	jobRoot         string
	guestTTY        bool
}

// Prepare returns a command ready for Run or Start and an idempotent cleanup.
// cleanup must be called after Wait, including startup/handshake failure paths.
// The caller must supply a context whose cancellation lasts for process lifetime.
func Prepare(ctx context.Context, options Options) (*exec.Cmd, func(), error) {
	switch os.Getenv("MYTHOS_CODE_SANDBOX_BACKEND") {
	case "", "docker":
	case "wsl":
		return prepareWSL(ctx, options)
	default:
		return nil, nil, errors.New("unsupported sandbox backend; choose docker or wsl explicitly")
	}
	image := strings.TrimSpace(os.Getenv("MYTHOS_CODE_SANDBOX_IMAGE"))
	if image == "" {
		return nil, nil, errors.New("sandbox unavailable: set MYTHOS_CODE_SANDBOX_IMAGE to an already-installed trusted Linux Docker image; host execution is disabled")
	}
	if strings.HasPrefix(image, "-") || strings.ContainsAny(image, "\x00\r\n\t ") {
		return nil, nil, errors.New("invalid sandbox image")
	}
	root, err := workspace.Canonical(options.Workspace)
	if err != nil {
		return nil, nil, err
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return nil, nil, errors.New("sandbox unavailable: Docker CLI is required; host execution is disabled")
	}
	docker, err = filepath.Abs(docker)
	if err != nil {
		return nil, nil, err
	}
	if workspace.Within(root, docker) {
		return nil, nil, errors.New("sandbox refuses a Docker executable inside the untrusted workspace")
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	check := exec.CommandContext(checkCtx, docker, "image", "inspect", "--format", "{{.Id}} {{.Os}}", "--", image)
	check.Env = HostEnvironment()
	checkOutput := &LimitedBuffer{Limit: 4096}
	check.Stdout, check.Stderr = checkOutput, checkOutput
	err = check.Run()
	cancel()
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox unavailable: Docker must be running and image %q must already exist locally (no image pull was attempted)", image)
	}
	fields := strings.Fields(checkOutput.String())
	if len(fields) != 2 || !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(fields[0]) || fields[1] != "linux" {
		return nil, nil, errors.New("sandbox requires a trusted local Linux container image")
	}
	options.guestTTY = options.TTY && runtime.GOOS == "windows"
	if options.guestTTY {
		probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
		probe := exec.CommandContext(probeCtx, docker, "run", "--pull=never", "--rm", "--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "--user=65534:65534", "--env", "PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "--entrypoint", "/bin/sh", fields[0], "-c", "command -v python3 >/dev/null")
		probe.Env = HostEnvironment()
		err = probe.Run()
		probeCancel()
		if err != nil {
			return nil, nil, errors.New("Windows Docker PTY jobs require python3 in the trusted local image")
		}
	}
	snapshot, err := makeSnapshot(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		os.RemoveAll(filepath.Dir(snapshot))
		return nil, nil, err
	}
	name := "mythoscode-" + hex.EncodeToString(nonce[:])
	if options.RetainContainer {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`).MatchString(options.SessionID) {
			os.RemoveAll(filepath.Dir(snapshot))
			return nil, nil, errors.New("retained sandbox job requires a valid session id")
		}
		options.jobRoot, err = os.MkdirTemp("", "mythoscode-jobs-"+options.SessionID+"-")
		if err != nil {
			os.RemoveAll(filepath.Dir(snapshot))
			return nil, nil, err
		}
		options.scratch = filepath.Join(options.jobRoot, "workspace")
		if err = os.Mkdir(options.scratch, 0o777); err == nil {
			err = os.Chmod(options.scratch, 0o1777)
		}
		if err != nil {
			os.RemoveAll(options.jobRoot)
			os.RemoveAll(filepath.Dir(snapshot))
			return nil, nil, err
		}
	}
	argv, err := commandArgs(options, root, snapshot, fields[0], name)
	if err != nil {
		if options.jobRoot != "" {
			os.RemoveAll(options.jobRoot)
		}
		os.RemoveAll(filepath.Dir(snapshot))
		return nil, nil, err
	}
	stop := func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		stopper := exec.CommandContext(stopCtx, docker, "rm", "--force", name)
		stopper.Env = HostEnvironment()
		_ = stopper.Run()
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			stop()
			_ = os.RemoveAll(filepath.Dir(snapshot))
			if options.jobRoot != "" {
				_ = os.RemoveAll(options.jobRoot)
			}
		})
	}
	cmd := exec.CommandContext(ctx, docker, argv...)
	cmd.Env = HostEnvironment()
	if options.guestTTY {
		cmd.Env = append(cmd.Env, dockerTTYEnv+"=1")
	}
	cmd.Cancel = func() error {
		stop()
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 12 * time.Second
	return cmd, cleanup, nil
}

func commandArgs(options Options, root, snapshot, image, name string) ([]string, error) {
	if options.Command == "" || strings.HasPrefix(options.Command, "-") || strings.ContainsAny(options.Command, "\x00\r\n") {
		return nil, errors.New("command must be an explicit executable; pass arguments separately")
	}
	for _, arg := range options.Args {
		if strings.ContainsRune(arg, '\x00') {
			return nil, errors.New("command argument contains NUL")
		}
	}
	cwd := options.CWD
	if cwd == "" {
		cwd = root
	}
	canonicalRoot, err := workspace.Canonical(root)
	if err != nil {
		return nil, err
	}
	target, err := workspace.Resolve(context.Background(), root, cwd, "command_cwd", nil)
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
	if strings.Contains(snapshot, ",") {
		return nil, errors.New("sandbox temporary path cannot contain a comma")
	}
	args := []string{"run", "--pull=never", "--name", name, "--interactive", "--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "--log-driver=none", "--pids-limit=128", "--memory=1g", "--cpus=2", "--ulimit", "fsize=536870912:536870912", "--user=65534:65534", "--tmpfs=/tmp:rw,nosuid,nodev,size=256m", "--mount", "type=bind,src=" + snapshot + ",dst=/input,readonly", "--workdir", "/workspace", "--env", "HOME=/tmp", "--env", "TMPDIR=/tmp", "--env", "PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "--env", "GOCACHE=/tmp/go-build", "--env", "GOTOOLCHAIN=local"}
	if options.RetainContainer {
		if options.scratch == "" || strings.Contains(options.scratch, ",") {
			return nil, errors.New("invalid job scratch path")
		}
		args = append(args, "--mount", "type=bind,src="+options.scratch+",dst=/workspace")
	} else {
		args = append(args, "--tmpfs=/workspace:rw,nosuid,nodev,size=512m,mode=1777")
	}
	if !options.RetainContainer {
		args = append(args, "--rm")
	}
	if options.TTY && !options.guestTTY {
		args = append(args, "--tty")
	}
	if options.SessionID != "" {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`).MatchString(options.SessionID) {
			return nil, errors.New("invalid sandbox session id")
		}
		args = append(args, "--label", "mythoscode.session="+options.SessionID)
	}
	keys := make([]string, 0, len(options.Env))
	for key := range options.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(key) || strings.ContainsRune(options.Env[key], '\x00') {
			return nil, errors.New("invalid explicit sandbox environment variable")
		}
		// These keys may change runtime loaders or inherit unintended host credentials.
		upper := strings.ToUpper(key)
		if upper == "PATH" || upper == "HOME" || upper == "TMPDIR" || strings.HasPrefix(upper, "LD_") || strings.HasPrefix(upper, "DYLD_") || upper == "NODE_OPTIONS" || upper == "PYTHONPATH" || strings.Contains(upper, "API_KEY") || upper == "OPENAI_API_BASE" || upper == "ANTHROPIC_AUTH_TOKEN" || strings.HasPrefix(upper, "DOCKER_") {
			return nil, fmt.Errorf("sandbox environment variable %s is reserved", key)
		}
		args = append(args, "--env", key+"="+options.Env[key])
	}
	// The bootstrap is fixed code. The chosen cwd, executable and each argument
	// are positional parameters, never interpolated into shell source.
	bootstrap := `set -eu; cp -R /input/. /workspace/; cd "$1"; shift; exec "$@"`
	if options.guestTTY {
		// Docker Desktop on Windows has no host PTY; the trusted guest image
		// allocates one and bridges it through Docker's ordinary stdin/stdout.
		bootstrap = `set -eu; cp -R /input/. /workspace/; cd "$1"; shift; exec python3 -c 'import os,pty,sys; sys.exit(os.waitstatus_to_exitcode(pty.spawn(sys.argv[1:])))' "$@"`
	}
	args = append(args, "--entrypoint", "/bin/sh", image, "-c", bootstrap, "mythoscode-sandbox", path.Join("/workspace", filepath.ToSlash(relative)), options.Command)
	return append(args, options.Args...), nil
}

// CleanupSessionContainers removes bounded, labelled containers left after an
// unclean process exit. A session lock must be held by the caller.
func CleanupSessionContainers(ctx context.Context, workspaceRoot, sessionID string) error {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`).MatchString(sessionID) {
		return fmt.Errorf("invalid sandbox session id")
	}
	if os.Getenv("MYTHOS_CODE_SANDBOX_BACKEND") == "wsl" {
		return cleanupSessionScratch(sessionID)
	}
	docker, err := exec.LookPath("docker")
	if errors.Is(err, exec.ErrNotFound) {
		return cleanupSessionScratch(sessionID)
	}
	if err != nil {
		return err
	}
	docker, err = filepath.Abs(docker)
	if err != nil {
		return err
	}
	root, err := workspace.Canonical(workspaceRoot)
	if err != nil {
		return err
	}
	if workspace.Within(root, docker) {
		return fmt.Errorf("sandbox refuses a Docker executable inside the untrusted workspace")
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	list := exec.CommandContext(cleanupCtx, docker, "ps", "-aq", "--filter", "label=mythoscode.session="+sessionID)
	list.Env = HostEnvironment()
	output := &LimitedBuffer{Limit: 4096}
	list.Stdout, list.Stderr = output, output
	if err := list.Run(); err != nil {
		return fmt.Errorf("cannot inspect previous sandbox jobs: %w", err)
	}
	ids := strings.Fields(output.String())
	if len(ids) > 32 {
		return fmt.Errorf("too many previous sandbox containers")
	}
	for _, id := range ids {
		if !regexp.MustCompile(`^[a-f0-9]{12,64}$`).MatchString(id) {
			return fmt.Errorf("invalid previous sandbox container id")
		}
		remove := exec.CommandContext(cleanupCtx, docker, "rm", "--force", id)
		remove.Env = HostEnvironment()
		remove.Stdout, remove.Stderr = io.Discard, io.Discard
		if err := remove.Run(); err != nil {
			return fmt.Errorf("cannot remove previous sandbox container: %w", err)
		}
	}
	return cleanupSessionScratch(sessionID)
}

func cleanupSessionScratch(sessionID string) error {
	temp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(temp)
	if err != nil {
		return err
	}
	prefix := "mythoscode-jobs-" + sessionID + "-"
	removed := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		removed++
		if removed > 32 {
			return fmt.Errorf("too many previous sandbox scratch directories")
		}
		target, err := filepath.Abs(filepath.Join(temp, entry.Name()))
		if err != nil || !workspace.Within(temp, target) || target == temp {
			return fmt.Errorf("invalid previous sandbox scratch path")
		}
		info, err := os.Lstat(target)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid previous sandbox scratch directory")
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	return nil
}

// CopyArtifact reads one regular file from a retained job's isolated scratch
// mount. It never writes into the host workspace; callers review/export.
func CopyArtifact(ctx context.Context, prepared *exec.Cmd, relative string) ([]byte, error) {
	if prepared == nil || strings.TrimSpace(relative) == "" || strings.ContainsAny(relative, "\\:\x00\r\n") || strings.HasPrefix(relative, "/") {
		return nil, fmt.Errorf("artifact path must be relative to sandbox workspace")
	}
	clean := path.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != relative {
		return nil, fmt.Errorf("artifact path escapes sandbox workspace")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, scratch := "", jobScratch(prepared)
	for i, arg := range prepared.Args {
		if arg == "--name" && i+1 < len(prepared.Args) {
			name = prepared.Args[i+1]
		}
	}
	if wslJobScratch(prepared) == "" && !regexp.MustCompile(`^mythoscode-[a-f0-9]{24}$`).MatchString(name) {
		return nil, fmt.Errorf("invalid sandbox container identity")
	}
	if filepath.Base(scratch) != "workspace" || !strings.HasPrefix(filepath.Base(filepath.Dir(scratch)), "mythoscode-jobs-") || !workspace.Within(os.TempDir(), scratch) {
		return nil, fmt.Errorf("invalid sandbox scratch identity")
	}
	access, err := workspace.Open(scratch)
	if err != nil {
		return nil, err
	}
	defer access.Close()
	return access.ReadFile(filepath.FromSlash(clean), 1<<20)
}

func jobScratch(prepared *exec.Cmd) string {
	if prepared == nil {
		return ""
	}
	if scratch := wslJobScratch(prepared); scratch != "" {
		return scratch
	}
	for i, arg := range prepared.Args {
		if arg == "--mount" && i+1 < len(prepared.Args) {
			mount := prepared.Args[i+1]
			if strings.HasPrefix(mount, "type=bind,src=") && strings.HasSuffix(mount, ",dst=/workspace") {
				return strings.TrimSuffix(strings.TrimPrefix(mount, "type=bind,src="), ",dst=/workspace")
			}
		}
	}
	return ""
}

// CheckJobScratchBounds is a best-effort disk guard for retained job files.
// Docker also enforces a per-file fsize limit; the scan never follows links.
func CheckJobScratchBounds(prepared *exec.Cmd) error {
	scratch := jobScratch(prepared)
	if filepath.Base(scratch) != "workspace" || !strings.HasPrefix(filepath.Base(filepath.Dir(scratch)), "mythoscode-jobs-") || !workspace.Within(os.TempDir(), scratch) {
		return fmt.Errorf("invalid job scratch identity")
	}
	var total int64
	entries := 0
	err := filepath.WalkDir(scratch, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > maxSnapshotFiles {
			return fmt.Errorf("job scratch entry limit reached")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			total += info.Size()
			if total > 512<<20 {
				return fmt.Errorf("job scratch exceeds 512 MiB")
			}
		}
		return nil
	})
	return err
}

// HostEnvironment is deliberately small. It supports the installed Docker or
// WSL CLI without handing model credentials, proxy credentials, or loaders to it.
func HostEnvironment() []string {
	var out []string
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "TEMP", "TMP", "USERPROFILE", "HOME", "LOCALAPPDATA", "APPDATA"} {
		if value, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+value)
		}
	}
	return out
}

func makeSnapshot(ctx context.Context, cwd string) (string, error) {
	access, err := workspace.Open(cwd)
	if err != nil {
		return "", err
	}
	defer access.Close()
	containerDirectory, err := os.MkdirTemp("", "mythoscode-sandbox-")
	if err != nil {
		return "", err
	}
	directory := filepath.Join(containerDirectory, "workspace")
	if err := os.Mkdir(directory, 0o755); err != nil {
		_ = os.RemoveAll(containerDirectory)
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(containerDirectory)
		}
	}()
	// The private outer directory protects this bounded snapshot on the host.
	// The container receives this directory read-only and copies into bounded tmpfs.
	if err := os.Chmod(directory, 0o755); err != nil {
		return "", err
	}
	var total int64
	count := 0
	stack := []string{"."}
	for len(stack) > 0 {
		if err = ctx.Err(); err != nil {
			break
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		var entries []os.DirEntry
		entries, err = access.ReadDir(current, maxSnapshotFiles+1)
		if err != nil {
			break
		}
		for _, entry := range entries {
			name := filepath.Join(current, entry.Name())
			if workspace.Protected(name) || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			destination := filepath.Join(directory, name)
			count++
			if count > maxSnapshotFiles {
				err = errors.New("sandbox snapshot exceeds 25000 entry limit")
				break
			}
			if entry.IsDir() {
				if err = os.Mkdir(destination, 0o755); err != nil {
					break
				}
				if err = os.Chmod(destination, 0o755); err != nil {
					break
				}
				stack = append(stack, name)
				continue
			}
			if !entry.Type().IsRegular() {
				continue
			}
			var data []byte
			data, err = access.ReadFile(name, 32*1024*1024)
			if err != nil {
				break
			}
			total += int64(len(data))
			if total > maxSnapshotBytes {
				err = errors.New("sandbox snapshot exceeds 256 MiB limit")
				break
			}
			if err = os.WriteFile(destination, data, 0o755); err != nil {
				break
			}
			if err = os.Chmod(destination, 0o755); err != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("cannot create safe sandbox snapshot: %w", err)
	}
	complete = true
	return directory, nil
}

// LimitedBuffer discards overflow while reporting successful writes, allowing
// children to finish without unbounded model-visible output or memory growth.
type LimitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	Limit     int
	truncated bool
}

func (b *LimitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	length := len(p)
	remaining := b.Limit - b.buf.Len()
	if remaining < 0 {
		remaining = 0
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buf.Write(p)
	return length, nil
}
func (b *LimitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := b.buf.String()
	if b.truncated {
		text += "\n[output truncated]"
	}
	return text
}
