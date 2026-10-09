package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HTTPClient is for streaming protocols such as MCP. Every request is checked
// independently, including legacy GET/DELETE requests and retries.
func (c *Client) HTTPClient() *http.Client {
	return &http.Client{Transport: guardedTransport{client: c}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type guardedTransport struct{ client *Client }

func (guard guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c := guard.client
	if c == nil || c.Permission == nil {
		return nil, fmt.Errorf("outbound network access requires explicit approval")
	}
	if req.Method != http.MethodGet && req.Method != http.MethodPost && req.Method != http.MethodDelete {
		return nil, fmt.Errorf("outbound method is not allowed")
	}
	origin, u, err := Origin(req.URL.String())
	if err != nil {
		return nil, err
	}
	if req.ContentLength > maxRequestBytes {
		return nil, fmt.Errorf("outbound request exceeds 1 MiB")
	}
	if req.Method == http.MethodPost && req.ContentLength < 0 {
		return nil, fmt.Errorf("streaming outbound request bodies are not allowed")
	}
	for name := range req.Header {
		canonical := http.CanonicalHeaderKey(name)
		if canonical != "Accept" && canonical != "Content-Type" && canonical != "Mcp-Protocol-Version" && canonical != "Mcp-Method" && canonical != "Mcp-Name" && canonical != "Mcp-Session-Id" && canonical != "Last-Event-Id" && canonical != "User-Agent" && !strings.HasPrefix(canonical, "Mcp-Param-") {
			return nil, fmt.Errorf("outbound header %s is not allowed", canonical)
		}
	}
	if err := c.Permission.EnsureNetwork(req.Context(), origin, req.Method); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.used+maxRequestBytes+MaxResponseBytes > maxSessionBytes {
		c.mu.Unlock()
		return nil, fmt.Errorf("session network traffic limit reached")
	}
	c.used += max(req.ContentLength, 0)
	c.mu.Unlock()
	if c.Audit != nil {
		if err := c.Audit(Event{Origin: origin, Method: req.Method, Phase: "requested"}); err != nil {
			return nil, err
		}
	}
	fail := func(err error) (*http.Response, error) {
		if c.Audit != nil {
			_ = c.Audit(Event{Origin: origin, Method: req.Method, Phase: "failed"})
		}
		return nil, err
	}
	addresses, err := resolvePublic(req.Context(), u.Hostname(), c.allowPrivateForTest)
	if err != nil {
		return fail(err)
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
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
	response, err := transport.RoundTrip(req)
	if err != nil {
		transport.CloseIdleConnections()
		return fail(err)
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		response.Body.Close()
		transport.CloseIdleConnections()
		return fail(fmt.Errorf("outbound redirects are blocked"))
	}
	if response.ContentLength > MaxResponseBytes {
		response.Body.Close()
		transport.CloseIdleConnections()
		return fail(fmt.Errorf("outbound response exceeds 4 MiB"))
	}
	response.Body = &boundedBody{ReadCloser: response.Body, client: c, transport: transport, origin: origin, method: req.Method, status: response.StatusCode}
	return response, nil
}

type boundedBody struct {
	io.ReadCloser
	client         *Client
	transport      *http.Transport
	origin, method string
	status         int
	read           int
	mu             sync.Mutex
	closed         bool
}

func (b *boundedBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	if len(p) == 0 {
		return 0, nil
	}
	remaining := MaxResponseBytes - b.read
	if remaining < len(p) {
		p = p[:remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.read += n
	b.client.mu.Lock()
	b.client.used += int64(n)
	overSession := b.client.used > maxSessionBytes
	b.client.mu.Unlock()
	if b.read > MaxResponseBytes || overSession {
		_ = b.closeLocked("failed")
		return 0, fmt.Errorf("outbound traffic limit reached")
	}
	return n, err
}

func (b *boundedBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeLocked("completed")
}

func (b *boundedBody) closeLocked(phase string) error {
	if b.closed {
		return nil
	}
	b.closed = true
	err := b.ReadCloser.Close()
	b.transport.CloseIdleConnections()
	if b.client.Audit != nil {
		_ = b.client.Audit(Event{Origin: b.origin, Method: b.method, Phase: phase, Status: b.status, Bytes: b.read})
	}
	return err
}
