package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/safety"
)

// Journal events describe durable execution boundaries. Tool inputs and outputs
// are intentionally absent; the checkpoint is the only event with a transcript.
type EventKind string

const (
	EventTurnStarted       EventKind = "turn_started"
	EventModelStarted      EventKind = "model_started"
	EventModelCompleted    EventKind = "model_completed"
	EventModelFailed       EventKind = "model_failed"
	EventToolStarted       EventKind = "tool_started"
	EventToolCompleted     EventKind = "tool_completed"
	EventApprovalRequested EventKind = "approval_requested"
	EventApprovalDecided   EventKind = "approval_decided"
	EventTurnCompleted     EventKind = "turn_completed"
	EventTurnFailed        EventKind = "turn_failed"
	EventTurnAbandoned     EventKind = "turn_abandoned"
	EventContextCompacted  EventKind = "context_compacted"
	EventCheckpoint        EventKind = "checkpoint"
)

const (
	journalVersion = 1
	maxEventBytes  = 16 << 20
	maxJournalSize = 128 << 20
	compactAtSize  = 32 << 20
)

type Event struct {
	SchemaVersion int       `json:"schemaVersion"`
	Sequence      uint64    `json:"sequence"`
	At            time.Time `json:"at"`
	Kind          EventKind `json:"kind"`
	TurnID        string    `json:"turnId,omitempty"`
	CallID        string    `json:"callId,omitempty"`
	ToolName      string    `json:"toolName,omitempty"`
	Decision      string    `json:"decision,omitempty"`
	Compacted     bool      `json:"compacted,omitempty"`
	PrevHash      string    `json:"prevHash,omitempty"`
	Hash          string    `json:"hash"`
	Checkpoint    *Record   `json:"checkpoint,omitempty"`
}

type Journal struct {
	store    Store
	id       string
	lock     *os.File
	file     *os.File
	mu       sync.Mutex
	seq      uint64
	hash     string
	bytes    int64
	last     Event
	closed   bool
	poisoned bool
}

// OpenJournal takes an exclusive, nonblocking process lock for this session.
// Hold it until the CLI exits so another process cannot resume stale context.
func (s Store) OpenJournal(id string) (*Journal, error) {
	path, err := s.journalPath(id)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := openRegularStateFile(strings.TrimSuffix(path, ".events.jsonl") + ".lock")
	if err != nil {
		return nil, err
	}
	if err := lockSessionFile(lock); err != nil {
		lock.Close()
		return nil, fmt.Errorf("session %s is active in another process: %w", id, err)
	}
	file, err := openRegularStateFile(path)
	if err != nil {
		_ = unlockSessionFile(lock)
		lock.Close()
		return nil, err
	}
	events, validBytes, err := readEvents(file, id)
	if err == nil {
		var size int64
		size, err = file.Seek(0, io.SeekEnd)
		if err == nil && validBytes < size {
			// A torn final line cannot have been acknowledged before a side effect.
			err = file.Truncate(validBytes)
		}
	}
	if err != nil {
		file.Close()
		_ = unlockSessionFile(lock)
		lock.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		file.Close()
		_ = unlockSessionFile(lock)
		lock.Close()
		return nil, err
	}
	j := &Journal{store: s, id: id, lock: lock, file: file, bytes: validBytes}
	if len(events) > 0 {
		j.seq = events[len(events)-1].Sequence
		j.hash = events[len(events)-1].Hash
		j.last = events[len(events)-1]
	}
	return j, nil
}

func (j *Journal) Append(event Event) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if event.Kind == EventCheckpoint || event.Checkpoint != nil {
		return 0, fmt.Errorf("use AppendCheckpoint for transcript events")
	}
	return j.appendLocked(event)
}

// AppendCheckpoint records a sanitized, replayable transcript before the
// atomic snapshot is replaced. Recovery never invokes a model or tool.
func (j *Journal) AppendCheckpoint(record Record, turnID string) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.appendCheckpointLocked(record, turnID)
}

