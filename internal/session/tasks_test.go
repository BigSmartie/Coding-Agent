package session

import (
	"context"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"github.com/BigSmartie/Coding-Agent/internal/taskstate"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

func TestTaskUpdateSurvivesCrashAndContextCompaction(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.Save(Record{ID: "session-1", Messages: []message.Message{message.SystemMessage("system")}}); err != nil {
		t.Fatal(err)
	}
	j, err := store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	tracker := NewTaskTracker(nil, j, context.Background())
	if err := tracker.UpsertTask(context.Background(), taskstate.Task{ID: "build", Title: "Build feature", Status: "in_progress"}); err != nil {
		t.Fatal(err)
	}
	j.Close() // crash before the turn's transcript checkpoint
	loaded, err := store.Load("session-1")
	if err != nil || loaded.ResumeError == "" || len(loaded.Tasks) != 1 || loaded.Tasks[0].Title != "Build feature" {
		t.Fatalf("task update was lost after crash: %#v, %v", loaded, err)
	}
	j, err = store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := j.RecoverInterrupted(false)
	if err != nil || len(recovered.Tasks) != 1 {
		t.Fatalf("task-only recovery failed: %#v, %v", recovered, err)
	}
	// A compacted transcript still carries the independent structured task.
	checkpoint, err := j.AppendCheckpoint(Record{ID: "session-1", Messages: []message.Message{message.SystemMessage("compacted"), message.UserMessage("summary")}, Tasks: recovered.Tasks}, "turn-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(checkpoint); err != nil {
		t.Fatal(err)
	}
	j.Close()
	loaded, err = store.Load("session-1")
	if err != nil || len(loaded.Tasks) != 1 || loaded.Tasks[0].ID != "build" || len(loaded.Messages) != 2 {
		t.Fatalf("task did not survive compaction: %#v, %v", loaded, err)
	}
}

func TestTaskToolRedactsSecretsAndFailsClosedWhenJournalUnavailable(t *testing.T) {
	const secret = "private-task-key"
	ctx := safety.WithSecrets(context.Background(), secret)
	store := Store{Dir: t.TempDir(), Context: ctx}
	j, err := store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	tracker := NewTaskTracker(nil, j, ctx)
	registry := tools.Builtins(t.TempDir(), nil, nil)
	result := registry.Execute(ctx, "task_update", map[string]any{"id": "build", "title": "Build " + secret, "status": "pending"}, tools.Context{Tasks: tracker})
	if !result.OK || strings.Contains(tracker.ListTasks()[0].Title, secret) {
		t.Fatalf("task update leaked secret: %#v, %#v", result, tracker.ListTasks())
	}
	listed := registry.Execute(ctx, "task_list", map[string]any{}, tools.Context{Tasks: tracker})
	if !listed.OK || !strings.Contains(listed.Output, "build") || strings.Contains(listed.Output, secret) {
		t.Fatalf("task list leaked or lost state: %#v", listed)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	before := tracker.ListTasks()[0]
	if err := tracker.UpsertTask(ctx, taskstate.Task{ID: "build", Title: "Changed", Status: "completed"}); err == nil {
		t.Fatal("task changed without a durable journal event")
	}
	if after := tracker.ListTasks()[0]; after.Title != before.Title || after.Status != before.Status {
		t.Fatalf("task changed after journal failure: %#v", after)
	}
}

func TestAsyncJobCompletionAfterCheckpointDoesNotBlockResume(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	baseline := Record{ID: "session-1", Messages: []message.Message{message.UserMessage("run the job")}}
	if err := store.Save(baseline); err != nil {
		t.Fatal(err)
	}
	j, err := store.OpenJournal(baseline.ID)
	if err != nil {
		t.Fatal(err)
	}
	const jobID = "0123456789abcdef"
	if _, err := j.Append(Event{Kind: EventJobStarted, JobID: jobID}); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := j.AppendCheckpoint(baseline, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Kind: EventJobCompleted, JobID: jobID}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(baseline.ID)
	if err != nil || loaded.ResumeError != "" {
		t.Fatalf("async job completion blocked resume: %#v, %v", loaded, err)
	}
}

func TestNetworkAuditIsDurableAndRequiresEffectRecovery(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.Save(Record{ID: "session-1", Messages: []message.Message{message.UserMessage("before")}}); err != nil {
		t.Fatal(err)
	}
	j, err := store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Kind: EventNetworkRequested, Origin: "https://api.example.com", Method: "POST"}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Kind: EventNetworkCompleted, Origin: "https://api.example.com", Method: "POST", Status: 200, Bytes: 42}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Kind: EventNetworkRequested, Origin: "https://api.example.com", Method: "DELETE"}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Kind: EventNetworkCompleted, Origin: "https://api.example.com", Method: "DELETE", Status: 204}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Kind: EventNetworkRequested, Origin: "http://internal.invalid", Method: "GET"}); err == nil {
		t.Fatal("invalid audit origin accepted")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("session-1")
	if err != nil || loaded.ResumeError == "" {
		t.Fatalf("network effect did not block unsafe automatic resume: %#v, %v", loaded, err)
	}
}

func TestSubagentEventsAreTypedAndRecoverable(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.Save(Record{ID: "session-1", Messages: []message.Message{message.UserMessage("investigate")}}); err != nil {
		t.Fatal(err)
	}
	j, err := store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EventKind{EventSubagentStarted, EventSubagentCompleted} {
		if _, err := j.Append(Event{Kind: kind, SubagentID: "sa-00000001"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.Append(Event{Kind: EventSubagentStarted, SubagentID: "../escape"}); err == nil {
		t.Fatal("invalid subagent id accepted")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("session-1"); err != nil {
		t.Fatalf("subagent events did not replay: %v", err)
	}
}
