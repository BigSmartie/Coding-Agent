package subagent

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/BigSmartie/Coding-Agent/internal/safety"
)

// TraceStore keeps only metadata: no prompts, model output, tool arguments,
// provider state, credentials, or file contents enter this record.
type TraceStore struct{ mu sync.Mutex }

func (s *TraceStore) Append(path string, event Trace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var events []Trace
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return fmt.Errorf("subagent trace is not a bounded regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &events); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if len(events) >= maxCallsPerSession*2 {
		return fmt.Errorf("subagent trace event limit reached")
	}
	events = append(events, event)
	data, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return err
	}
	return safety.PrivateWrite(path, append(data, '\n'))
}
