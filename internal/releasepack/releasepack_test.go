package releasepack

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchivesAndSBOMAreReproducible(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "mythoscode")
	if err := os.WriteFile(binary, []byte("test binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	const epoch = 1760000000
	for _, goos := range []string{"linux", "windows"} {
		first, err := Archive(binary, t.TempDir(), "v1.2.3", goos, "amd64", epoch)
		if err != nil {
			t.Fatal(err)
		}
		second, err := Archive(binary, t.TempDir(), "v1.2.3", goos, "amd64", epoch)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := os.ReadFile(first)
		b, _ := os.ReadFile(second)
		if !bytes.Equal(a, b) {
			t.Fatalf("%s archive is not reproducible", goos)
		}
	}
	modules := []Module{{Path: "github.com/BigSmartie/Coding-Agent", Main: true}, {Path: "example.com/dependency", Version: "v1.0.0"}}
	a, err := SBOM(modules, "v1.2.3", epoch)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SBOM(modules, "v1.2.3", epoch)
	if err != nil || !bytes.Equal(a, b) || !strings.Contains(string(a), "SPDX-2.3") {
		t.Fatal("SBOM is not stable SPDX JSON")
	}
}

func TestArchiveRejectsUnsafeVersionAndChecksumSorted(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "mythoscode")
	_ = os.WriteFile(binary, []byte("binary"), 0o600)
	if _, err := Archive(binary, t.TempDir(), "../escape", "linux", "amd64", 1760000000); err == nil {
		t.Fatal("unsafe archive path accepted")
	}
	dir := t.TempDir()
	for _, name := range []string{"b.zip", "a.tar.gz"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path, err := Checksums(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "a.tar.gz\n") || strings.Index(string(data), "a.tar.gz") > strings.Index(string(data), "b.zip") {
		t.Fatalf("invalid checksums: %s, %v", data, err)
	}
}
