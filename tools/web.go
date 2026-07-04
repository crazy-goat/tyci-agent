package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"

	md "github.com/JohannesKaufmann/html-to-markdown/v2"
	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// WebTool implements a unified web tool with three methods:
//   - search: real-time web search via Exa AI (for current events, docs, anything post-training)
//   - lookup: fast encyclopedic lookup via DuckDuckGo + Wikipedia (enough for most knowledge questions)
//   - get: fetch a URL with browser TLS impersonation, returns Markdown by default
type WebTool struct {
	client tls_client.HttpClient
	once   sync.Once
}

// Name returns the tool name.
func (t *WebTool) Name() string {
	return "web"
}

// getClient returns a shared TLS client instance (lazy init).
func (t *WebTool) getClient() (tls_client.HttpClient, error) {
	var initErr error
	t.once.Do(func() {
		client, err := tls_client.NewHttpClient(
			nil, // logger
			tls_client.WithClientProfile(profiles.Chrome_133),
			tls_client.WithTimeoutSeconds(30),
			tls_client.WithInsecureSkipVerify(),
			tls_client.WithNotFollowRedirects(),
		)
		if err != nil {
			initErr = fmt.Errorf("create tls client: %w", err)
			return
		}
		t.client = client
	})
	return t.client, initErr
}

// Run dispatches the web tool based on the method parameter.
func (t *WebTool) Run(ctx context.Context, input map[string]any) ToolResult {
	method, _ := input["method"].(string)
	what, ok := input["what"].(string)
	if !ok || what == "" {
		return ToolResult{Type: "result", Success: false, Error: "what (query/term/URL) is required"}
	}

	switch method {
	case "search":
		numResults := intParam(input, "numResults", 8)
		searchType, _ := input["type"].(string)
		if searchType == "" {
			searchType = "auto"
		}
		return t.webSearch(ctx, what, numResults, searchType)

	case "lookup":
		return t.webLookup(ctx, what)

	case "get":
		format, _ := input["format"].(string)
		if format == "" {
			format = "markdown"
		}
		return t.webGet(ctx, what, format)

	default:
		return ToolResult{Type: "result", Success: false,
			Error: "method must be one of: search (real-time web), lookup (encyclopedic facts), get (fetch URL)"}
	}
}

// ---------------------------------------------------------------------------
// search — Exa AI MCP endpoint
// ---------------------------------------------------------------------------

type exaRequest struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      int       `json:"id"`
	Method  string    `json:"method"`
	Params  exaParams `json:"params"`
}

type exaParams struct {
	Name      string       `json:"name"`
	Arguments exaArguments `json:"arguments"`
}

type exaArguments struct {
	Query      string `json:"query"`
	Type       string `json:"type"`
	NumResults int    `json:"numResults"`
	Livecrawl  string `json:"livecrawl"`
}

type exaResponse struct {
	JSONRPC string     `json:"jsonrpc"`
	ID      int        `json:"id"`
	Result  *exaResult `json:"result,omitempty"`
	Error   *exaError  `json:"error,omitempty"`
}

type exaResult struct {
	Content []exaContent `json:"content"`
}

type exaContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type exaError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (t *WebTool) webSearch(ctx context.Context, query string, numResults int, searchType string) ToolResult {
	client, err := t.getClient()
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	if numResults < 1 {
		numResults = 8
	}
	if numResults > 25 {
		numResults = 25
	}

	req := exaRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: exaParams{
			Name: "web_search_exa",
			Arguments: exaArguments{
				Query:      query,
				Type:       searchType,
				NumResults: numResults,
				Livecrawl:  "fallback",
			},
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("marshal request: %v", err)}
	}

	httpReq, err := http.NewRequest(http.MethodPost, "https://mcp.exa.ai/mcp", bytes.NewReader(body))
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("create request: %v", err)}
	}
	httpReq = httpReq.WithContext(ctx)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")

	// If EXA_API_KEY is set, append it as query param
	if apiKey := os.Getenv("EXA_API_KEY"); apiKey != "" {
		q := httpReq.URL.Query()
		q.Set("exaApiKey", apiKey)
		httpReq.URL.RawQuery = q.Encode()
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("exa request failed: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ToolResult{Type: "result", Success: false,
			Error: fmt.Sprintf("exa returned HTTP %d: %s", resp.StatusCode, string(respBody))}
	}

	// Parse SSE response
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		return t.parseExaSSE(ctx, resp.Body)
	}

	// Plain JSON response
	var result exaResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("parse exa response: %v", err)}
	}

	if result.Error != nil {
		return ToolResult{Type: "result", Success: false,
			Error: fmt.Sprintf("exa error [%d]: %s", result.Error.Code, result.Error.Message)}
	}

	return t.extractExaContent(result)
}

