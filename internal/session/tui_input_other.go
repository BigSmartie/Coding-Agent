//go:build !windows

package session

import (
	"io"

	"github.com/BigSmartie/Coding-Agent/internal/tui"
)

func readTUIEvents(input io.Reader, rest string, buffer []byte) ([]tui.InputEvent, string, error) {
	n, err := input.Read(buffer)
	if err != nil {
		return nil, rest, err
	}
	parsed := tui.ParseInputChunk(rest, string(buffer[:n]))
	return parsed.Events, parsed.Rest, nil
}
