package egress

import (
	"strings"
	"testing"
)

func FuzzOriginPolicy(f *testing.F) {
	for _, seed := range []string{"https://example.com/path", "http://127.0.0.1/", "https://user:pass@example.com/", "https://example.com:8443/mcp", "https://[::1]/", "https://::"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 8192 {
			t.Skip()
		}
		origin, u, err := Origin(raw)
		if err != nil {
			return
		}
		if !strings.HasPrefix(origin, "https://") || u.User != nil || u.Fragment != "" || u.Scheme != "https" {
			t.Fatalf("unsafe URL accepted: %q => %q", raw, origin)
		}
		canonical, _, err := Origin(origin)
		if err != nil || canonical != origin {
			t.Fatalf("origin is not canonical: %q => %q, %v", raw, origin, err)
		}
	})
}
