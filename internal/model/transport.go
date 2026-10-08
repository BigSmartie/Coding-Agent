package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	requestTimeout   = 120 * time.Second
	maxRequestBytes  = 8 << 20
	maxResponseBytes = 16 << 20
	maxEventBytes    = 1 << 20
	maxRetries       = 2
)

// RequestError never contains response bodies, credentials, or endpoint URLs.
type RequestError struct {
	StatusCode int
	Reason     string
}

func (e *RequestError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("model request failed (HTTP %d): %s", e.StatusCode, e.Reason)
	}
	return "model request failed: " + e.Reason
}

func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: transport, Timeout: requestTimeout, CheckRedirect: denyRedirect}
}

func denyRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func requestJSON(ctx context.Context, client *http.Client, endpoint string, payload any, auth func(*http.Request)) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &RequestError{Reason: "request cannot be encoded"}
	}
	if len(body) > maxRequestBytes {
		return nil, &RequestError{Reason: "request exceeds 8 MiB limit; shorten the conversation or tool output"}
	}
	if client == nil {
		client = newHTTPClient()
	}
	// Redirects must never forward credentials, including custom x-api-key headers.
	bounded := *client
	bounded.CheckRedirect = denyRedirect
	if bounded.Timeout <= 0 || bounded.Timeout > requestTimeout {
		bounded.Timeout = requestTimeout
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, &RequestError{Reason: "invalid endpoint"}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		auth(req)
		res, err := bounded.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var networkError net.Error
			if attempt < maxRetries && errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) {
				if err := waitRetry(ctx, retryDelay(attempt)); err != nil {
					return nil, err
				}
				continue
			}
			return nil, &RequestError{Reason: "transport failure; check connectivity and TLS settings"}
		}
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return res, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		delay := retryDelay(attempt)
		if specified, ok := parseRetryAfter(res.Header.Get("Retry-After"), time.Now()); ok {
			delay = specified
		}
		if attempt < maxRetries && transientStatus(res.StatusCode) && delay <= 30*time.Second {
			if err := waitRetry(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		reason := http.StatusText(res.StatusCode)
		if reason == "" {
			reason = "provider rejected the request"
		}
		return nil, &RequestError{StatusCode: res.StatusCode, Reason: reason}
	}
}

func transientStatus(status int) bool {
	switch status {
	case 408, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

func retryDelay(attempt int) time.Duration {
	return (250 * time.Millisecond << attempt) + time.Duration(rand.IntN(251))*time.Millisecond
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil {
		delay := date.Sub(now)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func decodeResponse(body io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return &RequestError{Reason: "response read interrupted"}
	}
	if len(data) > maxResponseBytes {
		return &RequestError{Reason: "response exceeds 16 MiB limit"}
	}
	if err := json.Unmarshal(data, target); err != nil {
		return &RequestError{Reason: "provider returned invalid JSON"}
	}
	return nil
}

// readSSE never retries after response bytes have been delivered. Event and
// total limits apply even to comments, unknown events and malformed servers.
func readSSE(body io.Reader, consume func(string, []byte) error) error {
	limited := &io.LimitedReader{R: body, N: maxResponseBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), maxEventBytes+1)
	var event string
	var data []byte
	flush := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		err := consume(event, bytes.TrimSuffix(data, []byte("\n")))
		event, data = "", nil
		return err
	}
	for scanner.Scan() {
		if limited.N == 0 {
			return &RequestError{Reason: "response exceeds 16 MiB limit"}
		}
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			if len(data)+len(value)+1 > maxEventBytes {
				return &RequestError{Reason: "stream event exceeds 1 MiB limit"}
			}
			data = append(data, value...)
			data = append(data, '\n')
		}
	}
	if limited.N == 0 {
		return &RequestError{Reason: "response exceeds 16 MiB limit"}
	}
	if scanner.Err() != nil {
		return &RequestError{Reason: "stream interrupted or event size limit exceeded"}
	}
	return flush()
}

func isSSE(res *http.Response) bool {
	return strings.HasPrefix(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream")
}

func emitText(onTextDelta func(string), text string) {
	if onTextDelta != nil && text != "" {
		onTextDelta(text)
	}
}

func rawObject(raw json.RawMessage) map[string]any {
	var item map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&item)
	return item
}
