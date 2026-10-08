package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/config"
)

func TestTrustInvalidatesOnRuleChanges(t *testing.T) {
	cwd := t.TempDir()
	s := Store{Dir: t.TempDir()}
	rule := filepath.Join(cwd, "CLAUDE.md")
	if err := os.WriteFile(rule, []byte("initial rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash, _, err := WorkspaceFingerprint(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if s.Allowed(cwd, "workspace", hash) {
		t.Fatal("new workspace trusted")
	}
	if err := s.Grant(cwd, "workspace", hash, "wrong"); err == nil {
		t.Fatal("accepted wrong fingerprint")
	}
	if err := s.Grant(cwd, "workspace", hash, hash); err != nil {
		t.Fatal(err)
	}
	if !s.Allowed(cwd, "workspace", hash) {
		t.Fatal("grant lost")
	}
	if err := os.WriteFile(rule, []byte("changed rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, _, err := WorkspaceFingerprint(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if hash == changed || s.Allowed(cwd, "workspace", changed) {
		t.Fatal("changed rules retained trust")
	}
	if err := s.Revoke(cwd, "workspace"); err != nil {
		t.Fatal(err)
	}
	if s.Allowed(cwd, "workspace", hash) {
		t.Fatal("revocation failed")
	}
}

func TestWorkspaceSnapshotRetainsOnlyReviewedBytes(t *testing.T) {
	cwd := t.TempDir()
	s := Store{Dir: t.TempDir()}
	path := filepath.Join(cwd, "AGENTS.md")
	if err := os.WriteFile(path, []byte("approved"), 0600); err != nil {
		t.Fatal(err)
	}
	fingerprint, _, err := WorkspaceFingerprint(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot, err := s.Workspace(cwd); err != nil || snapshot != nil {
		t.Fatalf("untrusted snapshot exposed: %v", err)
	}
	if err := s.Grant(cwd, "workspace", fingerprint, fingerprint); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Workspace(cwd)
	if err != nil || snapshot == nil {
		t.Fatalf("approved snapshot unavailable: %v", err)
	}
	paths := snapshot.Paths()
	paths[0] = "changed-by-caller"
	if err := os.WriteFile(path, []byte("unapproved"), 0600); err != nil {
		t.Fatal(err)
	}
	if snapshot.Content("AGENTS.md") != "approved" || snapshot.Paths()[0] != "AGENTS.md" {
		t.Fatal("snapshot is not immutable")
	}
	if next, err := s.Workspace(cwd); err != nil || next != nil {
		t.Fatalf("changed file was approved: %v", err)
	}
}

func TestTrustRecordLinksCannotImportProjectGrants(t *testing.T) {
	for _, hardlink := range []bool{false, true} {
		t.Run(fmt.Sprint(hardlink), func(t *testing.T) {
			cwd := t.TempDir()
			s := Store{Dir: t.TempDir()}
			key, err := workspaceKey(cwd, "workspace")
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(records{Grants: map[string]string{key: "forged-hash"}})
			forged := filepath.Join(cwd, "forged-trust.json")
			if err := os.WriteFile(forged, data, 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.Dir, "trust.json")
			if hardlink {
				err = os.Link(forged, path)
			} else {
				err = os.Symlink(forged, path)
			}
			if err != nil {
				t.Skip("links unavailable")
			}
			if s.Allowed(cwd, "workspace", "forged-hash") {
				t.Fatal("workspace-owned trust record accepted")
			}
		})
	}
}

func TestRevokeWorksWithInvalidRulesAndMissingMCP(t *testing.T) {
	cwd := t.TempDir()
	s := Store{Dir: t.TempDir()}
	t.Setenv("MY_CODE_HOME", s.Dir)
	for _, capability := range []string{"workspace", "mcp:removed"} {
		if err := s.Grant(cwd, capability, "old", "old"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(strings.Repeat("x", 256<<10+1)), 0600); err != nil {
		t.Fatal(err)
	}
	// Malformed settings also must not prevent revoking a removed MCP server.
	if err := os.WriteFile(filepath.Join(s.Dir, "settings.json"), []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"trust", "workspace", "--revoke"}, {"trust", "mcp", "removed", "--revoke"}} {
		if _, handled, err := Handle(cwd, args); !handled || err != nil {
			t.Fatalf("revocation depended on current configuration: %v", err)
		}
	}
	if s.Allowed(cwd, "workspace", "old") || s.Allowed(cwd, "mcp:removed", "old") {
		t.Fatal("old grant retained after revocation")
	}
}

func TestProjectSnapshotRejectsRuleLinks(t *testing.T) {
	cwd := t.TempDir()
	other := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(other, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(other, filepath.Join(cwd, "AGENTS.md")); err != nil {
		t.Skip("hardlinks unavailable")
	}
	if _, _, err := WorkspaceFingerprint(cwd); err == nil {
		t.Fatal("hardlinked project rule accepted")
	}
}

func TestMCPConfigBoundToWorkspaceAndEnvironment(t *testing.T) {
	cwd := t.TempDir()
	s := Store{Dir: t.TempDir()}
	server := config.MCPServerConfig{Command: "server", Env: map[string]any{"MODE": "one"}}
	hash, err := MCPFingerprint(cwd, "demo", server)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(cwd, "mcp:demo", hash, hash); err != nil {
		t.Fatal(err)
	}
	server.Env["MODE"] = "two"
	changed, _ := MCPFingerprint(cwd, "demo", server)
	if hash == changed || s.Allowed(cwd, "mcp:demo", changed) {
		t.Fatal("changed MCP config remained trusted")
	}
	other, _ := MCPFingerprint(t.TempDir(), "demo", server)
	if other == changed {
		t.Fatal("MCP trust not workspace-bound")
	}
	if err := (Store{Dir: filepath.Join(cwd, ".my-code")}).Grant(cwd, "workspace", hash, hash); err == nil {
		t.Fatal("repository allowed to own trust store")
	}
}
