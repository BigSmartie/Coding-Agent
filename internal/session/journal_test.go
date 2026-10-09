package session

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/model"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
)

func TestJournalReplaysCompletedCheckpointAfterSnapshotCrash(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	baseline := Record{ID: "session-1", CWD: t.TempDir(), Messages: []message.Message{message.UserMessage("old")}}
	if err := store.Save(baseline); err != nil {
		t.Fatal(err)
	}
	j, err := store.OpenJournal(baseline.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EventKind{EventTurnStarted, EventModelStarted, EventModelCompleted, EventToolStarted, EventToolCompleted, EventTurnCompleted} {
		if _, err := j.Append(Event{Kind: kind, TurnID: "turn-1"}); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint, err := j.AppendCheckpoint(Record{ID: baseline.ID, CWD: baseline.CWD, Messages: []message.Message{message.UserMessage("new"), message.AssistantMessage("done")}}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.JournalSequence != 7 {
		t.Fatalf("unexpected checkpoint sequence: %d", checkpoint.JournalSequence)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a process dying after fsync of the journal, before Save.
	loaded, err := store.Load(baseline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ResumeError != "" || len(loaded.Messages) != 2 || loaded.Messages[1].Content != "done" || loaded.JournalSequence != 7 {
		t.Fatalf("checkpoint did not replay: %#v", loaded)
	}
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	again, err := store.Load(baseline.ID)
	if err != nil || len(again.Messages) != 2 || again.JournalSequence != 7 {
		t.Fatalf("replayed checkpoint was not idempotent: %#v, %v", again, err)
	}
}

func TestJournalCompactionPreservesSequenceAndCrashReplay(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	baseline := Record{ID: "session-1", Messages: []message.Message{message.UserMessage("old")}}
	if err := store.Save(baseline); err != nil {
		t.Fatal(err)
	}
	j, err := store.OpenJournal(baseline.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EventKind{EventTurnStarted, EventModelStarted, EventModelCompleted, EventTurnCompleted} {
		if _, err := j.Append(Event{Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint, err := j.AppendCheckpoint(Record{ID: baseline.ID, Messages: []message.Message{message.UserMessage("new")}}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	// Force the size trigger without writing megabytes in the test. Simulate a
	// crash after journal replacement but before the JSON snapshot save.
	j.bytes = compactAtSize
	if err := j.CompactIfNeeded(); err != nil {
		t.Fatal(err)
	}
	if j.bytes >= compactAtSize {
		t.Fatal("journal was not compacted")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(baseline.ID)
	if err != nil || loaded.JournalSequence != checkpoint.JournalSequence || loaded.Messages[0].Content != "new" {
		t.Fatalf("compacted checkpoint did not replay: %#v, %v", loaded, err)
	}
	j, err = store.OpenJournal(baseline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if seq, err := j.Append(Event{Kind: EventTurnStarted}); err != nil || seq != checkpoint.JournalSequence+1 {
		t.Fatalf("sequence after compaction = %d, %v", seq, err)
	}
	j.Close()
	events, err := store.readJournal(baseline.ID)
	if err != nil || len(events) != 2 || events[0].Kind != EventCheckpoint {
		t.Fatalf("unexpected compacted journal: %#v, %v", events, err)
	}
}

func TestJournalRejectsUnmarkedMissingPrefix(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	j, err := store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Kind: EventTurnStarted}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.AppendCheckpoint(Record{ID: "session-1"}, "turn-1"); err != nil {
		t.Fatal(err)
	}
	j.Close()
	path, _ := store.journalPath("session-1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if err := os.WriteFile(path, []byte(lines[1]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenJournal("session-1"); err == nil {
		t.Fatal("unmarked missing prefix was accepted as compaction")
	}
}

func TestJournalIncompleteToolTurnBlocksResumeWithoutReplay(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.Save(Record{ID: "session-1", Messages: []message.Message{message.UserMessage("before")}}); err != nil {
		t.Fatal(err)
	}
	j, err := store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EventKind{EventTurnStarted, EventModelCompleted, EventToolStarted} {
		if _, err := j.Append(Event{Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ResumeError == "" || len(loaded.Messages) != 1 || loaded.Messages[0].Content != "before" {
		t.Fatalf("unsafe interrupted turn was resumed: %#v", loaded)
	}
}

func TestJournalRecoversOnlySideEffectFreeInterruptedTurnAutomatically(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.Save(Record{ID: "session-1", Messages: []message.Message{message.UserMessage("before")}}); err != nil {
		t.Fatal(err)
	}
	j, err := store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EventKind{EventTurnStarted, EventModelStarted} {
		if _, err := j.Append(Event{Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	j.Close()
	j, err = store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := j.RecoverInterrupted(false)
	if err != nil || recovered.ResumeError != "" || len(recovered.Messages) != 1 || recovered.Messages[0].Content != "before" {
		t.Fatalf("side-effect-free recovery failed: %#v, %v", recovered, err)
	}
	j.Close()
	loaded, err := store.Load("session-1")
	if err != nil || loaded.ResumeError != "" {
		t.Fatalf("recovery did not persist: %#v, %v", loaded, err)
	}
}

func TestJournalRecoveryRequiresAcknowledgementAfterToolStart(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.Save(Record{ID: "session-1", Messages: []message.Message{message.UserMessage("before")}}); err != nil {
		t.Fatal(err)
	}
	j, err := store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []EventKind{EventTurnStarted, EventToolStarted} {
		if _, err := j.Append(Event{Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	j.Close()
	j, err = store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.RecoverInterrupted(false); err == nil || !strings.Contains(err.Error(), "--recover-interrupted") {
		t.Fatalf("tool effects were silently abandoned: %v", err)
	}
	recovered, err := j.RecoverInterrupted(true)
	if err != nil || recovered.ResumeError != "" || len(recovered.Messages) != 1 {
		t.Fatalf("acknowledged recovery failed: %#v, %v", recovered, err)
	}
	j.Close()
	loaded, err := store.Load("session-1")
	if err != nil || loaded.ResumeError != "" {
		t.Fatalf("acknowledged recovery did not persist: %#v, %v", loaded, err)
	}
}

func TestJournalExclusiveLockAndTornTailRecovery(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	j, err := store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenJournal("session-1"); err == nil {
		t.Fatal("second process-style session lock succeeded")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestJournalLockHelperProcess$")
	cmd.Env = append(os.Environ(), "MYTHOSCODE_TEST_JOURNAL_LOCK=1", "MYTHOSCODE_TEST_JOURNAL_DIR="+store.Dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock was not enforced: %v: %s", err, output)
	}
	if _, err := j.Append(Event{Kind: EventTurnStarted}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	path, _ := store.journalPath("session-1")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"schemaVersion":1,"sequence":2`); err != nil {
		t.Fatal(err)
	}
	file.Close()
	j, err = store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if seq, err := j.Append(Event{Kind: EventModelStarted}); err != nil || seq != 2 {
		t.Fatalf("torn tail was not removed: sequence %d, error %v", seq, err)
	}
	j.Close()
	events, err := store.readJournal("session-1")
	if err != nil || len(events) != 2 {
		t.Fatalf("unexpected repaired journal: %#v, %v", events, err)
	}
}

func TestJournalLockHelperProcess(t *testing.T) {
	if os.Getenv("MYTHOSCODE_TEST_JOURNAL_LOCK") != "1" {
		return
	}
	store := Store{Dir: os.Getenv("MYTHOSCODE_TEST_JOURNAL_DIR")}
	if journal, err := store.OpenJournal("session-1"); err == nil {
		journal.Close()
		t.Fatal("second process acquired active session")
	}
}

func TestJournalDetectsModifiedCompleteEvent(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	j, err := store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Kind: EventModelStarted}); err != nil {
		t.Fatal(err)
	}
	j.Close()
	path, _ := store.journalPath("session-1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(data), string(EventModelStarted), string(EventModelFailed), 1)
	if err := os.WriteFile(path, []byte(modified), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenJournal("session-1"); err == nil {
		t.Fatal("modified event passed integrity check")
	}
}

func TestLegacySnapshotMigratesOnNextSave(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	legacy := Record{ID: "session-1", SchemaVersion: 1, Messages: []message.Message{message.UserMessage("legacy")}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.path(legacy.ID)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(legacy.ID)
	if err != nil || loaded.SchemaVersion != 1 {
		t.Fatalf("legacy load failed: %#v, %v", loaded, err)
	}
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	migrated, err := store.Load(legacy.ID)
	if err != nil || migrated.SchemaVersion != 2 || migrated.Messages[0].Content != "legacy" {
		t.Fatalf("legacy migration failed: %#v, %v", migrated, err)
	}
}

func TestJournalRejectsCorruptCompleteEventAndSymlink(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	path, _ := store.journalPath("session-1")
	if err := os.WriteFile(path, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenJournal("session-1"); err == nil {
		t.Fatal("accepted a corrupt complete event")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.OpenJournal("session-1"); err == nil {
		t.Fatal("accepted a symlink journal")
	}
}

func TestJournalRedactsCheckpointAndRunOnceWritesEvents(t *testing.T) {
	const secret = "sample-journal-runtime-secret"
	store := Store{Dir: t.TempDir(), Context: safety.WithSecrets(context.Background(), secret)}
	j, err := store.OpenJournal("session-1")
	if err != nil {
		t.Fatal(err)
	}
	s := New(Args{
		CWD:       t.TempDir(),
		Tools:     tools.NewRegistry(nil, tools.Metadata{}),
		Model:     model.Mock{},
		Messages:  []message.Message{message.SystemMessage("system")},
		Store:     store,
		SessionID: "session-1",
		Journal:   j,
		Out:       &strings.Builder{},
	})
	if err := s.RunOnce(context.Background(), "hello "+secret); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	path, _ := store.journalPath("session-1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || !strings.Contains(string(data), "turn_started") || !strings.Contains(string(data), "checkpoint") {
		t.Fatalf("journal event/redaction mismatch: %q", data)
	}
	loaded, err := store.Load("session-1")
	if err != nil || loaded.ResumeError != "" || loaded.JournalSequence == 0 {
		t.Fatalf("run once did not checkpoint: %#v, %v", loaded, err)
	}
}
