package terminal

import "os"

type State struct {
	restore func() error
}

func MakeRaw(file *os.File) (*State, error) {
	return makeRaw(file)
}

func (s *State) Restore() error {
	if s == nil || s.restore == nil {
		return nil
	}
	return s.restore()
}

func Size(file *os.File) (int, int, error) {
	return size(file)
}

func IsTerminal(file *os.File) bool {
	return isTerminal(file)
}
