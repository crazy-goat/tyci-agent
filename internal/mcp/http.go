package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HTTPClient communicates with an MCP server over HTTP using the
// Streamable HTTP transport (POST + SSE).
type HTTPClient struct {
	name string
	url  string
	auth string // "bearer" or ""

	mu               sync.Mutex
	nextID           int
	client           *http.Client
	headers          http.Header
	samplingHandler  SamplingHandler
	elicitationHandler ElicitationHandler
}

// NewHTTPClient creates a new HTTP-based MCP client.
func NewHTTPClient(name, url, auth string) *HTTPClient {
	return &HTTPClient{
		name:    name,
		url:     strings.TrimRight(url, "/"),
		auth:    auth,
		nextID:  1,
		client:  &http.Client{Timeout: 30 * time.Second},
		headers: http.Header{"Content-Type": []string{"application/json"}},
	}
}

// Name returns the server name.
func (c *HTTPClient) Name() string {
	return c.name
}

// SetAuthToken sets the bearer token for authentication.
func (c *HTTPClient) SetAuthToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.auth == "bearer" && token != "" {
		c.headers.Set("Authorization", "Bearer "+token)
	}
}

// Initialize performs the MCP handshake over HTTP.
func (c *HTTPClient) Initialize(ctx context.Context) error {
	req := InitializeRequest(c.nextID)
	c.mu.Lock()
	c.nextID++
	c.mu.Unlock()

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	if resp.Error != nil {
		return fmt.Errorf("initialize error: %s", resp.Error.Message)
	}

	// Send initialized notification
	notif := InitializedNotification()
	notif.ID = 0 // notifications have no ID
	if err := c.sendNotification(ctx, notif); err != nil {
		return fmt.Errorf("sending initialized: %w", err)
	}

	return nil
}

// ListTools returns available tools from the server.
func (c *HTTPClient) ListTools(ctx context.Context) ([]Tool, error) {
	c.mu.Lock()
	req := ToolsListRequest(c.nextID)
	c.nextID++
	c.mu.Unlock()

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list error: %s", resp.Error.Message)
	}

	var result ToolListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parsing tools/list result: %w", err)
	}

	return result.Tools, nil
}

// CallTool invokes a tool on the server.
func (c *HTTPClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*CallToolResult, error) {
	c.mu.Lock()
	req := ToolsCallRequest(c.nextID, CallToolParams{
		Name:      name,
		Arguments: arguments,
	})
	c.nextID++
	c.mu.Unlock()

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tools/call: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("tools/call error: %s", resp.Error.Message)
	}

	var result CallToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parsing tools/call result: %w", err)
	}

	return &result, nil
}

// SetSamplingHandler sets the handler for sampling/createMessage requests.
func (c *HTTPClient) SetSamplingHandler(handler SamplingHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samplingHandler = handler
}

// SetElicitationHandler sets the handler for elicitation/create requests.
func (c *HTTPClient) SetElicitationHandler(handler ElicitationHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.elicitationHandler = handler
}

// Close shuts down the client (no-op for HTTP).
func (c *HTTPClient) Close() error {
	return nil
}

// sendRequest sends a JSON-RPC request over HTTP and returns the response.
// It first tries direct JSON response, then falls back to SSE parsing.
func (c *HTTPClient) sendRequest(ctx context.Context, req Request) (*Response, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	// Copy headers (including auth)
	c.mu.Lock()
	for k, v := range c.headers {
		for _, val := range v {
			httpReq.Header.Add(k, val)
		}
	}
	c.mu.Unlock()

	// Accept both JSON and SSE
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Check content type to determine response format
	contentType := resp.Header.Get("Content-Type")

	if strings.HasPrefix(contentType, "text/event-stream") {
		return c.parseSSEResponse(resp.Body, req.ID)
	}

	// Direct JSON response
	var jsonResp Response
	if err := json.NewDecoder(resp.Body).Decode(&jsonResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &jsonResp, nil
}

// sendNotification sends a JSON-RPC notification over HTTP (no response expected).
func (c *HTTPClient) sendNotification(ctx context.Context, req Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshaling notification: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	c.mu.Lock()
	for k, v := range c.headers {
		for _, val := range v {
			httpReq.Header.Add(k, val)
		}
	}
	c.mu.Unlock()

	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Drain the body to allow connection reuse
	io.Copy(io.Discard, resp.Body)

	return nil
}

// parseSSEResponse reads SSE events from the response body and returns
// the first Response message with the matching ID.
func (c *HTTPClient) parseSSEResponse(body io.Reader, expectedID int) (*Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)

	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: "data: <payload>"
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			dataLines = append(dataLines, payload)
			continue
		}

		// Empty line = end of event
		if line == "" && len(dataLines) > 0 {
			// Try to parse concatenated JSON objects or single JSON
			fullData := strings.Join(dataLines, "")
			resp, ok := c.tryParseResponse(fullData, expectedID)
			if ok {
				return resp, nil
			}
			dataLines = dataLines[:0]
			continue
		}
	}

	// Handle remaining data after loop ends
	if len(dataLines) > 0 {
		fullData := strings.Join(dataLines, "")
		resp, ok := c.tryParseResponse(fullData, expectedID)
		if ok {
			return resp, nil
		}
	}

	return nil, fmt.Errorf("no valid response found in SSE stream")
}

// tryParseResponse tries to parse JSON data as a Response with the expected ID.
// It handles both single JSON objects and arrays.
func (c *HTTPClient) tryParseResponse(data string, expectedID int) (*Response, bool) {
	data = strings.TrimSpace(data)
	if data == "" {
		return nil, false
	}

	// Try single response
	var resp Response
	if err := json.Unmarshal([]byte(data), &resp); err == nil && resp.ID == expectedID {
		return &resp, true
	}

	// Try array of responses
	var responses []Response
	if err := json.Unmarshal([]byte(data), &responses); err == nil {
		for _, r := range responses {
			if r.ID == expectedID {
				return &r, true
			}
		}
	}

	// Try concatenated JSON objects (split by top-level boundaries)
	if idx := findTopLevelEnd(data); idx > 0 && idx < len(data) {
		first := data[:idx]
		rest := data[idx:]

		var resp Response
		if err := json.Unmarshal([]byte(first), &resp); err == nil && resp.ID == expectedID {
			return &resp, true
		}

		// Try rest
		if resp, ok := c.tryParseResponse(rest, expectedID); ok {
			return resp, true
		}
	}

	return nil, false
}

// findTopLevelEnd finds the index of the first top-level JSON object end.
func findTopLevelEnd(data string) int {
	depth := 0
	inString := false
	escape := false

	for i, ch := range data {
		if escape {
			escape = false
			continue
		}
		if ch == '\\' && inString {
			escape = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}

		switch ch {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}
