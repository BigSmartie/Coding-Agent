package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/BigSmartie/Coding-Agent/internal/egress"
)

type webTestPermission struct {
	testPermission
	seen []string
}

func (p *webTestPermission) EnsureWebRequest(_ context.Context, rawURL string) error {
	p.seen = append(p.seen, rawURL)
	return nil
}

func TestWebTextExtractionAndSearchResultBounds(t *testing.T) {
	page, err := readableWebText("text/html; charset=utf-8", []byte(`<html><head><style>hidden style</style></head><body><h1>Title &amp; More</h1><script>hidden script</script><p>Useful <b>text</b>.</p></body></html>`))
	if err != nil || strings.Contains(page.text, "hidden") || !strings.Contains(page.text, "Title & More") || !strings.Contains(page.text, "Useful text") {
		t.Fatalf("unsafe HTML extraction: %#v, %v", page, err)
	}
	if _, err := readableWebText("application/octet-stream", []byte("binary")); err == nil {
		t.Fatal("binary web response accepted")
	}
	large, err := readableWebText("text/plain", []byte(strings.Repeat("long ", 10000)))
	if err != nil || !large.truncated || len(large.text) > maxWebOutputBytes {
		t.Fatalf("web text was not bounded: %#v, %v", large, err)
	}
	htmlLarge, err := readableWebText("text/html", []byte("<p>"+strings.Repeat("large ", 10000)+"</p>"))
	if err != nil || !htmlLarge.truncated || len(htmlLarge.text) > maxWebOutputBytes {
		t.Fatalf("HTML text was not bounded: %#v, %v", htmlLarge, err)
	}
	fixture := `{"results":[{"title":"<b>Useful</b>","url":"https://docs.example/page#section","content":"A <em>snippet</em>"},{"title":"private","url":"http://unsafe.example","content":"skip"}]}`
	results, err := parseSearchResults([]byte(fixture))
	if err != nil || len(results) != 1 || results[0].Title != "Useful" || results[0].URL != "https://docs.example/page" || results[0].Snippet != "A snippet" {
		t.Fatalf("unsafe search results: %#v, %v", results, err)
	}
}

func TestWebToolsRequirePerURLApprovalAndFixedSearchEndpoint(t *testing.T) {
	registry := NewRegistry([]Definition{webFetchTool(), WebSearchTool("https://search.example/search")}, Metadata{})
	permission := &webTestPermission{}
	ctx := Context{Permission: permission, Network: &egress.Client{}}
	fetch := registry.Execute(context.Background(), "web_fetch", map[string]any{"url": "https://docs.example/a?q=one"}, ctx)
	if fetch.OK || len(permission.seen) != 1 || permission.seen[0] != "https://docs.example/a?q=one" {
		t.Fatalf("web fetch skipped URL approval: %#v, %#v", fetch, permission.seen)
	}
	search := registry.Execute(context.Background(), "web_search", map[string]any{"query": "a private topic"}, ctx)
	if search.OK || len(permission.seen) != 2 || !strings.HasPrefix(permission.seen[1], "https://search.example/search?") || !strings.Contains(permission.seen[1], "q=a+private+topic") || !strings.Contains(permission.seen[1], "format=json") {
		t.Fatalf("web search endpoint or query escaped: %#v, %#v", search, permission.seen)
	}
	denied := registry.Execute(context.Background(), "web_fetch", map[string]any{"url": "https://docs.example/a"}, Context{Network: &egress.Client{}})
	if denied.OK || !strings.Contains(denied.Output, "approval") {
		t.Fatalf("headless web request was allowed: %#v", denied)
	}
	invalid := registry.Execute(context.Background(), "web_fetch", json.RawMessage(`{"url":"http://localhost/"}`), ctx)
	if invalid.OK || len(permission.seen) != 2 {
		t.Fatalf("invalid web URL reached approval: %#v", invalid)
	}
}