func (t *WebTool) parseExaSSE(ctx context.Context, r io.Reader) ToolResult {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 256*1024)

	var lastResult exaResponse
	for scanner.Scan() {
		line := scanner.Text()

		if ctx.Err() != nil {
			return ToolResult{Type: "result", Success: false, Error: "search cancelled"}
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "" || data == "[DONE]" {
			continue
		}

		var resp exaResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			continue
		}

		if resp.Error != nil {
			return ToolResult{Type: "result", Success: false,
				Error: fmt.Sprintf("exa error [%d]: %s", resp.Error.Code, resp.Error.Message)}
		}

		if resp.Result != nil {
			lastResult = resp
		}
	}

	if err := scanner.Err(); err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("sse read error: %v", err)}
	}

	if lastResult.Result == nil {
		return ToolResult{Type: "result", Success: false, Error: "no search results from exa"}
	}

	return t.extractExaContent(lastResult)
}

func (t *WebTool) extractExaContent(resp exaResponse) ToolResult {
	if resp.Result == nil || len(resp.Result.Content) == 0 {
		return ToolResult{Type: "result", Success: false, Error: "no search results"}
	}

	var texts []string
	for _, c := range resp.Result.Content {
		if c.Type == "text" && c.Text != "" {
			texts = append(texts, c.Text)
		}
	}

	if len(texts) == 0 {
		return ToolResult{Type: "result", Success: false, Error: "no search results"}
	}

	return ToolResult{Type: "result", Success: true, Content: strings.Join(texts, "\n\n---\n\n")}
}

// ---------------------------------------------------------------------------
// lookup — DuckDuckGo + Wikipedia fallback chain
// ---------------------------------------------------------------------------

type ddgResponse struct {
	AbstractText   string       `json:"AbstractText"`
	AbstractSource string       `json:"AbstractSource"`
	Answer         string       `json:"Answer"`
	AnswerType     string       `json:"AnswerType"`
	RelatedTopics  []ddgRelated `json:"RelatedTopics"`
}

type ddgRelated struct {
	Text     string       `json:"Text"`
	FirstURL string       `json:"FirstURL"`
	Topics   []ddgRelated `json:"Topics,omitempty"`
}

type wikiSummary struct {
	Title   string `json:"title"`
	Extract string `json:"extract"`
	URL     string `json:"url"`
}

type wikiSearchResponse struct {
	Query struct {
		Search []struct {
			Title string `json:"title"`
		} `json:"search"`
	} `json:"query"`
}

