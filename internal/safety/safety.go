// Package safety contains shared boundaries for untrusted text and persisted data.
package safety

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type secretsKey struct{}

// WithSecrets registers exact runtime credentials without exposing them to tools.
func WithSecrets(ctx context.Context, values ...string) context.Context {
	secrets, _ := ctx.Value(secretsKey{}).([]string)
	out := append([]string(nil), secrets...)
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return context.WithValue(ctx, secretsKey{}, out)
}

var keyPattern = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}`)
var bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/-]{8,}=*`)
var assignmentPattern = regexp.MustCompile(`(?i)((?:api[_-]?key|auth[_-]?token|access[_-]?token|password|secret)\s*["']?\s*[:=]\s*["']?)[^\s,"'}]+`)

func Redact(ctx context.Context, value string) string {
	value = RedactKnownSecrets(ctx, value)
	value = keyPattern.ReplaceAllString(value, "[REDACTED]")
	value = bearerPattern.ReplaceAllString(value, "${1}[REDACTED]")
	return assignmentPattern.ReplaceAllString(value, "${1}[REDACTED]")
}

// RedactKnownSecrets also applies to opaque protocol bytes: a provider must not
// be able to make an actual runtime credential persist by labelling it encrypted.
func RedactKnownSecrets(ctx context.Context, value string) string {
	if ctx != nil {
		secrets, _ := ctx.Value(secretsKey{}).([]string)
		for _, secret := range secrets {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

// RedactJSON traverses values, retaining valid JSON and opaque provider fields.
func RedactJSON(ctx context.Context, data []byte) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var visit func(any) any
	visit = func(v any) any {
		switch item := v.(type) {
		case string:
			return Redact(ctx, item)
		case []any:
			for i := range item {
				item[i] = visit(item[i])
			}
			return item
		case map[string]any:
			for key, child := range item {
				if IsSecretName(key) {
					item[key] = "[REDACTED]"
				} else {
					item[key] = visit(child)
				}
			}
			return item
		default:
			return v
		}
	}
	return json.MarshalIndent(visit(value), "", "  ")
}

func IsSecretName(name string) bool {
	name = strings.ToUpper(name)
	return strings.Contains(name, "API_KEY") || strings.Contains(name, "APIKEY") || strings.Contains(name, "AUTH_TOKEN") || strings.Contains(name, "ACCESS_TOKEN") || strings.Contains(name, "PASSWORD") || strings.Contains(name, "SECRET")
}

// EscapeTerminal visibly quotes controls instead of executing ANSI/OSC sequences.
// Newlines and tabs are retained; carriage returns cannot overwrite a prompt.
func EscapeTerminal(value string) string {
	var out strings.Builder
	for _, r := range strings.ToValidUTF8(value, "\uFFFD") {
		if r == '\n' || r == '\t' {
			out.WriteRune(r)
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			if r <= 0xff {
				fmt.Fprintf(&out, "\\x%02x", r)
			} else {
				fmt.Fprintf(&out, "\\u%04x", r)
			}
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// EscapeInline is for paths, server names and other single-line metadata.
func EscapeInline(value string) string {
	return strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(EscapeTerminal(value))
}

// PrivateWrite leaves the previous file intact until the replacement is complete.
// Windows applies the containing profile directory's ACL; Unix uses 0700/0600.
func PrivateWrite(path string, data []byte) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty persistence path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("refusing to overwrite non-regular state file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(dir, ".mythoscode-state-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return nil
}
