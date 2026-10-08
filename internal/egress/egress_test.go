package egress

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type allowOrigin struct{ seen []string }

func (p *allowOrigin) EnsureNetwork(_ context.Context, origin, method string) error {
	p.seen = append(p.seen, method+" "+origin)
	return nil
}

func TestPublicAddressPolicyRejectsSSRFTargets(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "169.254.169.254", "10.0.0.1", "100.64.1.1", "[::1]", "[fc00::1]"} {
		if _, err := resolvePublic(context.Background(), strings.Trim(host, "[]"), false); err == nil {
			t.Errorf("private host accepted: %s", host)
		}
	}
	for _, raw := range []string{"http://example.com", "https://user:pass@example.com/", "https://example.com/#fragment", "https://example.com:99999/"} {
		if _, _, err := Origin(raw); err == nil {
			t.Errorf("unsafe URL accepted: %s", raw)
		}
	}
}

func TestExactOriginGrantAndRedirectDenial(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/other", http.StatusFound)
			return
		}
		fmt.Fprint(w, "safe response")
	}))
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	permission := &allowOrigin{}
	var phases []string
	client := &Client{Permission: permission, allowPrivateForTest: true, rootCAsForTest: pool, Audit: func(event Event) error { phases = append(phases, event.Phase); return nil }}
	got, err := client.Fetch(context.Background(), "GET", server.URL+"/path?q=1", nil, nil)
	if err != nil || got.Status != 200 || string(got.Body) != "safe response" {
		t.Fatalf("approved response missing: %#v, %v", got, err)
	}
	if len(permission.seen) != 1 || permission.seen[0] != "GET "+server.URL {
		t.Fatalf("grant widened beyond origin: %#v", permission.seen)
	}
	if _, err := client.Fetch(context.Background(), "GET", server.URL+"/redirect", nil, nil); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect followed: %v", err)
	}
	if strings.Join(phases, ",") != "requested,completed,requested,failed" {
		t.Fatalf("audit phases missing: %#v", phases)
	}
}

func TestDeniedOrMalformedRequestNeverDials(t *testing.T) {
	client := &Client{}
	if _, err := client.Fetch(context.Background(), "GET", "https://example.com/", nil, nil); err == nil || !strings.Contains(err.Error(), "approval") {
		t.Fatalf("request without permission was allowed: %v", err)
	}
	permission := &allowOrigin{}
	client.Permission = permission
	if _, err := client.Fetch(context.Background(), "DELETE", "https://example.com/", nil, nil); err == nil {
		t.Fatal("unsafe method was accepted")
	}
	if len(permission.seen) != 0 {
		t.Fatal("malformed request reached permission or network")
	}
}

func TestStreamingTransportPinsOriginBoundsBodyAndBlocksRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/target", http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == "/large" {
			w.Write([]byte(strings.Repeat("x", MaxResponseBytes+1)))
			return
		}
		w.Write([]byte("stream response"))
	}))
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	client := &Client{Permission: &allowOrigin{}, allowPrivateForTest: true, rootCAsForTest: pool}
	request, err := http.NewRequest("POST", server.URL+"/ok", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.HTTPClient().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(data) != "stream response" {
		t.Fatalf("wrong streaming response: %q, %v", data, err)
	}
	request, _ = http.NewRequest("GET", server.URL+"/redirect", nil)
	if _, err := client.HTTPClient().Do(request); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("streaming redirect followed: %v", err)
	}
	request, _ = http.NewRequest("GET", server.URL+"/large", nil)
	response, err = client.HTTPClient().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized stream accepted: %v", err)
	}
}
