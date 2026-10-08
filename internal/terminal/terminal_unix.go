//go:build !windows

package terminal

import (
	"os"

	"golang.org/x/term"
)

func makeRaw(file *os.File) (*State, error) {
	if file == nil {
		return nil, os.ErrInvalid
	}
	oldState, err := term.MakeRaw(int(file.Fd()))
	if err != nil {
		return nil, err
	}
	return &State{
		restore: func() error {
			return term.Restore(int(file.Fd()), oldState)
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
