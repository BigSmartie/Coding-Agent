//go:build linux || darwin

package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openSecure walks each component with O_NOFOLLOW, using already-open directory
// descriptors. Symlink swaps after the initial policy check cannot redirect IO.
func (a *Access) openSecure(relative string) (*os.File, error) {
	file, err := OpenNoFollow(a.root, relative)
	if err != nil {
		return nil, err
	}
	if err := a.verifyHandle(file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// OpenNoFollow opens a root-relative path without following any symlink or
// blocking on a substituted FIFO. Callers still apply policy to OpenedPath.
func OpenNoFollow(root *os.Root, relative string) (*os.File, error) {
	if !filepath.IsLocal(relative) || relative != filepath.Clean(relative) {
		return nil, fmt.Errorf("invalid root-relative path")
	}
	current, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	if relative != "." {
		parts := strings.Split(relative, string(filepath.Separator))
		for index, part := range parts {
			flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
			if index < len(parts)-1 {
				flags |= unix.O_DIRECTORY
			}
			fd, err := unix.Openat(int(current.Fd()), part, flags, 0)
			current.Close()
			if err != nil {
				return nil, &os.PathError{Op: "open nofollow", Path: relative, Err: err}
			}
			current = os.NewFile(uintptr(fd), part)
		}
	}
	if err := validateFileLinks(current); err != nil {
		current.Close()
		return nil, err
	}
	return current, nil
}

func (a *Access) verifyHandle(file *os.File) error {
	final, err := OpenedPath(file)
	if err != nil {
		return err
	}
	return a.verifyOpenedPath(final)
}

func validateFileLinks(file *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	mode := stat.Mode & unix.S_IFMT
	if mode != unix.S_IFDIR && mode != unix.S_IFREG {
		return fmt.Errorf("special files are not accessible")
	}
	if mode == unix.S_IFREG && stat.Nlink != 1 {
		return fmt.Errorf("hard-linked files are not accessible")
	}
	return nil
}
