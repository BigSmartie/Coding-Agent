package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/sandbox"
	"github.com/BigSmartie/Coding-Agent/internal/workspace"
	"github.com/creack/pty"
)

const (
	maxActive = 4
	maxJobs   = 8
	maxOutput = 1 << 20
)

type Permission interface {
	EnsurePathAccess(context.Context, string, string) error
	EnsureCommand(context.Context, string, []string, string) error
}

type Event struct {
	Kind    string
	JobID   string
	Command string
}

type Spec struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	CWD     string   `json:"cwd,omitempty"`
	TTY     bool     `json:"tty,omitempty"`
}

type Snapshot struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	TTY        bool      `json:"tty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	ExitCode   int       `json:"exitCode"`
	OutputBase int64     `json:"outputBase"`
	OutputEnd  int64     `json:"outputEnd"`
}

type ReadResult struct {
	Snapshot   Snapshot `json:"job"`
	Output     string   `json:"output"`
	NextOffset int64    `json:"nextOffset"`
	Truncated  bool     `json:"truncated"`
}

type job struct {
	mu            sync.Mutex
	inputMu       sync.Mutex
	id            string
	status        string
	tty           bool
	started       time.Time
	finished      time.Time
	exitCode      int
	quotaExceeded bool
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	cancel        context.CancelFunc
	cleanup       func()
	done          chan struct{}
	output        outputBuffer
}

type Manager struct {
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	workspace  string
	sessionID  string
	permission Permission
	onEvent    func(Event) error
	jobs       map[string]*job
	active     int
	closed     bool
	recoverMu  sync.Mutex
	recovered  bool
}

func New(workspaceRoot, sessionID string, permission Permission, onEvent func(Event) error) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{ctx: ctx, cancel: cancel, workspace: workspaceRoot, sessionID: sessionID, permission: permission, onEvent: onEvent, jobs: map[string]*job{}}
}

func (m *Manager) Start(ctx context.Context, spec Spec) (Snapshot, error) {
	if m.permission == nil {
		return Snapshot{}, fmt.Errorf("background command requires explicit approval")
	}
	cwd, err := workspace.Resolve(ctx, m.workspace, defaultCWD(spec.CWD), "command_cwd", m.permission)
	if err != nil {
		return Snapshot{}, err
	}
	if err := m.permission.EnsureCommand(ctx, spec.Command, spec.Args, cwd); err != nil {
		return Snapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if m.sessionID != "" {
		m.recoverMu.Lock()
		if !m.recovered {
			if err := sandbox.CleanupSessionContainers(ctx, m.workspace, m.sessionID); err != nil {
				m.recoverMu.Unlock()
				return Snapshot{}, err
			}
			m.recovered = true
		}
		m.recoverMu.Unlock()
	}
	m.mu.Lock()
	if m.closed || m.active >= maxActive || len(m.jobs) >= maxJobs {
		m.mu.Unlock()
		return Snapshot{}, fmt.Errorf("background job limit reached or manager closed")
	}
	m.active++
	m.mu.Unlock()
	reserved := true
	defer func() {
		if reserved {
			m.mu.Lock()
			m.active--
			m.mu.Unlock()
		}
	}()
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Snapshot{}, err
	}
	id := hex.EncodeToString(nonce[:])
	jobCtx, cancel := context.WithTimeout(m.ctx, 10*time.Minute)
	cmd, cleanup, err := sandbox.Prepare(jobCtx, sandbox.Options{Workspace: m.workspace, CWD: cwd, Command: spec.Command, Args: spec.Args, TTY: spec.TTY, RetainContainer: true, SessionID: m.sessionID})
	if err != nil {
		cancel()
		return Snapshot{}, err
	}
	var stdin io.WriteCloser
	var stdout, stderr io.ReadCloser
	var slave io.Closer
	if spec.TTY {
		master, terminal, ptyErr := pty.Open()
		if ptyErr != nil {
			cancel()
			cleanup()
			return Snapshot{}, fmt.Errorf("host PTY is unavailable: %w", ptyErr)
		}
		stdin, stdout, slave = master, master, terminal
		cmd.Stdin, cmd.Stdout, cmd.Stderr = terminal, terminal, terminal
	} else {
		stdin, err = cmd.StdinPipe()
		if err == nil {
			stdout, err = cmd.StdoutPipe()
		}
		if err == nil {
			stderr, err = cmd.StderrPipe()
		}
		if err != nil {
			cancel()
			cleanup()
			return Snapshot{}, err
		}
	}
	if err := m.emit(Event{Kind: "job_started", JobID: id, Command: spec.Command}); err != nil {
		if slave != nil {
			_ = slave.Close()
		}
		if spec.TTY {
			_ = stdin.Close()
		}
		cancel()
		cleanup()
		return Snapshot{}, err
	}
	if err := cmd.Start(); err != nil {
		if slave != nil {
			_ = slave.Close()
		}
		if spec.TTY {
			_ = stdin.Close()
		}
		_ = m.emit(Event{Kind: "job_failed", JobID: id})
		cancel()
		cleanup()
		return Snapshot{}, err
	}
	if slave != nil {
		_ = slave.Close()
	}
	j := &job{id: id, status: "running", tty: spec.TTY, started: time.Now().UTC(), exitCode: -1, cmd: cmd, stdin: stdin, cancel: cancel, cleanup: cleanup, done: make(chan struct{})}
	m.mu.Lock()
	m.jobs[id] = j
	m.mu.Unlock()
	reserved = false
	go m.wait(j, stdout, stderr, jobCtx)
	go m.monitorScratch(j)
	return j.snapshot(), nil
}

func (m *Manager) monitorScratch(j *job) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-j.done:
			return
		case <-ticker.C:
			if err := sandbox.CheckJobScratchBounds(j.cmd); err != nil {
				j.mu.Lock()
				j.quotaExceeded = true
				j.mu.Unlock()
				_, _ = io.WriteString(&j.output, "\n[job scratch limit: "+err.Error()+"]\n")
				j.cancel()
				return
			}
		}
	}
}

func (m *Manager) wait(j *job, stdout, stderr io.ReadCloser, ctx context.Context) {
	var readers sync.WaitGroup
	readers.Add(1)
	go func() { defer readers.Done(); _, _ = io.Copy(&j.output, stdout) }()
	if stderr != nil {
		readers.Add(1)
		go func() { defer readers.Done(); _, _ = io.Copy(&j.output, stderr) }()
	}
	readers.Wait()
	err := j.cmd.Wait()
	_ = j.stdin.Close()
	j.mu.Lock()
	j.finished = time.Now().UTC()
	if j.quotaExceeded {
		j.status = "failed"
	} else if ctx.Err() != nil {
		j.status = "canceled"
	} else if err != nil {
		j.status = "failed"
	} else {
		j.status = "completed"
	}
	if err == nil {
		j.exitCode = 0
	} else if exit := new(exec.ExitError); errors.As(err, &exit) {
		j.exitCode = exit.ExitCode()
	}
	status := j.status
	j.mu.Unlock()
	_ = m.emit(Event{Kind: "job_" + status, JobID: j.id})
	if status != "completed" {
		j.cleanup()
	}
	m.mu.Lock()
	m.active--
	m.mu.Unlock()
	close(j.done)
}

func (m *Manager) Poll(id string) (Snapshot, error) {
	j, err := m.find(id)
	if err != nil {
		return Snapshot{}, err
	}
	return j.snapshot(), nil
}

func (m *Manager) List() []Snapshot {
	m.mu.Lock()
	jobs := make([]*job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	out := make([]Snapshot, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.snapshot())
	}
	sort.Slice(out, func(i, k int) bool { return out[i].StartedAt.Before(out[k].StartedAt) })
	return out
}

func (m *Manager) Read(id string, offset int64, limit int) (ReadResult, error) {
	j, err := m.find(id)
	if err != nil {
		return ReadResult{}, err
	}
	if limit <= 0 || limit > 64<<10 {
		limit = 64 << 10
	}
	output, next, truncated := j.output.Read(offset, limit)
	return ReadResult{Snapshot: j.snapshot(), Output: output, NextOffset: next, Truncated: truncated}, nil
}

func (m *Manager) Write(ctx context.Context, id string, data string) error {
	if len(data) > 4096 {
		return fmt.Errorf("job input exceeds 4096 bytes")
	}
	j, err := m.find(id)
	if err != nil {
		return err
	}
	if j.snapshot().Status != "running" {
		return fmt.Errorf("job is not running")
	}
	if err := m.emit(Event{Kind: "job_input", JobID: id}); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		j.inputMu.Lock()
		defer j.inputMu.Unlock()
		_, err := io.WriteString(j.stdin, data)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		j.cancel()
		return ctx.Err()
	case <-time.After(2 * time.Second):
		j.cancel()
		return fmt.Errorf("job input timed out; job canceled")
	}
}

func (m *Manager) Cancel(id string) error {
	j, err := m.find(id)
	if err != nil {
		return err
	}
	if j.snapshot().Status != "running" {
		return nil
	}
	if err := m.emit(Event{Kind: "job_cancel_requested", JobID: id}); err != nil {
		return err
	}
	j.cancel()
	return nil
}

func (m *Manager) ReadArtifact(ctx context.Context, id, relative string) ([]byte, error) {
	j, err := m.find(id)
	if err != nil {
		return nil, err
	}
	if j.snapshot().Status != "completed" {
		return nil, fmt.Errorf("artifacts can be exported only after a successful job")
	}
	return sandbox.CopyArtifact(ctx, j.cmd, relative)
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.cancel()
	jobs := make([]*job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	deadline := time.After(15 * time.Second)
	for _, j := range jobs {
		select {
		case <-j.done:
		case <-deadline:
		}
		j.cleanup()
	}
}

func (m *Manager) find(id string) (*job, error) {
	m.mu.Lock()
	j := m.jobs[id]
	m.mu.Unlock()
	if j == nil {
		return nil, fmt.Errorf("unknown job id")
	}
	return j, nil
}

func (m *Manager) emit(event Event) error {
	if m.onEvent != nil {
		return m.onEvent(event)
	}
	return nil
}

func (j *job) snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	base, end := j.output.Bounds()
	return Snapshot{ID: j.id, Status: j.status, TTY: j.tty, StartedAt: j.started, FinishedAt: j.finished, ExitCode: j.exitCode, OutputBase: base, OutputEnd: end}
}

func defaultCWD(cwd string) string {
	if cwd == "" {
		return "."
	}
	return cwd
}

type outputBuffer struct {
	mu   sync.Mutex
	data []byte
	base int64
}

func (b *outputBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.data = append(b.data, p...)
	if len(b.data) > maxOutput {
		drop := len(b.data) - maxOutput
		b.base += int64(drop)
		b.data = append([]byte(nil), b.data[drop:]...)
	}
	return n, nil
}

func (b *outputBuffer) Read(offset int64, limit int) (string, int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	truncated := offset < b.base
	if offset < b.base {
		offset = b.base
	}
	end := b.base + int64(len(b.data))
	if offset > end {
		offset = end
	}
	count := min(limit, int(end-offset))
	return string(b.data[offset-b.base : int(offset-b.base)+count]), offset + int64(count), truncated
}

func (b *outputBuffer) Bounds() (int64, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.base, b.base + int64(len(b.data))
}
