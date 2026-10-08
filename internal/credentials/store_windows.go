package credentials

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var credentialDLL = windows.NewLazySystemDLL("advapi32.dll")
var credentialRead = credentialDLL.NewProc("CredReadW")
var credentialWrite = credentialDLL.NewProc("CredWriteW")
var credentialDelete = credentialDLL.NewProc("CredDeleteW")
var credentialFree = credentialDLL.NewProc("CredFree")

type nativeCredential struct {
	Flags, Type             uint32
	TargetName, Comment     *uint16
	LastWritten             windows.Filetime
	BlobSize                uint32
	Blob                    *byte
	Persist, AttributeCount uint32
	Attributes              uintptr
	TargetAlias, UserName   *uint16
}

func credentialTarget(id string) (*uint16, error) {
	return windows.UTF16PtrFromString("mycode/" + id)
}

func (SystemStore) Get(id string) (string, error) {
	target, err := credentialTarget(id)
	if err != nil {
		return "", errors.New("invalid credential reference")
	}
	var credential *nativeCredential
	ok, _, callErr := credentialRead.Call(uintptr(unsafe.Pointer(target)), 1, 0, uintptr(unsafe.Pointer(&credential)))
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("Windows Credential Manager read failed: %w", callErr)
	}
	defer credentialFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential.BlobSize == 0 || credential.Blob == nil {
		return "", ErrNotFound
	}
	return string(unsafe.Slice(credential.Blob, credential.BlobSize)), nil
}

func (SystemStore) Set(id, secret string) error {
	if err := valid(id, secret); err != nil {
		return err
	}
	if len(secret) > 2560 {
		return errors.New("credential exceeds Windows Credential Manager limit")
	}
	target, err := credentialTarget(id)
	if err != nil {
		return errors.New("invalid credential reference")
	}
	blob := []byte(secret)
	credential := nativeCredential{Type: 1, TargetName: target, BlobSize: uint32(len(blob)), Blob: &blob[0], Persist: 2}
	ok, _, callErr := credentialWrite.Call(uintptr(unsafe.Pointer(&credential)), 0)
	for i := range blob {
		blob[i] = 0
	}
	if ok == 0 {
		return fmt.Errorf("Windows Credential Manager write failed: %w", callErr)
	}
	return nil
}

func (SystemStore) Delete(id string) error {
	target, err := credentialTarget(id)
	if err != nil {
		return errors.New("invalid credential reference")
	}
	ok, _, callErr := credentialDelete.Call(uintptr(unsafe.Pointer(target)), 1, 0)
	if ok == 0 && !errors.Is(callErr, windows.ERROR_NOT_FOUND) {
		return fmt.Errorf("Windows Credential Manager delete failed: %w", callErr)
	}
	return nil
}
