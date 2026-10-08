package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/brand"
	"github.com/BigSmartie/Coding-Agent/internal/tools"
	"github.com/BigSmartie/Coding-Agent/internal/trust"
)

type Loaded struct {
	tools.SkillSummary
	Content string
}

type Store struct {
	CWD             string
	Home            string
	ProjectSnapshot func() *trust.WorkspaceSnapshot
}

func NewStore(cwd, home string) Store {
	return Store{CWD: cwd, Home: home}
}

func (s Store) Discover(ctx context.Context) ([]tools.SkillSummary, error) {
	byName := map[string]tools.SkillSummary{}
	for _, skill := range s.projectSkills() {
		if _, exists := byName[skill.Name]; !exists {
			byName[skill.Name] = skill.SkillSummary
		}
	}
	for _, root := range s.roots() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(root.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if _, exists := byName[name]; exists {
				continue
			}
			skillPath := filepath.Join(root.path, name, "SKILL.md")
			if !validName(name) {
				continue
			}
			content, err := s.readSkill(root, name)
			if err != nil {
				continue
			}
			byName[name] = tools.SkillSummary{
				Name:        name,
				Description: extractDescription(string(content)),
				Path:        skillPath,
				Source:      root.source,
			}
		}
	}
	out := make([]tools.SkillSummary, 0, len(byName))
	for _, skill := range byName {
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s Store) Load(ctx context.Context, name string) (Loaded, error) {
	if !validName(name) {
		return Loaded{}, fmt.Errorf("invalid skill name")
	}
	if err := ctx.Err(); err != nil {
		return Loaded{}, err
	}
	for _, skill := range s.projectSkills() {
		if skill.Name == name {
			return skill, nil
		}
	}
	for _, root := range s.roots() {
		if err := ctx.Err(); err != nil {
			return Loaded{}, err
		}
		skillPath := filepath.Join(root.path, strings.TrimSpace(name), "SKILL.md")
		content, err := s.readSkill(root, name)
		if err != nil {
			continue
		}
		return Loaded{
			SkillSummary: tools.SkillSummary{
				Name:        strings.TrimSpace(name),
				Description: extractDescription(string(content)),
				Path:        skillPath,
				Source:      root.source,
			},
			Content: string(content),
		}, nil
	}
	return Loaded{}, errors.New("Unknown skill: " + name)
}

func (s Store) LoadSkill(ctx context.Context, name string) (string, error) {
	loaded, err := s.Load(ctx, name)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"SKILL: " + loaded.Name,
		"SOURCE: " + loaded.Source,
		"PATH: " + loaded.Path,
		"",
		loaded.Content,
	}, "\n"), nil
}

func (s Store) Install(_ context.Context, sourcePath, name, scope string) (string, error) {
	content, err := readBoundedFile(filepath.Join(sourcePath, "SKILL.md"))
	if err != nil {
		content, err = readBoundedFile(sourcePath)
		if err != nil {
			return "", err
		}
	}
	if scope != "" && scope != "user" && scope != "project" {
		return "", fmt.Errorf("invalid skill scope")
	}
	if name == "" {
		name = filepath.Base(sourcePath)
		if name == "SKILL.md" {
			name = filepath.Base(filepath.Dir(sourcePath))
		}
	}
	if !validName(name) {
		return "", fmt.Errorf("invalid skill name")
	}
	targetRoot := filepath.Join(s.Home, brand.ConfigDirName, "skills")
	if scope == "project" {
		targetRoot = filepath.Join(s.CWD, brand.ConfigDirName, "skills")
	}
	target := filepath.Join(targetRoot, name, "SKILL.md")
	access, err := s.openRoot(root{targetRoot, scope}, true)
	if err != nil {
		return "", err
	}
	defer access.Close()
	if err := access.MkdirAll(name, 0o700); err != nil {
		return "", err
	}
	if err := rejectLink(access, name); err != nil {
		return "", err
	}
	if err := rejectLink(access, filepath.Join(name, "SKILL.md")); err != nil {
		return "", err
	}
	return target, access.WriteFile(filepath.Join(name, "SKILL.md"), content, 0o600)
}

