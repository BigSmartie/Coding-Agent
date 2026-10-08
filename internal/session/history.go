package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/BigSmartie/Coding-Agent/internal/safety"
)

type History struct {
	Path    string
	Context context.Context
}

type historyFile struct {
	Entries []string `json:"entries"`
}

func (h History) Load() ([]string, error) {
	file, err := os.Open(h.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	bytes, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(bytes) > 1<<20 {
		return nil, fmt.Errorf("input history exceeds size limit")
	}
	var parsed historyFile
	if err := json.Unmarshal(bytes, &parsed); err != nil {
		return nil, err
	}
	return parsed.Entries, nil
}

func (h History) Save(entries []string) error {
	if h.Path == "" {
		return nil
	}
	if len(entries) > 200 {
		entries = entries[len(entries)-200:]
	}
	bytes, err := json.MarshalIndent(historyFile{Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	bytes, err = safety.RedactJSON(h.Context, bytes)
	if err != nil {
		return err
	}
	if len(bytes) > 1<<20 {
		return fmt.Errorf("input history exceeds size limit")
	}
	return safety.PrivateWrite(h.Path, append(bytes, '\n'))
}
