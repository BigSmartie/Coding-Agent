//go:build linux

package workspace

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// OpenedPath inspects the already-open inode and descriptor, rejecting hard
// links, special files and deleted files before callers read any data.
func OpenedPath(file *os.File) (string, error) {
	if err := validateFileLinks(file); err != nil {
		return "", err
	}
	final, err := os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(file.Fd()), 10))
	if err != nil {
		return "", fmt.Errorf("cannot verify opened file handle: %w", err)
	}
	if strings.HasSuffix(final, " (deleted)") {
		return "", fmt.Errorf("opened file was removed during access")
	}
	return final, nil
}
