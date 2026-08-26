package tools

import (
	"context"
	"strings"
	"sync"
	"testing"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
)

// ---------------------------------------------------------------------------
// extractExaContent
// ---------------------------------------------------------------------------

func TestExtractExaContent_single(t *testing.T) {
	wt := &WebTool{}
	resp := exaResponse{
		Result: &exaResult{
			Content: []exaContent{
				{Type: "text", Text: "Title: Go 1.23 released\nURL: https://go.dev/blog/go1.23\nHighlights: range over func, iterators"},
			},
		},
	}
	r := wt.extractExaContent(resp)
	if !r.Success {
		t.Fatalf("expected success, got error: %s", r.Error)
	}
	if !strings.Contains(r.Content, "Go 1.23") {
		t.Errorf("content missing expected text: %s", r.Content)
	}
}

func TestExtractExaContent_multiple(t *testing.T) {
	wt := &WebTool{}
	resp := exaResponse{
		Result: &exaResult{
			Content: []exaContent{
				{Type: "text", Text: "First result"},
				{Type: "text", Text: "Second result"},
				{Type: "text", Text: "Third result"},
			},
		},
	}
	r := wt.extractExaContent(resp)
	if !r.Success {
		t.Fatalf("expected success, got error: %s", r.Error)
	}
	if !strings.Contains(r.Content, "---") {
		t.Errorf("expected separator between results, got: %s", r.Content)
	}
}