func (s Store) Remove(_ context.Context, name, scope string) (string, bool, error) {
	if !validName(name) {
		return "", false, fmt.Errorf("invalid skill name")
	}
	if scope != "" && scope != "user" && scope != "project" {
		return "", false, fmt.Errorf("invalid skill scope")
	}
	targetRoot := filepath.Join(s.Home, brand.ConfigDirName, "skills")
	if scope == "project" {
		targetRoot = filepath.Join(s.CWD, brand.ConfigDirName, "skills")
	}
	target := filepath.Join(targetRoot, name)
	access, err := s.openRoot(root{targetRoot, scope}, false)
	if os.IsNotExist(err) {
		return target, false, nil
	}
	if err != nil {
		return target, false, err
	}
	defer access.Close()
	if err := rejectLink(access, name); err != nil {
		return target, false, err
	}
	err = access.RemoveAll(name)
	return target, err == nil, err
}

type root struct {
	path   string
	source string
}

func (s Store) roots() []root {
	roots := []root{
		{filepath.Join(s.Home, brand.ConfigDirName, "skills"), "user"},
		{filepath.Join(s.Home, ".claude", "skills"), "compat_user"},
	}
	return roots
}

func (s Store) projectSkills() []Loaded {
	if s.ProjectSnapshot == nil {
		return nil
	}
	snapshot := s.ProjectSnapshot()
	if !snapshot.ForWorkspace(s.CWD) {
		return nil
	}
	var out []Loaded
	for _, source := range []struct{ prefix, label string }{{".my-code/skills/", "project"}, {".claude/skills/", "compat_project"}} {
		for _, path := range snapshot.Paths() {
			if !strings.HasPrefix(path, source.prefix) || !strings.HasSuffix(path, "/SKILL.md") {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(path, source.prefix), "/SKILL.md")
			if !validName(name) {
				continue
			}
			content := snapshot.Content(path)
			out = append(out, Loaded{SkillSummary: tools.SkillSummary{Name: name, Description: extractDescription(content), Path: filepath.Join(s.CWD, filepath.FromSlash(path)), Source: source.label}, Content: content})
		}
	}
	return out
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func validName(name string) bool { return namePattern.MatchString(name) }

func (s Store) openRoot(location root, create bool) (*os.Root, error) {
	base := s.Home
	if location.source == "project" || location.source == "compat_project" {
		base = s.CWD
	}
	parent, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	rel, err := filepath.Rel(base, location.path)
	if err != nil {
		return nil, err
	}
	if create {
		if err := parent.MkdirAll(rel, 0o700); err != nil {
			return nil, err
		}
	}
	current := ""
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := rejectLink(parent, current); err != nil {
			return nil, err
		}
	}
	return parent.OpenRoot(rel)
}

func rejectLink(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("skill links are not allowed")
	}
	return nil
}

func (s Store) readSkill(location root, name string) ([]byte, error) {
	access, err := s.openRoot(location, false)
	if err != nil {
		return nil, err
	}
	defer access.Close()
	if err := rejectLink(access, name); err != nil {
		return nil, err
	}
	rel := filepath.Join(name, "SKILL.md")
	if err := rejectLink(access, rel); err != nil {
		return nil, err
	}
	return trust.ReadUserFile(s.CWD, filepath.Join(location.path, rel), 256<<10)
}

func readBoundedFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readSkillFile(file)
}

func readSkillFile(file *os.File) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("skill must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (256<<10)+1))
	if err == nil && len(data) > 256<<10 {
		return nil, fmt.Errorf("skill exceeds 256 KiB limit")
	}
	return data, err
}

func extractDescription(markdown string) string {
	blocks := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n\n")
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" || strings.HasPrefix(block, "#") {
			continue
		}
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				return strings.ReplaceAll(line, "`", "")
			}
		}
	}
	return "No description provided."
}
