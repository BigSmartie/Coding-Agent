//go:build windows

package terminal

import (
	"os"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

func makeRaw(file *os.File) (*State, error) {
	if file == nil {
		return nil, os.ErrInvalid
	}
	handle := windows.Handle(file.Fd())
	var oldState uint32
	err := windows.GetConsoleMode(handle, &oldState)
	if err != nil {
		return nil, err
	}
	raw := oldState &^ (windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_LINE_INPUT)
	if err := windows.SetConsoleMode(handle, raw); err != nil {
		return nil, err
	}
	return &State{
		restore: func() error {
			return windows.SetConsoleMode(handle, oldState)
		},
	}, nil
}

func size(file *os.File) (int, int, error) {
	if file == nil {
		return 0, 0, os.ErrInvalid
	}
	return term.GetSize(int(file.Fd()))
}

func isTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}
