package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/workspace"
)

// WorkspaceSnapshot contains the exact immutable bytes whose fingerprint was
// approved. Its contents cannot be constructed or changed outside this package.
type WorkspaceSnapshot struct {
	root        string
	fingerprint string
	files       map[string]string
}

func (s *WorkspaceSnapshot) ForWorkspace(cwd string) bool {
	if s == nil {
		return false
	}
	root, err := canonical(cwd)
	return err == nil && samePath(s.root, root)
}

func (s *WorkspaceSnapshot) Content(path string) string {
	if s == nil {
		return ""
	}
	return s.files[filepath.ToSlash(path)]
}

func (s *WorkspaceSnapshot) Paths() []string {
	if s == nil {
		return nil
	}
	paths := make([]string, 0, len(s.files))
	for path := range s.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// Expand replaces reviewed @include lines using only bytes in this approved
// snapshot. It never reads the current workspace after approval.
func (s *WorkspaceSnapshot) Expand(path string) (string, error) {
	if s == nil {
		return "", nil
	}
	active := map[string]bool{}
	var expand func(string, int) (string, error)
	expand = func(name string, depth int) (string, error) {
		if depth > 32 || active[name] {
			return "", fmt.Errorf("project memory include cycle or depth limit")
		}
		content, ok := s.files[filepath.ToSlash(name)]
		if !ok {
			return "", fmt.Errorf("included project memory is not in the approved snapshot")
		}
		active[name] = true
		defer delete(active, name)
		var out strings.Builder
		for _, line := range strings.SplitAfter(content, "\n") {
			target, included, err := IncludeTarget(strings.TrimSuffix(line, "\n"))
			if err != nil {
				return "", err
			}
			if !included {
				out.WriteString(line)
				continue
			}
			child, err := resolveInclude(name, target)
			if err != nil {
				return "", err
			}
			part, err := expand(child, depth+1)
			if err != nil {
				return "", err
			}
			out.WriteString("\nIncluded reviewed memory from " + child + ":\n" + part + "\n")
			if out.Len() > 4<<20 {
				return "", fmt.Errorf("expanded project memory exceeds size limit")
			}
		}
		return out.String(), nil
	}
	return expand(filepath.ToSlash(path), 0)
}

// IncludeTarget recognizes an explicit, single relative Markdown path.
func IncludeTarget(line string) (string, bool, error) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "@include ") {
		return "", false, nil
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, "@include "))
	if target == "" || strings.ContainsAny(target, "\\:\t\r\n") || strings.Contains(target, " ") || filepath.IsAbs(target) || !strings.HasSuffix(strings.ToLower(target), ".md") {
		return "", true, fmt.Errorf("memory include must be one relative .md path")
	}
	for _, part := range strings.Split(target, "/") {
		if part == "" || part == "." || part == ".." {
			return "", true, fmt.Errorf("memory include cannot traverse directories")
		}
	}
	return target, true, nil
}

func resolveInclude(parent, target string) (string, error) {
	name := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(parent), filepath.FromSlash(target))))
	if name == ".." || strings.HasPrefix(name, "../") || filepath.IsAbs(name) {
		return "", fmt.Errorf("memory include escapes its root")
	}
	return name, nil
}

// Workspace returns no project content unless this exact snapshot is approved.
func (s Store) Workspace(cwd string) (*WorkspaceSnapshot, error) {
	snapshot, err := captureWorkspace(cwd)
	if err != nil {
		return nil, err
	}
	if !s.Allowed(cwd, "workspace", snapshot.fingerprint) {
		return nil, nil
	}
	return snapshot, nil
}

