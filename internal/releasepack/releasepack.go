// Package releasepack produces deterministic single-binary release archives.
package releasepack

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var safeVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func Archive(binary, outDir, version, goos, goarch string, epoch int64) (string, error) {
	if !safeVersion.MatchString(version) || (goos != "linux" && goos != "darwin" && goos != "windows") || (goarch != "amd64" && goarch != "arm64") || epoch <= 0 {
		return "", fmt.Errorf("invalid release target or source epoch")
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		return "", err
	}
	if len(data) == 0 || len(data) > 100<<20 {
		return "", fmt.Errorf("release binary must be 1–100 MiB")
	}
	name := "mycode"
	ext := ".tar.gz"
	if goos == "windows" {
		name += ".exe"
		ext = ".zip"
	}
	archiveName := fmt.Sprintf("mycode_%s_%s_%s%s", version, goos, goarch, ext)
	var output bytes.Buffer
	stamp := time.Unix(epoch, 0).UTC()
	if goos == "windows" {
		if stamp.Before(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)) {
			return "", fmt.Errorf("ZIP source epoch is before 1980")
		}
		writer := zip.NewWriter(&output)
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetModTime(stamp)
		header.SetMode(0o755)
		member, err := writer.CreateHeader(header)
		if err != nil {
			return "", err
		}
		if _, err := member.Write(data); err != nil {
			return "", err
		}
		if err := writer.Close(); err != nil {
			return "", err
		}
	} else {
		gzipWriter := gzip.NewWriter(&output)
		gzipWriter.Header.ModTime = stamp
		gzipWriter.Header.OS = 255
		tarWriter := tar.NewWriter(gzipWriter)
		header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), ModTime: stamp, Typeflag: tar.TypeReg, Uid: 0, Gid: 0, Format: tar.FormatPAX}
		if err := tarWriter.WriteHeader(header); err != nil {
			return "", err
		}
		if _, err := tarWriter.Write(data); err != nil {
			return "", err
		}
		if err := tarWriter.Close(); err != nil {
			return "", err
		}
		if err := gzipWriter.Close(); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(outDir, archiveName)
	if err := os.WriteFile(path, output.Bytes(), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func Checksums(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "SHA256SUMS" {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".zip") || strings.HasSuffix(entry.Name(), ".tar.gz") || strings.HasSuffix(entry.Name(), ".spdx.json") {
			if !entry.Type().IsRegular() {
				return "", fmt.Errorf("release artifact must be a regular file: %s", entry.Name())
			}
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no release artifacts to checksum")
	}
	sort.Strings(names)
	var output strings.Builder
	for _, name := range names {
		file, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return "", err
		}
		digest := sha256.New()
		_, err = io.Copy(digest, file)
		closeErr := file.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		fmt.Fprintf(&output, "%s  %s\n", hex.EncodeToString(digest.Sum(nil)), name)
	}
	path := filepath.Join(dir, "SHA256SUMS")
	return path, os.WriteFile(path, []byte(output.String()), 0o644)
}
