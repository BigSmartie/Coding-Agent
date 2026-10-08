//go:build darwin

package workspace

import (
	"bytes"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func OpenedPath(file *os.File) (string, error) {
	if err := validateFileLinks(file); err != nil {
		return "", err
	}
	var buffer [1024]byte
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), uintptr(unix.F_GETPATH), uintptr(unsafe.Pointer(&buffer[0])))
	runtime.KeepAlive(file)
	if errno != 0 {
		return "", errno
	}
	if index := bytes.IndexByte(buffer[:], 0); index >= 0 {
		return string(buffer[:index]), nil
	}
	return "", syscall.ENAMETOOLONG
}
