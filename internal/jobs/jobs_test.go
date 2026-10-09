package jobs

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

type allowPermission struct{}

func (allowPermission) EnsurePathAccess(context.Context, string, string) error        { return nil }
func (allowPermission) EnsureCommand(context.Context, string, []string, string) error { return nil }

func TestOutputBufferKeepsBoundedTailAndOffsets(t *testing.T) {
	var buffer outputBuffer
	_, _ = buffer.Write([]byte(strings.Repeat("a", maxOutput)))
	_, _ = buffer.Write([]byte("tail"))
	base, end := buffer.Bounds()
	if base != 4 || end != int64(maxOutput+4) {
		t.Fatalf("wrong ring bounds: %d %d", base, end)
	}
	last, next, truncated := buffer.Read(int64(maxOutput), 32)
	if last != "tail" || next != end || truncated {
		t.Fatalf("wrong tail: %q %d %v", last, next, truncated)
	}
	_, _, truncated = buffer.Read(0, 16)
	if !truncated {
		t.Fatal("lost truncation marker")
	}
}

func TestJobsRequireApprovalBeforeDocker(t *testing.T) {
	m := New(t.TempDir(), "session-1", nil, nil)
	defer m.Close()
	if _, err := m.Start(context.Background(), Spec{Command: "/bin/sh"}); err == nil || !strings.Contains(err.Error(), "approval") {
		t.Fatalf("unapproved job started: %v", err)
	}
}

func TestIntegrationBackgroundJobPTYCancelAndArtifact(t *testing.T) {
	if os.Getenv("MY_CODE_SANDBOX_INTEGRATION") != "1" || os.Getenv("MY_CODE_SANDBOX_IMAGE") == "" {
		t.Skip("Docker integration is opt-in")
	}
	m := New(t.TempDir(), "test-session", allowPermission{}, nil)
	defer m.Close()
	job, err := m.Start(context.Background(), Spec{Command: "/bin/sh", Args: []string{"-c", "printf artifact > generated.txt; echo completed"}})
	if err != nil {
		t.Fatal(err)
	}
	job = waitStatus(t, m, job.ID, "completed")
	read, err := m.Read(job.ID, 0, 1024)
	if err != nil || !strings.Contains(read.Output, "completed") {
		t.Fatalf("job output missing: %#v, %v", read, err)
	}
	data, err := m.ReadArtifact(context.Background(), job.ID, "generated.txt")
	if err != nil || string(data) != "artifact" {
		t.Fatalf("artifact missing: %q, %v", data, err)
	}
	interactive, err := m.Start(context.Background(), Spec{Command: "/bin/sh", TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(context.Background(), interactive.ID, "echo pty-ready\nexit\n"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, interactive.ID, "completed")
	read, err = m.Read(interactive.ID, 0, 1024)
	if err != nil || !strings.Contains(read.Output, "pty-ready") {
		t.Fatalf("PTY output missing: %#v, %v", read, err)
	}
	long, err := m.Start(context.Background(), Spec{Command: "/bin/sh", Args: []string{"-c", "sleep 120 & echo started; wait"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Cancel(long.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, long.ID, "canceled")
}

func TestIntegrationWSLGuestPTY(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("MY_CODE_WSL_INTEGRATION") != "1" {
		t.Skip("Windows WSL2 integration is opt-in")
	}
	t.Setenv("MY_CODE_SANDBOX_BACKEND", "wsl")
	m := New(t.TempDir(), "wsl-pty-test", allowPermission{}, nil)
	defer m.Close()
	job, err := m.Start(context.Background(), Spec{Command: "/bin/sh", Args: []string{"-c", `test -t 0 && echo tty-ready; read line; printf 'reply:%s\n' "$line"`}, TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(context.Background(), job.ID, "hello\n"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, job.ID, "completed")
	read, err := m.Read(job.ID, 0, 4096)
	if err != nil || !strings.Contains(read.Output, "tty-ready") || !strings.Contains(read.Output, "reply:hello") {
		t.Fatalf("WSL guest PTY interaction failed: %#v, %v", read, err)
	}
	long, err := m.Start(context.Background(), Spec{Command: "/bin/sh", Args: []string{"-c", "echo running; exec sleep 30"}, TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	ready := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		output, err := m.Read(long.ID, 0, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.Output, "running") {
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("WSL guest PTY job did not start")
	}
	if err := m.Cancel(long.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, long.ID, "canceled")
}

func waitStatus(t *testing.T, m *Manager, id, want string) Snapshot {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, err := m.Poll(id)
		if err != nil {
			t.Fatal(err)
		}
		if status.Status == want {
			return status
		}
		if status.Status != "running" {
			t.Fatalf("job ended unexpectedly: %#v", status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", id, want)
	return Snapshot{}
}
