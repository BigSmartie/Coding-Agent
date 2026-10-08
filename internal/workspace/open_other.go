//go:build !linux && !darwin && !windows

package workspace

import (
	"fmt"
	"os"
)

func (a *Access) openSecure(relative string) (*os.File, error) {
	return nil, fmt.Errorf("secure workspace IO is unsupported on this operating system")
}
func (a *Access) verifyHandle(file *os.File) error {
	return fmt.Errorf("secure workspace IO is unsupported on this operating system")
}

func OpenedPath(file *os.File) (string, error) {
	return "", fmt.Errorf("secure file handle inspection is unsupported on this operating system")
}

func OpenNoFollow(root *os.Root, relative string) (*os.File, error) {
	return nil, fmt.Errorf("secure file access is unsupported on this operating system")
}
