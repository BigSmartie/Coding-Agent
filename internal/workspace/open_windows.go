//go:build windows

package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func (a *Access) openSecure(relative string) (*os.File, error) {
	file, err := a.root.Open(relative)
	if err != nil {
		return nil, err
	}
	if err := a.verifyHandle(file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// Verification uses the opened handle, not a pathname which an attacker can
// replace. This also rejects NTFS hard links and junctions into protected state.
func (a *Access) verifyHandle(file *os.File) error {
	final, err := OpenedPath(file)
	if err != nil {
		return err
	}
	return a.verifyOpenedPath(final)
}

// OpenedPath returns the actual path named by an open file handle and rejects
// hard-linked regular files. It does not apply a workspace policy by itself.
func OpenedPath(file *os.File) (string, error) {
	handle := windows.Handle(file.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return "", err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 && info.NumberOfLinks != 1 {
		return "", fmt.Errorf("hard-linked files are not accessible")
	}
	buffer := make([]uint16, 32768)
	size, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if size >= uint32(len(buffer)) {
		return "", fmt.Errorf("opened file path exceeds supported length")
	}
	final := windows.UTF16ToString(buffer[:size])
	if strings.HasPrefix(final, `\\?\UNC\`) {
		final = `\\` + strings.TrimPrefix(final, `\\?\UNC\`)
	} else {
		final = strings.TrimPrefix(final, `\\?\`)
	}
	return final, nil
}

// OpenNoFollow is paired with OpenedPath validation on Windows. os.Root prevents
// external traversal; handle identity/path verification rejects protected aliases.
func OpenNoFollow(root *os.Root, relative string) (*os.File, error) {
	if !filepath.IsLocal(relative) || relative != filepath.Clean(relative) {
		return nil, fmt.Errorf("invalid root-relative path")
	}
	file, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	if _, err := OpenedPath(file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
