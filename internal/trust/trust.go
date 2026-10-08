// Package trust records user-reviewed workspace rules and MCP configurations.
// Trust records live outside the workspace and are invalidated by config changes.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BigSmartie/Coding-Agent/internal/config"
	"github.com/BigSmartie/Coding-Agent/internal/safety"
	"github.com/BigSmartie/Coding-Agent/internal/workspace"
)

type Store struct{ Dir string }
type records struct {
	Grants map[string]string `json:"grants"`
}

func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func workspaceKey(cwd, capability string) (string, error) {
	root, err := canonical(cwd)
	if err != nil {
		return "", err
	}
	return root + "\x00" + capability, nil
}

func (s Store) path(cwd string) (string, error) {
	root, err := canonical(cwd)
	if err != nil {
		return "", err
	}
	dir, err := workspace.Canonical(s.Dir)
	if err != nil {
		return "", err
	}
	if workspace.Within(root, dir) {
		return "", fmt.Errorf("trust store must be outside the workspace; remove the workspace MY_CODE_HOME override")
	}
	return filepath.Join(dir, "trust.json"), nil
}

func (s Store) load(cwd string) (records, error) {
	path, err := s.path(cwd)
	if err != nil {
		return records{}, err
	}
	r := records{Grants: map[string]string{}}
	data, err := ReadUserFile(cwd, path, 1<<20)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.Grants == nil {
		r.Grants = map[string]string{}
	}
	return r, nil
}

func (s Store) Allowed(cwd, capability, fingerprint string) bool {
	key, err := workspaceKey(cwd, capability)
	if err != nil || fingerprint == "" {
		return false
	}
	r, err := s.load(cwd)
	return err == nil && r.Grants[key] == fingerprint
}

func (s Store) Grant(cwd, capability, fingerprint, accepted string) error {
	if fingerprint == "" || accepted != fingerprint {
		return fmt.Errorf("fingerprint mismatch; review the current configuration before accepting")
	}
	key, err := workspaceKey(cwd, capability)
	if err != nil {
		return err
	}
	r, err := s.load(cwd)
	if err != nil {
		return err
	}
	r.Grants[key] = fingerprint
	path, err := s.path(cwd)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return safety.PrivateWrite(path, append(data, '\n'))
}

func (s Store) Revoke(cwd, capability string) error {
	key, err := workspaceKey(cwd, capability)
	if err != nil {
		return err
	}
	r, err := s.load(cwd)
	if err != nil {
		return err
	}
	delete(r.Grants, key)
	path, err := s.path(cwd)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return safety.PrivateWrite(path, append(data, '\n'))
}

func WorkspaceFingerprint(cwd string) (string, []string, error) {
	snapshot, err := captureWorkspace(cwd)
	if err != nil {
		return "", nil, err
	}
	return snapshot.fingerprint, snapshot.Paths(), nil
}

func MCPFingerprint(cwd, name string, server config.MCPServerConfig) (string, error) {
	root, err := canonical(cwd)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		Version                int
		Workspace, Name, Image string
		Config                 config.MCPServerConfig
	}{1, root, name, os.Getenv("MY_CODE_SANDBOX_IMAGE"), server})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Handle is deliberately a user CLI operation; model tools cannot grant trust.
func Handle(cwd string, argv []string) (string, bool, error) {
	if len(argv) == 0 || argv[0] != "trust" {
		return "", false, nil
	}
	usage := "Usage: mycode trust workspace [--accept FINGERPRINT | --revoke]\n       mycode trust mcp NAME [--accept FINGERPRINT | --revoke]"
	if len(argv) < 2 {
		return usage, true, nil
	}
	store := Store{Dir: config.AppDir()}
	if _, err := store.path(cwd); err != nil {
		return "", true, err
	}
	capability, fingerprint, details := "", "", ""
	var err error
	next := 2
	// Revocation must work even if the current configuration was removed,
	// corrupted or made unreadable after it was granted.
	switch argv[1] {
	case "workspace":
		capability = "workspace"
	case "mcp":
		if len(argv) < 3 {
			return usage, true, fmt.Errorf("MCP server name required")
		}
		capability, next = "mcp:"+argv[2], 3
	default:
		return usage, true, fmt.Errorf("unknown trust capability")
	}
	if len(argv) == next+1 && argv[next] == "--revoke" {
		if err := store.Revoke(cwd, capability); err != nil {
			return "", true, err
		}
		return "Trust revoked for " + safety.EscapeInline(capability), true, nil
	}
	switch argv[1] {
	case "workspace":
		capability = "workspace"
		var paths []string
		fingerprint, paths, err = WorkspaceFingerprint(cwd)
		details = "Project instructions and skill descriptions:\n  " + strings.Join(paths, "\n  ")
	case "mcp":
		if len(argv) < 3 {
			return usage, true, fmt.Errorf("MCP server name required")
		}
		name := argv[2]
		next = 3
		capability = "mcp:" + name
		var settings config.Settings
		settings, err = config.LoadEffectiveSettings(cwd)
		if err == nil {
			server, ok := settings.MCPServers[name]
			if !ok {
				return "", true, fmt.Errorf("unknown MCP server %q", name)
			}
			fingerprint, err = MCPFingerprint(cwd, name, server)
			envKeys := []string{}
			for key := range server.Env {
				envKeys = append(envKeys, key)
			}
			sort.Strings(envKeys)
			args, _ := json.Marshal(server.Args)
			details = fmt.Sprintf("MCP server: %s\nCommand: %s\nArguments: %s\nWorking directory: %s\nExplicit environment keys: %s\nSandbox image: %s\nExecution: isolated workspace snapshot, network disabled; changes are temporary.", name, server.Command, args, server.CWD, strings.Join(envKeys, ", "), os.Getenv("MY_CODE_SANDBOX_IMAGE"))
		}
	default:
		return usage, true, fmt.Errorf("unknown trust capability")
	}
	if err != nil {
		return "", true, err
	}
	if len(argv) == next+2 && argv[next] == "--accept" {
		if err := store.Grant(cwd, capability, fingerprint, argv[next+1]); err != nil {
			return "", true, err
		}
		return "Trust recorded for " + capability, true, nil
	}
	if len(argv) != next {
		return usage, true, fmt.Errorf("invalid trust arguments")
	}
	status := "not trusted"
	if store.Allowed(cwd, capability, fingerprint) {
		status = "trusted"
	}
	return safety.EscapeTerminal(safety.Redact(nil, fmt.Sprintf("%s\nStatus: %s\nFingerprint: %s\nAfter reviewing these files/configuration, repeat this command with --accept %s.", details, status, fingerprint, fingerprint))), true, nil
}