func captureWorkspace(cwd string) (*WorkspaceSnapshot, error) {
	root, err := canonical(cwd)
	if err != nil {
		return nil, err
	}
	access, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer access.Close()
	snapshot := &WorkspaceSnapshot{root: root, files: map[string]string{}}
	total, entries := 0, 0
	state := map[string]uint8{}
	var add func(string, bool, int) error
	add = func(path string, followIncludes bool, depth int) error {
		path = filepath.ToSlash(path)
		if depth > 32 || state[path] == 1 {
			return fmt.Errorf("project memory include cycle or depth limit")
		}
		if state[path] == 2 {
			return nil
		}
		state[path] = 1
		data, err := readRootFile(access, path, 256<<10)
		if err != nil {
			return err
		}
		total += len(data)
		if total > 4<<20 || len(snapshot.files) >= 1024 {
			return fmt.Errorf("project rules exceed size limit")
		}
		snapshot.files[path] = string(data)
		if followIncludes {
			for _, line := range strings.Split(string(data), "\n") {
				target, included, err := IncludeTarget(line)
				if err != nil {
					return err
				}
				if included {
					child, err := resolveInclude(path, target)
					if err != nil {
						return err
					}
					if err := add(child, true, depth+1); err != nil {
						return err
					}
				}
			}
		}
		state[path] = 2
		return nil
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md", "MEMORY.md", ".mythos-code/MEMORY.md"} {
		if _, err := access.Lstat(name); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err := add(name, true, 0); err != nil {
			return nil, err
		}
	}
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if depth > 32 {
			return fmt.Errorf("project skills exceed directory depth limit")
		}
		file, err := openRootFile(access, path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		children, err := file.ReadDir(4097)
		file.Close()
		if err != nil && err != io.EOF {
			return err
		}
		entries += len(children)
		if entries > 4096 {
			return fmt.Errorf("project skills exceed entry limit")
		}
		for _, child := range children {
			if child.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("project rules cannot include links")
			}
			next := filepath.Join(path, child.Name())
			if child.IsDir() {
				if err := walk(next, depth+1); err != nil {
					return err
				}
			} else if child.Name() == "SKILL.md" {
				if err := add(next, false, 0); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, path := range []string{".mythos-code/skills", ".claude/skills"} {
		if err := walk(filepath.FromSlash(path), 0); err != nil {
			return nil, err
		}
	}
	// Project memory is opt-in through the reviewed root MEMORY.md. Its
	// included nested files are captured by add and covered by the fingerprint.
	encoded, err := json.Marshal(struct {
		Version int
		Root    string
		Files   map[string]string
	}{2, root, snapshot.files})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(encoded)
	snapshot.fingerprint = hex.EncodeToString(sum[:])
	return snapshot, nil
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// openRootFile rejects links both before and after opening and checks the actual
// handle path. os.Root prevents an intermediate substitution from escaping root.
func openRootFile(root *os.Root, path string) (*os.File, error) {
	path = filepath.Clean(path)
	if filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("path escapes rule root")
	}
	current := ""
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return nil, fmt.Errorf("rules and trust records cannot contain links or special files")
		}
	}
	file, err := workspace.OpenNoFollow(root, path)
	if err != nil {
		return nil, err
	}
	actual, err := workspace.OpenedPath(file)
	if err != nil || !samePath(actual, filepath.Join(root.Name(), path)) {
		file.Close()
		return nil, fmt.Errorf("rule or trust file changed location while opening")
	}
	return file, nil
}

func readRootFile(root *os.Root, path string, limit int64) ([]byte, error) {
	file, err := openRootFile(root, path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("rules and trust records must be bounded regular files")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("rules or trust record exceeds size limit")
	}
	return data, nil
}

// ReadUserFile reads a global rule/trust record without following its final link,
// importing workspace-owned data, or reopening a previously checked file.
func ReadUserFile(cwd, path string, limit int64) ([]byte, error) {
	root, err := canonical(cwd)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir, err := canonical(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	if workspace.Within(root, dir) {
		return nil, fmt.Errorf("user rules and trust records must be outside the workspace")
	}
	access, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer access.Close()
	return readRootFile(access, filepath.Base(abs), limit)
}
