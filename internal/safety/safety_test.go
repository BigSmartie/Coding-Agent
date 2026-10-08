package safety

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactionRetainsValidJSON(t *testing.T) {
	ctx := WithSecrets(context.Background(), "exact-real-value")
	data, err := RedactJSON(ctx, []byte(`{"message":"key exact-real-value","api_key":"opaque","nested":["Bearer abcdefghijkl","sk-abcdefghijklmnop"]}`))
	if err != nil || !json.Valid(data) {
		t.Fatalf("invalid result: %v", err)
	}
	for _, secret := range []string{"exact-real-value", "opaque", "abcdefghijkl"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("secret persisted")
		}
	}
}

func TestTerminalControlsAreVisible(t *testing.T) {
	got := EscapeTerminal("ok\x1b[2J\x1b]52;c;abc\a\r\u202E中\n\t")
	if strings.ContainsAny(got, "\x1b\a\r\u202E") || !strings.Contains(got, `\x1b[2J`) || !strings.HasSuffix(got, "中\n\t") {
		t.Fatalf("unsafe output: %q", got)
	}
}

func TestPrivateWriteReplacesAndRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := PrivateWrite(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := PrivateWrite(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Fatal("replacement not written")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Skip("symlink unavailable")
	}
	if err := PrivateWrite(link, []byte("bad")); err == nil {
		t.Fatal("followed symlink")
	}
}