func (j *Journal) appendCheckpointLocked(record Record, turnID string) (Record, error) {
	record.ID = j.id
	record.SchemaVersion = 2
	record.JournalSequence = j.seq + 1
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}
	sanitized, err := sanitizeRecord(j.store.Context, record)
	if err != nil {
		return Record{}, err
	}
	_, err = j.appendLocked(Event{Kind: EventCheckpoint, TurnID: turnID, Checkpoint: &sanitized})
	return sanitized, err
}

func (j *Journal) appendLocked(event Event) (uint64, error) {
	if j.closed || j.poisoned {
		return 0, fmt.Errorf("session journal is unavailable")
	}
	if !validEventKind(event.Kind) {
		return 0, fmt.Errorf("unknown journal event kind %q", event.Kind)
	}
	if event.Compacted {
		return 0, fmt.Errorf("compacted marker is reserved for journal replacement")
	}
	event.SchemaVersion = journalVersion
	event.Sequence = j.seq + 1
	event.At = time.Now().UTC()
	event.TurnID = safety.Redact(j.store.Context, event.TurnID)
	event.CallID = safety.Redact(j.store.Context, event.CallID)
	event.ToolName = safety.Redact(j.store.Context, event.ToolName)
	event.Decision = safety.Redact(j.store.Context, event.Decision)
	event.PrevHash = j.hash
	event.Hash = eventDigest(event)
	data, err := json.Marshal(event)
	if err != nil {
		return 0, err
	}
	if len(data)+1 > maxEventBytes || j.bytes+int64(len(data)+1) > maxJournalSize {
		return 0, fmt.Errorf("session journal size limit reached")
	}
	data = append(data, '\n')
	n, err := j.file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = j.file.Sync()
	}
	if err != nil {
		j.poisoned = true
		return 0, err
	}
	j.seq = event.Sequence
	j.hash = event.Hash
	j.bytes += int64(len(data))
	j.last = event
	return j.seq, nil
}

// CompactIfNeeded retains the last durable checkpoint after its JSON snapshot
// has been saved. The checkpoint keeps its sequence and hash, so a crash before
// or after replacement can recover from either complete journal file.
func (j *Journal) CompactIfNeeded() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.bytes < compactAtSize {
		return nil
	}
	return j.compactLocked()
}

// RecoverInterrupted abandons an unfinished turn at the last checkpoint. No
// model or tool call is replayed. A tool start or approval decision requires
// the caller to explicitly acknowledge possible external side effects.
func (j *Journal) RecoverInterrupted(acknowledgeEffects bool) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed || j.poisoned {
		return Record{}, fmt.Errorf("session journal is unavailable")
	}
	record, err := j.store.Load(j.id)
	if err != nil {
		return Record{}, err
	}
	if record.ResumeError == "" {
		return record, nil
	}
	if record.ResumeError != interruptedTurnError {
		return Record{}, fmt.Errorf("saved session cannot resume: %s", record.ResumeError)
	}
	events, _, err := readEvents(j.file, j.id)
	if err != nil {
		return Record{}, err
	}
	var effects bool
	for _, event := range events {
		if event.Sequence <= record.JournalSequence {
			continue
		}
		if event.Kind == EventToolStarted || event.Kind == EventApprovalDecided {
			effects = true
		}
	}
	if effects && !acknowledgeEffects {
		return Record{}, fmt.Errorf("interrupted turn may have external effects; review the workspace, then use --recover-interrupted with --resume to abandon that turn")
	}
	record.ResumeError = ""
	if _, err := j.appendLocked(Event{Kind: EventTurnAbandoned}); err != nil {
		return Record{}, err
	}
	record, err = j.appendCheckpointLocked(record, "")
	if err != nil {
		return Record{}, err
	}
	if err := j.store.Save(record); err != nil {
		return Record{}, err
	}
	if j.bytes >= compactAtSize {
		if err := j.compactLocked(); err != nil {
			return Record{}, err
		}
	}
	return record, nil
}

