package session

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"github.com/BigSmartie/Coding-Agent/internal/taskstate"
)

type TaskTracker struct {
	mu      sync.Mutex
	tasks   []taskstate.Task
	journal *Journal
	ctx     context.Context
}

func NewTaskTracker(initial []taskstate.Task, journal *Journal, ctx context.Context) *TaskTracker {
	return &TaskTracker{tasks: append([]taskstate.Task(nil), initial...), journal: journal, ctx: ctx}
}

func (t *TaskTracker) UpsertTask(_ context.Context, task taskstate.Task) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	task.UpdatedAt = time.Now().UTC()
	task.Title = safety.Redact(t.ctx, task.Title)
	task.Details = safety.Redact(t.ctx, task.Details)
	next, err := taskstate.Upsert(t.tasks, task)
	if err != nil {
		return err
	}
	if t.journal != nil {
		if _, err := t.journal.Append(Event{Kind: EventTaskUpdated, Task: &task}); err != nil {
			return err
		}
	}
	t.tasks = next
	return nil
}

func (t *TaskTracker) ListTasks() []taskstate.Task {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := append([]taskstate.Task(nil), t.tasks...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
