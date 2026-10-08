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
	EventCheckpoint        EventKind = "checkpoint"
)

const (
	journalVersion = 1
	maxEventBytes  = 16 << 20
	maxJournalSize = 128 << 20
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
	return j.seq, nil
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
		if event.SchemaVersion != journalVersion || !validEventKind(event.Kind) || event.Sequence != uint64(len(events)+1) {
			return nil, 0, fmt.Errorf("invalid session journal sequence or schema")
		}
		if event.PrevHash != previousHash || event.Hash != eventDigest(event) {
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
		EventTurnCompleted, EventTurnFailed, EventCheckpoint:
		return true
	default:
		return false
	}
}
