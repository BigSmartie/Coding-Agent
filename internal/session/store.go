package session

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BigSmartie/Coding-Agent/internal/message"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
)

type Store struct {
	Dir     string
	Context context.Context
}

type Record struct {
	SchemaVersion int               `json:"schemaVersion,omitempty"`
	ResumeError   string            `json:"resumeError,omitempty"`
	ID            string            `json:"id"`
	CWD           string            `json:"cwd,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
	Messages      []message.Message `json:"messages"`
}

type Summary struct {
	ID           string
	CWD          string
	UpdatedAt    time.Time
	MessageCount int
}

func NewRecord(cwd string, messages []message.Message) Record {
	now := time.Now().UTC()
	return Record{
		ID:            now.Format("20060102-150405") + "-" + strings.ToLower(rand.Text()),
		SchemaVersion: 1,
		CWD:           cwd,
		CreatedAt:     now,
		UpdatedAt:     now,
		Messages:      append([]message.Message(nil), messages...),
	}
}

func (s Store) Save(record Record) error {
	if strings.TrimSpace(record.ID) == "" {
		record.ID = NewRecord(record.CWD, nil).ID
	}
	path, err := s.path(record.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		if previous, err := s.Load(record.ID); err == nil {
			record.CreatedAt = previous.CreatedAt
		} else if !os.IsNotExist(err) {
			return err
		} else {
			record.CreatedAt = now
		}
	}
	record.SchemaVersion = 1
	record.UpdatedAt = now
	bytes, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	bytes, err = safety.RedactJSON(s.Context, bytes)
	if err != nil {
		return err
	}
	// Signed/encrypted continuation fields are protocol bytes, not display text.
	// Keep those fields intact. If signed plaintext required redaction, retain
	// the redacted history but explicitly disable replay of the invalid signature.
	var sanitized Record
	if err := json.Unmarshal(bytes, &sanitized); err != nil {
		return err
	}
	if err := preserveOpaqueState(s.Context, record, &sanitized); err != nil {
		return err
	}
	bytes, err = json.MarshalIndent(sanitized, "", "  ")
	if err != nil {
		return err
	}
	if len(bytes) > 16<<20 {
		return fmt.Errorf("session exceeds 16 MiB persistence limit")
	}
	return safety.PrivateWrite(path, append(bytes, '\n'))
}

func preserveOpaqueState(ctx context.Context, original Record, sanitized *Record) error {
	for i, msg := range original.Messages {
		if msg.ProviderState == nil || sanitized.Messages[i].ProviderState == nil {
			continue
		}
		for j, raw := range msg.ProviderState.Items {
			var before, after map[string]json.RawMessage
			if err := json.Unmarshal(raw, &before); err != nil {
				return err
			}
			if err := json.Unmarshal(sanitized.Messages[i].ProviderState.Items[j], &after); err != nil {
				return err
			}
			var kind string
			_ = json.Unmarshal(before["type"], &kind)
			field := ""
			switch {
			case msg.ProviderState.Protocol == "openai_responses" && kind == "reasoning":
				field = "encrypted_content"
			case msg.ProviderState.Protocol == "anthropic_messages" && kind == "redacted_thinking":
				field = "data"
			case msg.ProviderState.Protocol == "anthropic_messages" && kind == "thinking":
				field = "signature"
				var oldText, newText string
				_ = json.Unmarshal(before["thinking"], &oldText)
				_ = json.Unmarshal(after["thinking"], &newText)
				if oldText != newText && len(before["signature"]) > 0 {
					sanitized.ResumeError = "This session contains signed reasoning that required secret redaction. Its history was saved, but it cannot be resumed safely; start a new session."
				}
			}
			if field != "" && len(before[field]) > 0 {
				var opaque string
				if err := json.Unmarshal(before[field], &opaque); err != nil {
					return fmt.Errorf("invalid opaque provider state")
				}
				if safety.RedactKnownSecrets(ctx, opaque) != opaque {
					sanitized.ResumeError = "This session contains provider continuation data that required credential redaction. Its history was saved, but it cannot be resumed safely; start a new session."
					continue
				}
				after[field] = before[field]
				var err error
				sanitized.Messages[i].ProviderState.Items[j], err = json.Marshal(after)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s Store) Load(id string) (Record, error) {
	path, err := s.path(id)
	if err != nil {
		return Record{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Record{}, err
	}
	defer file.Close()
	bytes, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil {
		return Record{}, err
	}
	if len(bytes) > 16<<20 {
		return Record{}, fmt.Errorf("session exceeds 16 MiB read limit")
	}
	var record Record
	if err := json.Unmarshal(bytes, &record); err != nil {
		return Record{}, err
	}
	if record.ID != id {
		return Record{}, fmt.Errorf("session ID does not match its filename")
	}
	if record.SchemaVersion > 1 {
		return Record{}, fmt.Errorf("session was written by a newer schema version")
	}
	return record, nil
}

func (s Store) Latest() (Record, error) {
	summaries, err := s.List()
	if err != nil {
		return Record{}, err
	}
	if len(summaries) == 0 {
		return Record{}, fmt.Errorf("no saved sessions")
	}
	return s.Load(summaries[0].ID)
}

func (s Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Summary{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := s.Load(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, Summary{ID: record.ID, CWD: record.CWD, UpdatedAt: record.UpdatedAt, MessageCount: len(record.Messages)})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (s Store) path(id string) (string, error) {
	if strings.TrimSpace(s.Dir) == "" {
		return "", fmt.Errorf("session store directory is empty")
	}
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("session id is empty")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`).MatchString(id) {
		return "", fmt.Errorf("invalid session id: %s", id)
	}
	return filepath.Join(s.Dir, id+".json"), nil
}
