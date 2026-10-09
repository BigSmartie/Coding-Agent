package mcp

import (
	"bytes"
	"testing"
)

func FuzzMCPFrameBounds(f *testing.F) {
	f.Add([]byte("Content-Length: 2\r\n\r\n{}"))
	f.Add([]byte("Content-Length: 99999999\r\n\r\n{}"))
	f.Add([]byte("garbage\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 8192 {
			t.Skip()
		}
		body, err := readContentLengthMessage(bytes.NewReader(data))
		if err == nil && (len(body) == 0 || len(body) > maxMessageBytes) {
			t.Fatalf("invalid MCP frame accepted: %d bytes", len(body))
		}
	})
}
