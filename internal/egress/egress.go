// Package egress is the sole host-side path for model-triggered outbound HTTP.
// It pins DNS results, rejects non-public addresses, and never uses host proxies.
package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const MaxResponseBytes = 4 << 20
const maxRequestBytes = 1 << 20
const maxSessionBytes = 32 << 20

type Permission interface {
	EnsureNetwork(context.Context, string, string) error
}

type Event struct {
	Origin string
	Method string
	Phase  string
	Status int
	Bytes  int
}

type Client struct {
	Permission          Permission
	Audit               func(Event) error
	mu                  sync.Mutex
	used                int64
	allowPrivateForTest bool
	rootCAsForTest      *x509.CertPool
}

type Response struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	Body        []byte `json:"body"`
}

// Origin rejects credential-bearing, non-HTTPS and malformed request URLs.
func Origin(raw string) (string, *url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", nil, fmt.Errorf("outbound URL must be an absolute HTTPS URL without userinfo or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasSuffix(host, ".") || strings.ContainsAny(host, "\x00\r\n\t ") {
		return "", nil, fmt.Errorf("invalid outbound hostname")
	}
	if strings.Contains(host, ":") {
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is6() || ip.Zone() != "" {
			return "", nil, fmt.Errorf("invalid outbound IPv6 hostname")
		}
		host = "[" + host + "]"
	} else if !validDNSHost(host) {
		return "", nil, fmt.Errorf("invalid outbound hostname")
	}
	if port := u.Port(); port != "" && port != "443" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", nil, fmt.Errorf("invalid outbound port")
		}
		host += ":" + strconv.Itoa(value)
	}
	return "https://" + host, u, nil
}

func validDNSHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
				return false
			}
		}
	}
	return true
}

// Fetch permits only GET and POST. The caller may choose Accept and
// Content-Type; MCP routing headers have a separate explicit allowlist.
func (c *Client) Fetch(ctx context.Context, method, raw string, headers http.Header, body []byte) (Response, error) {
	if method != http.MethodGet && method != http.MethodPost {
		return Response{}, fmt.Errorf("outbound method is not allowed")
	}
	if len(body) > maxRequestBytes {
		return Response{}, fmt.Errorf("outbound request exceeds 1 MiB")
	}
	origin, u, err := Origin(raw)
	if err != nil {
		return Response{}, err
	}
	if c == nil || c.Permission == nil {
		return Response{}, fmt.Errorf("outbound network access requires explicit approval")
	}
	if err := c.Permission.EnsureNetwork(ctx, origin, method); err != nil {
		return Response{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used+int64(len(body))+MaxResponseBytes > maxSessionBytes {
		return Response{}, fmt.Errorf("session network traffic limit reached")
	}
	if c.Audit != nil {
		if err := c.Audit(Event{Origin: origin, Method: method, Phase: "requested"}); err != nil {
			return Response{}, err
		}
	}
	failed := true
	defer func() {
		if failed && c.Audit != nil {
			_ = c.Audit(Event{Origin: origin, Method: method, Phase: "failed"})
		}
	}()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	addresses, err := resolvePublic(ctx, u.Hostname(), c.allowPrivateForTest)
	if err != nil {
		return Response{}, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: c.rootCAsForTest},
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var last error
			for _, ip := range addresses {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	for name, values := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if canonical != "Accept" && canonical != "Content-Type" && canonical != "Mcp-Protocol-Version" && canonical != "Mcp-Method" && canonical != "Mcp-Name" && !strings.HasPrefix(canonical, "Mcp-Param-") {
			return Response{}, fmt.Errorf("outbound header %s is not allowed", canonical)
		}
		for _, value := range values {
			request.Header.Add(canonical, value)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return Response{}, fmt.Errorf("outbound HTTPS request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return Response{}, fmt.Errorf("outbound redirects are blocked")
	}
	if response.ContentLength > MaxResponseBytes {
		return Response{}, fmt.Errorf("outbound response exceeds 4 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		return Response{}, err
	}
	if len(data) > MaxResponseBytes {
		return Response{}, fmt.Errorf("outbound response exceeds 4 MiB")
	}
	c.used += int64(len(body) + len(data))
	result := Response{Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: data}
	if c.Audit != nil {
		if err := c.Audit(Event{Origin: origin, Method: method, Phase: "completed", Status: result.Status, Bytes: len(data)}); err != nil {
			return Response{}, err
		}
	}
	failed = false
	return result, nil
}

func resolvePublic(ctx context.Context, host string, allowPrivate bool) ([]netip.Addr, error) {
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = append(ips, ip.Unmap())
	} else {
		resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("outbound DNS lookup failed: %w", err)
		}
		for _, item := range resolved {
			ip, ok := netip.AddrFromSlice(item.IP)
			if ok {
				ips = append(ips, ip.Unmap())
			}
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("outbound DNS returned no addresses")
	}
	for _, ip := range ips {
		if !allowPrivate && !publicIP(ip) {
			return nil, fmt.Errorf("outbound DNS returned a non-public address")
		}
	}
	return ips, nil
}

func publicIP(ip netip.Addr) bool {
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, raw := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		prefix := netip.MustParsePrefix(raw)
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