func (j *Journal) compactLocked() error {
	if j.closed || j.poisoned || j.last.Kind != EventCheckpoint {
		return fmt.Errorf("journal can only compact at a durable checkpoint")
	}
	path, err := j.store.journalPath(j.id)
	if err != nil {
		return err
	}
	root := j.last
	root.Compacted = true
	root.Hash = eventDigest(root)
	data, err := json.Marshal(root)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".journal-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := j.file.Close(); err != nil {
		j.poisoned = true
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		j.poisoned = true
		return err
	}
	j.file, err = openRegularStateFile(path)
	if err != nil {
		j.poisoned = true
		return err
	}
	if _, err := j.file.Seek(0, io.SeekEnd); err != nil {
		j.poisoned = true
		return err
	}
	j.bytes = int64(len(data))
	j.last = root
	j.hash = root.Hash
	return nil
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	err := j.file.Close()
	unlockErr := unlockSessionFile(j.lock)
	closeErr := j.lock.Close()
	return errors.Join(err, unlockErr, closeErr)
}

func (s Store) journalPath(id string) (string, error) {
	path, err := s.path(id)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(path, ".json") + ".events.jsonl", nil
}

func openRegularStateFile(path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing non-regular session state file")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("refusing non-regular session state file")
	}
	// Recheck the directory entry after opening it to reject a swapped symlink.
	entry, err := os.Lstat(path)
	opened, statErr := file.Stat()
	if err != nil || statErr != nil || !entry.Mode().IsRegular() || !os.SameFile(entry, opened) {
		file.Close()
		return nil, fmt.Errorf("session state file changed during open")
	}
	return file, nil
}

func readEvents(file *os.File, id string) ([]Event, int64, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	if info.Size() > maxJournalSize+maxEventBytes {
		return nil, 0, fmt.Errorf("session journal exceeds size limit")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, 0, err
	}
	reader := bufio.NewReader(file)
	events := []Event{}
	var validBytes int64
	previousHash := ""
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			// Only the final incomplete event may be discarded.
			if len(line) > maxEventBytes {
				return nil, 0, fmt.Errorf("session journal event exceeds size limit")
			}
			return events, validBytes, nil
		}
		if err != nil {
			return nil, 0, err
		}
		if len(line) > maxEventBytes {
			return nil, 0, fmt.Errorf("session journal event exceeds size limit")
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, 0, fmt.Errorf("invalid session journal event: %w", err)
		}
		expected := uint64(1)
		if len(events) > 0 {
			expected = events[len(events)-1].Sequence + 1
		} else if event.Kind == EventCheckpoint && event.Compacted {
			// A compacted journal starts at a checkpoint with its original
			// sequence and hash-chain predecessor retained as an anchor.
			expected = event.Sequence
		}
		if event.SchemaVersion != journalVersion || !validEventKind(event.Kind) || event.Sequence != expected || event.Sequence == 0 || (event.Compacted && (len(events) > 0 || event.Kind != EventCheckpoint)) {
			return nil, 0, fmt.Errorf("invalid session journal sequence or schema")
		}
		if (!event.Compacted || len(events) > 0) && event.PrevHash != previousHash || event.Hash != eventDigest(event) {
			return nil, 0, fmt.Errorf("session journal integrity check failed")
		}
		if event.Kind == EventCheckpoint && (event.Checkpoint == nil || event.Checkpoint.ID != id || event.Checkpoint.SchemaVersion != 2 || event.Checkpoint.JournalSequence != event.Sequence) {
			return nil, 0, fmt.Errorf("invalid session journal checkpoint")
		}
		events = append(events, event)
		previousHash = event.Hash
		validBytes += int64(len(line))
		if validBytes > maxJournalSize {
			return nil, 0, fmt.Errorf("session journal exceeds size limit")
		}
	}
}

func eventDigest(event Event) string {
	event.Hash = ""
	data, _ := json.Marshal(event)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s Store) readJournal(id string) ([]Event, error) {
	path, err := s.journalPath(id)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	events, _, err := readEvents(file, id)
	return events, err
}

func validEventKind(kind EventKind) bool {
	switch kind {
	case EventTurnStarted, EventModelStarted, EventModelCompleted, EventModelFailed,
		EventToolStarted, EventToolCompleted, EventApprovalRequested, EventApprovalDecided,
		EventTurnCompleted, EventTurnFailed, EventTurnAbandoned, EventContextCompacted, EventCheckpoint:
		return true
	default:
		return false
	}
}
