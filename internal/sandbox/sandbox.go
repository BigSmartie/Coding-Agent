// Package sandbox executes untrusted programs only in a resource-limited Linux
// Docker container over a filtered, disposable workspace snapshot. It never
// falls back to host execution and never downloads an image automatically.
package sandbox

import (
	"archive/tar"
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
}

// Prepare returns a command ready for Run or Start and an idempotent cleanup.
// cleanup must be called after Wait, including startup/handshake failure paths.
// The caller must supply a context whose cancellation lasts for process lifetime.
func Prepare(ctx context.Context, options Options) (*exec.Cmd, func(), error) {
	image := strings.TrimSpace(os.Getenv("MY_CODE_SANDBOX_IMAGE"))
	if image == "" {
		return nil, nil, errors.New("sandbox unavailable: set MY_CODE_SANDBOX_IMAGE to an already-installed trusted Linux Docker image; host execution is disabled")
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
	snapshot, err := makeSnapshot(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		os.RemoveAll(filepath.Dir(snapshot))
		return nil, nil, err
	}
	name := "mycode-" + hex.EncodeToString(nonce[:])
	argv, err := commandArgs(options, root, snapshot, fields[0], name)
	if err != nil {
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
	cleanup := func() { once.Do(func() { stop(); _ = os.RemoveAll(filepath.Dir(snapshot)) }) }
	cmd := exec.CommandContext(ctx, docker, argv...)
	cmd.Env = HostEnvironment()
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
	args := []string{"run", "--pull=never", "--name", name, "--interactive", "--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "--log-driver=none", "--pids-limit=128", "--memory=1g", "--cpus=2", "--user=65534:65534", "--tmpfs=/tmp:rw,nosuid,nodev,size=256m", "--tmpfs=/workspace:rw,nosuid,nodev,size=512m,mode=1777", "--mount", "type=bind,src=" + snapshot + ",dst=/input,readonly", "--workdir", "/workspace", "--env", "HOME=/tmp", "--env", "TMPDIR=/tmp", "--env", "PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "--env", "GOCACHE=/tmp/go-build", "--env", "GOTOOLCHAIN=local"}
	if !options.RetainContainer {
		args = append(args, "--rm")
	}
	if options.TTY {
		args = append(args, "--tty")
	}
	if options.SessionID != "" {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`).MatchString(options.SessionID) {
			return nil, errors.New("invalid sandbox session id")
		}
		args = append(args, "--label", "mycode.session="+options.SessionID)
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
	const bootstrap = `set -eu; cp -R /input/. /workspace/; cd "$1"; shift; exec "$@"`
	args = append(args, "--entrypoint", "/bin/sh", image, "-c", bootstrap, "mycode-sandbox", path.Join("/workspace", filepath.ToSlash(relative)), options.Command)
	return append(args, options.Args...), nil
}

// CleanupSessionContainers removes bounded, labelled containers left after an
// unclean process exit. A session lock must be held by the caller.
func CleanupSessionContainers(ctx context.Context, workspaceRoot, sessionID string) error {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`).MatchString(sessionID) {
		return fmt.Errorf("invalid sandbox session id")
	}
	docker, err := exec.LookPath("docker")
	if errors.Is(err, exec.ErrNotFound) {
		return nil
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
	list := exec.CommandContext(cleanupCtx, docker, "ps", "-aq", "--filter", "label=mycode.session="+sessionID)
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
	return nil
}

// CopyArtifact reads one regular file from a retained sandbox container. It
// never writes into the host workspace; callers must separately review/export.
func CopyArtifact(ctx context.Context, prepared *exec.Cmd, relative string) ([]byte, error) {
	if prepared == nil || strings.TrimSpace(relative) == "" || strings.ContainsAny(relative, "\\:\x00\r\n") || strings.HasPrefix(relative, "/") {
		return nil, fmt.Errorf("artifact path must be relative to sandbox workspace")
	}
	clean := path.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != relative {
		return nil, fmt.Errorf("artifact path escapes sandbox workspace")
	}
	name := ""
	for i, arg := range prepared.Args {
		if arg == "--name" && i+1 < len(prepared.Args) {
			name = prepared.Args[i+1]
			break
		}
	}
	if !regexp.MustCompile(`^mycode-[a-f0-9]{24}$`).MatchString(name) {
		return nil, fmt.Errorf("invalid sandbox container identity")
	}
	copyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(copyCtx, prepared.Path, "cp", name+":/workspace/"+clean, "-")
	cmd.Env = HostEnvironment()
	stderr := &LimitedBuffer{Limit: 4096}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	reader := tar.NewReader(stdout)
	header, err := reader.Next()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("sandbox artifact is unavailable")
	}
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA || header.Size < 0 || header.Size > 1<<20 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("sandbox artifact must be a regular file no larger than 1 MiB")
	}
	data := make([]byte, header.Size)
	if _, err := io.ReadFull(reader, data); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	if _, err := reader.Next(); err != io.EOF {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("sandbox artifact copy contained additional entries")
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("sandbox artifact copy failed: %w", err)
	}
	return data, nil
}

// HostEnvironment is deliberately small. It supports the installed Docker CLI
// without handing model credentials, proxy credentials, or loader variables to it.
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
	containerDirectory, err := os.MkdirTemp("", "mycode-sandbox-")
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
