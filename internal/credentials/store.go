// Package credentials keeps provider secrets in the current user's OS credential store.
// It deliberately has no plaintext-file fallback.
package credentials

import (
	"errors"
	"strings"
	"unicode"
)

var ErrNotFound = errors.New("credential not found in the operating system credential store")

type Store interface {
	Get(id string) (string, error)
	Set(id, secret string) error
	Delete(id string) error
}

type SystemStore struct{}

func valid(id, secret string) error {
	if id == "" || strings.ContainsFunc(id, unicode.IsControl) {
		return errors.New("invalid credential reference")
	}
	if len(secret) > 16*1024 {
		return errors.New("credential exceeds 16 KiB limit")
	}
	if strings.TrimSpace(secret) == "" || strings.ContainsFunc(secret, unicode.IsControl) {
		return errors.New("credential must be nonempty and contain no control line separators")
	}
	return nil
}