func (t *WebTool) webLookup(ctx context.Context, term string) ToolResult {
	client, err := t.getClient()
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Lookup: %s\n\n", term))

	// Step 1: DuckDuckGo Instant Answer
	ddgURL := fmt.Sprintf("https://api.duckduckgo.com/?q=%s&format=json&no_html=1", url.QueryEscape(term))
	ddgResp, err := t.doGet(ctx, client, ddgURL)
	if err != nil {
		b.WriteString(fmt.Sprintf("⚠️ DuckDuckGo unavailable: %s\n\n", err))
	} else {
		var ddg ddgResponse
		if err := json.Unmarshal([]byte(ddgResp), &ddg); err != nil {
			b.WriteString(fmt.Sprintf("⚠️ DuckDuckGo response parse error: %s\n\n", err))
		} else {
			if ddg.AbstractText != "" {
				b.WriteString(fmt.Sprintf("**Abstract** (%s):\n%s\n\n", ddg.AbstractSource, ddg.AbstractText))
			}
			if ddg.Answer != "" {
				b.WriteString(fmt.Sprintf("**Answer**: %s\n\n", ddg.Answer))
			}
			for _, topic := range ddg.RelatedTopics {
				if topic.Text != "" {
					b.WriteString(fmt.Sprintf("- %s\n", topic.Text))
				}
				if len(topic.Topics) > 0 {
					for _, sub := range topic.Topics {
						if sub.Text != "" {
							b.WriteString(fmt.Sprintf("  - %s\n", sub.Text))
						}
					}
				}
			}
			if ddg.AbstractText == "" && ddg.Answer == "" && len(ddg.RelatedTopics) == 0 {
				b.WriteString("(no instant answer from DuckDuckGo)\n")
			}
			b.WriteString("\n")
		}
	}

	// Step 2: Wikipedia summary
	wikiURL := fmt.Sprintf("https://en.wikipedia.org/api/rest_v1/page/summary/%s", url.PathEscape(term))
	wikiResp, err := t.doGet(ctx, client, wikiURL)
	if err != nil {
		b.WriteString(fmt.Sprintf("⚠️ Wikipedia summary unavailable: %s\n\n", err))
	} else {
		var wiki wikiSummary
		if err := json.Unmarshal([]byte(wikiResp), &wiki); err != nil {
			b.WriteString(fmt.Sprintf("⚠️ Wikipedia response parse error: %s\n\n", err))
		} else if wiki.Extract != "" {
			b.WriteString(fmt.Sprintf("**Wikipedia**: %s\n%s\n\n", wiki.Title, wiki.Extract))
		} else {
			b.WriteString("(no summary found on Wikipedia)\n\n")
		}
	}

	// Step 3: Wikipedia search fallback (only if we got very little so far)
	if b.Len() < 100 {
		searchURL := fmt.Sprintf(
			"https://en.wikipedia.org/w/api.php?action=query&list=search&srsearch=%s&format=json&origin=*",
			url.QueryEscape(term))
		searchResp, err := t.doGet(ctx, client, searchURL)
		if err != nil {
			b.WriteString(fmt.Sprintf("⚠️ Wikipedia search unavailable: %s\n\n", err))
		} else {
			var ws wikiSearchResponse
			if err := json.Unmarshal([]byte(searchResp), &ws); err != nil {
				b.WriteString(fmt.Sprintf("⚠️ Wikipedia search parse error: %s\n\n", err))
			} else if len(ws.Query.Search) > 0 {
				b.WriteString("**Wikipedia search results**:\n")
				for i, s := range ws.Query.Search {
					if i >= 10 {
						break
					}
					b.WriteString(fmt.Sprintf("- %s\n", s.Title))
				}
				b.WriteString("\n")
			} else {
				b.WriteString("(no Wikipedia search results)\n\n")
			}
		}
	}

	result := strings.TrimSpace(b.String())
	if result == "" || strings.Count(result, "\n") <= 1 {
		return ToolResult{Type: "result", Success: true,
			Content: fmt.Sprintf("No information found for %q. All backends returned empty or failed.", term)}
	}

	return ToolResult{Type: "result", Success: true, Content: result}
}

// ---------------------------------------------------------------------------
// get — Direct HTTP with TLS impersonation
// ---------------------------------------------------------------------------

func (t *WebTool) webGet(ctx context.Context, rawURL, format string) ToolResult {
	client, err := t.getClient()
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	body, err := t.doGet(ctx, client, rawURL)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("fetch failed: %v", err)}
	}

	switch format {
	case "original":
		return ToolResult{Type: "result", Success: true, Content: body}

	case "markdown":
		fallthrough
	default:
		// Try HTML to Markdown conversion
		converted, err := md.ConvertString(body)
		if err == nil && converted != "" {
			return ToolResult{Type: "result", Success: true, Content: converted}
		}
		// If conversion fails or produces empty, return raw
		return ToolResult{Type: "result", Success: true, Content: body}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// doGet performs an HTTP GET with the shared TLS client, following redirects.
func (t *WebTool) doGet(ctx context.Context, client tls_client.HttpClient, rawURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req = req.WithContext(ctx)

	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,application/json;q=0.8,*/*;q=0.7")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	// Follow redirects manually up to 5 hops
	for i := 0; i < 5; i++ {
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			if loc == "" {
				break
			}
			resp.Body.Close()

			absLoc := resolveURL(rawURL, loc)
			req, err := http.NewRequest(http.MethodGet, absLoc, nil)
			if err != nil {
				return "", fmt.Errorf("redirect request: %w", err)
			}
			req = req.WithContext(ctx)
			req.Header = http.Header{
				"User-Agent":      {"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"},
				"Accept":          {"text/html,application/xhtml+xml,application/xml;q=0.9,application/json;q=0.8,*/*;q=0.7"},
				"Accept-Language": {"en-US,en;q=0.9"},
			}
			resp, err = client.Do(req)
			if err != nil {
				return "", fmt.Errorf("redirect follow: %w", err)
			}
			defer resp.Body.Close()
			continue
		}
		break
	}

	if resp.StatusCode != 200 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(limited)))
	}

	// Max 5MB response body
	limited, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	return string(limited), nil
}

// resolveURL resolves a relative URL against a base.
func resolveURL(base, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	baseU, err := url.Parse(base)
	if err != nil {
		return ref
	}
	return baseU.ResolveReference(u).String()
}