func TestExtractExaContent_empty(t *testing.T) {
	wt := &WebTool{}
	tests := []struct {
		name string
		resp exaResponse
	}{
		{"nil result", exaResponse{Result: nil}},
		{"empty content", exaResponse{Result: &exaResult{Content: []exaContent{}}}},
		{"all non-text", exaResponse{Result: &exaResult{Content: []exaContent{{Type: "image", Text: ""}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := wt.extractExaContent(tt.resp)
			if r.Success {
				t.Error("expected failure for empty result")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseExaSSE — test SSE parsing with mock data
// ---------------------------------------------------------------------------

func TestParseExaSSE_basic(t *testing.T) {
	wt := &WebTool{}
	sseData := `data: {"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Hello world"}]}}

data: [DONE]
`
	r := wt.parseExaSSE(context.Background(), strings.NewReader(sseData))
	if !r.Success {
		t.Fatalf("expected success, got error: %s", r.Error)
	}
	if !strings.Contains(r.Content, "Hello world") {
		t.Errorf("expected 'Hello world', got: %s", r.Content)
	}
}

func TestParseExaSSE_multipleData(t *testing.T) {
	wt := &WebTool{}
	// Last data event with result should win
	sseData := `data: {"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"First"}]}}

data: {"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"Second"}]}}

data: [DONE]
`
	r := wt.parseExaSSE(context.Background(), strings.NewReader(sseData))
	if !r.Success {
		t.Fatalf("expected success, got error: %s", r.Error)
	}
	if !strings.Contains(r.Content, "Second") {
		t.Errorf("expected 'Second', got: %s", r.Content)
	}
	if strings.Contains(r.Content, "First") {
		t.Error("should not contain first result — last wins")
	}
}

func TestParseExaSSE_error(t *testing.T) {
	wt := &WebTool{}
	sseData := `data: {"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error"}}

data: [DONE]
`
	r := wt.parseExaSSE(context.Background(), strings.NewReader(sseData))
	if r.Success {
		t.Fatal("expected failure on error SSE")
	}
	if !strings.Contains(r.Error, "Internal error") {
		t.Errorf("expected 'Internal error', got: %s", r.Error)
	}
}

func TestParseExaSSE_noResult(t *testing.T) {
	wt := &WebTool{}
	sseData := "data: [DONE]\n"
	r := wt.parseExaSSE(context.Background(), strings.NewReader(sseData))
	if r.Success {
		t.Fatal("expected failure on no result")
	}
}

// ---------------------------------------------------------------------------
// resolveURL
// ---------------------------------------------------------------------------

func TestResolveURL(t *testing.T) {
	tests := []struct {
		base, ref, want string
	}{
		{"https://example.com/page", "https://other.com/doc", "https://other.com/doc"},
		{"https://example.com/page", "/doc", "https://example.com/doc"},
		{"https://example.com/a/b/", "../c", "https://example.com/a/c"},
		{"https://example.com/a/b/page", "c", "https://example.com/a/b/c"},
		{"https://example.com/page", "", "https://example.com/page"},
	}
	for _, tt := range tests {
		got := resolveURL(tt.base, tt.ref)
		if got != tt.want {
			t.Errorf("resolveURL(%q, %q) = %q, want %q", tt.base, tt.ref, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Run — input validation and dispatch
// ---------------------------------------------------------------------------

func TestWebRun_missingWhat(t *testing.T) {
	wt := &WebTool{}
	r := wt.Run(context.Background(), map[string]any{"method": "search"})
	if r.Success {
		t.Error("expected failure when 'what' is missing")
	}
}

func TestWebRun_invalidMethod(t *testing.T) {
	wt := &WebTool{}
	r := wt.Run(context.Background(), map[string]any{"method": "invalid", "what": "test"})
	if r.Success {
		t.Error("expected failure for invalid method")
	}
	if !strings.Contains(r.Error, "must be one of") {
		t.Errorf("error should guide user, got: %s", r.Error)
	}
}

// ---------------------------------------------------------------------------
// webLookup — test the error-reporting output format
// ---------------------------------------------------------------------------

// TestLookup_formatErrors verifies that webLookup includes error details
// in its output when backends fail, rather than silently returning nothing.
func TestLookup_formatErrors(t *testing.T) {
	wt := &WebTool{}

	// Force doGet to fail by using an invalid URL scheme.
	// webLookup will call doGet → http.NewRequest will fail → "⚠️ ... unavailable" message.
	r := wt.Run(context.Background(), map[string]any{"method": "lookup", "what": "://bad"})
	if !r.Success {
		t.Fatalf("webLookup should return success even with errors, got: %s", r.Error)
	}
	// Should contain error messages, not silent empty
	if !strings.Contains(r.Content, "⚠️") {
		t.Errorf("expected ⚠️ error indicators in output, got: %s", r.Content)
	}
	if !strings.Contains(r.Content, "unavailable") {
		t.Errorf("expected 'unavailable' in error output, got: %s", r.Content)
	}
}

// TestLookup_formatEmpty tests that when everything fails, it says so clearly.
func TestLookup_formatEmpty(t *testing.T) {
	wt := &WebTool{}
	r := wt.Run(context.Background(), map[string]any{"method": "lookup", "what": "://bad"})
	if !r.Success {
		t.Fatalf("webLookup should return success even when empty, got: %s", r.Error)
	}
	if r.Content == "" {
		t.Fatal("webLookup returned empty content")
	}
}

// ---------------------------------------------------------------------------
// webGet — test format selection
// ---------------------------------------------------------------------------

func TestWebGet_formatOriginal(t *testing.T) {
	wt := &WebTool{}

	// webGet with a bad URL should fail with an error message mentioning the HTTP/network error,
	// not a panic or empty response.
	r := wt.Run(context.Background(), map[string]any{
		"method": "get",
		"what":   "://bad",
		"format": "original",
	})
	if r.Success {
		t.Fatal("expected failure for bad URL")
	}
	if r.Error == "" {
		t.Fatal("expected non-empty error")
	}
}

func TestWebGet_defaultFormat(t *testing.T) {
	wt := &WebTool{}

	r := wt.Run(context.Background(), map[string]any{
		"method": "get",
		"what":   "://bad",
		// no format → should default to markdown
	})
	if r.Success {
		t.Fatal("expected failure for bad URL")
	}
	if r.Error == "" {
		t.Fatal("expected non-empty error")
	}
}

// ---------------------------------------------------------------------------
// Schema consistency
// ---------------------------------------------------------------------------

func TestWebSchema_present(t *testing.T) {
	schemas := GetToolsSchema()
	found := false
	for _, s := range schemas {
		fn, ok := s["function"].(map[string]any)
		if !ok {
			continue
		}
		if fn["name"] == "web" {
			found = true
			params, ok := fn["parameters"].(map[string]any)
			if !ok {
				t.Fatal("web schema missing parameters")
			}
			required, ok := params["required"].([]string)
			if !ok {
				t.Fatal("web schema missing required")
			}
			if !contains(required, "method") || !contains(required, "what") {
				t.Errorf("web schema should require 'method' and 'what', got %v", required)
			}
			props, ok := params["properties"].(map[string]any)
			if !ok {
				t.Fatal("web schema missing properties")
			}
			method, ok := props["method"].(map[string]any)
			if !ok {
				t.Fatal("web schema missing method property")
			}
			enum, ok := method["enum"].([]string)
			if !ok {
				t.Fatal("method missing enum")
			}
			if !contains(enum, "search") || !contains(enum, "lookup") || !contains(enum, "get") {
				t.Errorf("method enum should include search/lookup/get, got %v", enum)
			}

			// Verify format enum no longer has "json" (removed as redundant)
			format, ok := props["format"].(map[string]any)
			if ok {
				formatEnum, ok := format["enum"].([]string)
				if ok && contains(formatEnum, "json") {
					t.Error("format should not contain 'json' — it was removed as redundant with 'original'")
				}
			}
		}
	}
	if !found {
		t.Fatal("web tool not found in GetToolsSchema()")
	}
}

func TestWebRegistry_registered(t *testing.T) {
	tool, ok := lookupTool("web")
	if !ok {
		t.Fatal("web tool not registered in toolRegistry")
	}
	if tool.Name() != "web" {
		t.Errorf("expected name 'web', got %q", tool.Name())
	}
}

// ---------------------------------------------------------------------------
// Thread safety — concurrent client init
// ---------------------------------------------------------------------------

func TestWebTool_threadSafeInit(t *testing.T) {
	wt := &WebTool{}
	var wg sync.WaitGroup
	clients := make([]tls_client.HttpClient, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c, err := wt.getClient()
			if err != nil {
				t.Errorf("getClient failed: %v", err)
				return
			}
			clients[idx] = c
		}(i)
	}
	wg.Wait()

	// All goroutines should get the same instance
	for i := 1; i < len(clients); i++ {
		// Compare pointers via formatting — we just check they're not nil
		if clients[i] == nil {
			t.Fatalf("client[%d] is nil", i)
		}
	}

	// Verify the client works by checking it can make a request
	c, err := wt.getClient()
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	if c == nil {
		t.Fatal("client is nil")
	}

	req, err := http.NewRequest("GET", "https://example.com", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("User-Agent", "test")
	resp, err := c.Do(req)
	if err != nil {
		// Network might not be available in CI, skip
		t.Skip("skipping: network unavailable:", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
