package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const MaxFileBytes = 2 * 1024 * 1024

type Permission interface {
	EnsurePathAccess(ctx context.Context, targetPath, intent string) error
}

// Canonical resolves existing ancestors, including Windows junctions, while
// allowing a not-yet-created file beneath an existing directory.
func Canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(abs)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", err
		}
		tail = append(tail, filepath.Base(abs))
		abs = parent
	}
}

// Protected paths are never model-visible, even when an ordinary path approval
// exists. The same policy is used when preparing a process snapshot.
func Protected(path string) bool {
	for _, part := range strings.FieldsFunc(strings.ToLower(filepath.ToSlash(path)), func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.Contains(part, ":") {
			return true
		}
		switch part {
		case ".git", ".my-code", ".mini-code", ".codex", ".agents", ".claude", ".ssh", ".aws", ".azure", ".kube", ".gnupg", ".mcp.json", ".npmrc", ".pypirc", ".netrc", "credentials", "credentials.json", "secrets.json", "id_rsa", "id_ed25519":
			return true
		}
		if part == ".env" || strings.HasPrefix(part, ".env.") || strings.HasSuffix(part, ".pem") || strings.HasSuffix(part, ".key") || strings.HasSuffix(part, ".p12") || strings.HasSuffix(part, ".pfx") {
			return true
		}
	}
	return false
}

func Resolve(ctx context.Context, cwd, targetPath, intent string, permission Permission) (string, error) {
	root, err := Canonical(cwd)
	if err != nil {
		return "", err
	}
	lexical := targetPath
	if !filepath.IsAbs(lexical) {
		lexical = filepath.Join(root, lexical)
	}
	lexical, err = filepath.Abs(lexical)
	if err != nil {
		return "", err
	}
	if !Within(root, lexical) {
		return "", fmt.Errorf("path escapes workspace: %s", targetPath)
	}
	rel, _ := filepath.Rel(root, lexical)
	if Protected(rel) {
		return "", fmt.Errorf("protected workspace path: %s", targetPath)
	}
	resolved, err := Canonical(lexical)
	if err != nil {
		return "", err
	}
	if !Within(root, resolved) {
		return "", fmt.Errorf("path escapes workspace through a link: %s", targetPath)
	}
	canonicalRel, _ := filepath.Rel(root, resolved)
	if Protected(canonicalRel) {
		return "", fmt.Errorf("protected workspace path through a link: %s", targetPath)
	}
	if permission != nil {
		if err := permission.EnsurePathAccess(ctx, resolved, intent); err != nil {
			return "", err
		}
	}
	return resolved, nil
}

func Within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)))
}

// Access keeps an os.Root handle so all operations remain inside the workspace
// even if a directory or symlink is swapped after path approval.
type Access struct{ root *os.Root }

func Open(cwd string) (*Access, error) {
	canonical, err := Canonical(cwd)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	return &Access{root: root}, nil
}
func (a *Access) Close() error { return a.root.Close() }
func (a *Access) relative(path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.root.Name(), path)
	}
	if !Within(a.root.Name(), path) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}
	rel, err := filepath.Rel(a.root.Name(), path)
	if err != nil {
		return "", err
	}
	if Protected(rel) {
		return "", fmt.Errorf("protected workspace path: %s", rel)
	}
	current := ""
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := a.root.Lstat(current)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symbolic links are not accessible: %s", rel)
		}
	}
	return rel, nil
}
func (a *Access) ReadFile(path string, limit int64) ([]byte, error) {
	rel, err := a.relative(path)
	if err != nil {
		return nil, err
	}
	f, err := a.openSecure(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", rel)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d byte limit: %s", limit, rel)
	}
	return data, err
}
func (a *Access) ReadChunk(path string, offset int64, limit int) ([]byte, int64, error) {
	rel, err := a.relative(path)
	if err != nil {
		return nil, 0, err
	}
	f, err := a.openSecure(rel)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("not a regular file: %s", rel)
	}
	if offset > info.Size() {
		offset = info.Size()
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)))
	return data, info.Size(), err
}
func (a *Access) ReadDir(path string, limit int) ([]os.DirEntry, error) {
	rel, err := a.relative(path)
	if err != nil {
		return nil, err
	}
	f, err := a.openSecure(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(limit)
	if err == io.EOF {
		err = nil
	}
	return entries, err
}

// WriteFile writes a sibling temporary file and renames it through os.Root.
// A final symlink is replaced rather than followed, with no absolute reopen.
func (a *Access) WriteFile(path string, data []byte) error {
	if len(data) > MaxFileBytes {
		return fmt.Errorf("file exceeds %d byte limit", MaxFileBytes)
	}
	rel, err := a.relative(path)
	if err != nil {
		return err
	}
	parent := filepath.Dir(rel)
	if err := a.root.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	parentHandle, err := a.openSecure(parent)
	if err != nil {
		return err
	}
	defer parentHandle.Close()
	parentRoot, err := a.root.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer parentRoot.Close()
	actual, err := parentRoot.Open(".")
	if err != nil {
		return err
	}
	defer actual.Close()
	expectedInfo, err := parentHandle.Stat()
	if err != nil {
		return err
	}
	actualInfo, err := actual.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(expectedInfo, actualInfo) {
		return fmt.Errorf("parent changed while opening workspace path")
	}
	if err := a.verifyHandle(actual); err != nil {
		return err
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	base := filepath.Base(rel)
	temp := base + ".mycode-write-" + hex.EncodeToString(random[:])
	f, err := parentRoot.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer parentRoot.Remove(temp)
	if err := a.verifyHandle(f); err != nil {
		f.Close()
		return err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if _, err := a.relative(path); err != nil {
		return err
	}
	if err := a.verifyHandle(actual); err != nil {
		return err
	}
	return parentRoot.Rename(temp, base)
}

func (a *Access) verifyOpenedPath(final string) error {
	if !Within(a.root.Name(), final) {
		return fmt.Errorf("opened handle escapes workspace")
	}
	relative, err := filepath.Rel(a.root.Name(), final)
	if err != nil {
		return err
	}
	if Protected(relative) {
		return fmt.Errorf("opened handle targets protected workspace state")
	}
	return nil
}
