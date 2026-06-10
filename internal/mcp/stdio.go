package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// Ensure we use context
var _ = context.Background

// StdioClient communicates with an MCP server over stdio.
type StdioClient struct {
	name    string
	command string
	args    []string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	mu               sync.Mutex
	nextID           int
	pending          map[int]chan *Response
	samplingHandler  SamplingHandler
	elicitationHandler ElicitationHandler
}

// NewStdioClient creates a new stdio-based MCP client.
func NewStdioClient(name, command string, args []string) *StdioClient {
	return &StdioClient{
		name:    name,
		command: command,
		args:    args,
		nextID:  1,
		pending: make(map[int]chan *Response),
	}
}

// Name returns the server name.
func (c *StdioClient) Name() string {
	return c.name
}

// Initialize performs the MCP handshake over stdio.
func (c *StdioClient) Initialize(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Start the process
	c.cmd = exec.CommandContext(ctx, c.command, c.args...)
	c.cmd.Stderr = nil // Discard stderr for now

	var err error
	c.stdin, err = c.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}

	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}
	c.stdout = bufio.NewReader(stdout)

	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	// Start response reader goroutine
	go c.readLoop()

	// Send initialize request
	initReq := InitializeRequest(c.nextID)
	c.nextID++

	resp, err := c.sendRequest(ctx, initReq)
	if err != nil {
		c.Close()
		return fmt.Errorf("initialize: %w", err)
	}

	if resp.Error != nil {
		c.Close()
		return fmt.Errorf("initialize error: %s", resp.Error.Message)
	}

	// Send initialized notification
	notif := InitializedNotification()
	if err := c.sendNotification(notif); err != nil {
		c.Close()
		return fmt.Errorf("sending initialized: %w", err)
	}

	return nil
}

// ListTools returns available tools from the server.
func (c *StdioClient) ListTools(ctx context.Context) ([]Tool, error) {
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
func (c *StdioClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*CallToolResult, error) {
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
func (c *StdioClient) SetSamplingHandler(handler SamplingHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samplingHandler = handler
}

// SetElicitationHandler sets the handler for elicitation/create requests.
func (c *StdioClient) SetElicitationHandler(handler ElicitationHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.elicitationHandler = handler
}

// Close shuts down the client.
func (c *StdioClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Close stdin to signal the process to exit
	if c.stdin != nil {
		c.stdin.Close()
	}

	// Wait for process to exit (with timeout)
	if c.cmd != nil && c.cmd.Process != nil {
		done := make(chan error, 1)
		go func() {
			done <- c.cmd.Wait()
		}()

		select {
		case <-done:
			// Process exited
		default:
			// Process still running, force kill
			c.cmd.Process.Kill()
		}
	}

	// Close all pending channels
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}

	return nil
}

// sendRequest sends a JSON-RPC request and waits for the response.
func (c *StdioClient) sendRequest(ctx context.Context, req Request) (*Response, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	// Create pending channel
	ch := make(chan *Response, 1)
	c.mu.Lock()
	c.pending[req.ID] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, req.ID)
		c.mu.Unlock()
	}()

	// Send request
	if _, err := c.stdin.Write(data); err != nil {
		return nil, fmt.Errorf("writing request: %w", err)
	}

	// Wait for response
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// sendNotification sends a JSON-RPC notification (no response expected).
func (c *StdioClient) sendNotification(req Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if _, err := c.stdin.Write(data); err != nil {
		return fmt.Errorf("writing notification: %w", err)
	}

	return nil
}

// rawMessage is used to detect if a message is a request or response.
type rawMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	ID      int             `json:"id"`
	Params  json.RawMessage `json:"params"`
}

// readLoop reads responses from stdout and dispatches them.
func (c *StdioClient) readLoop() {
	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			// EOF or error, stop reading
			return
		}

		// First, try to detect if this is a request from the server
		var raw rawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			// Skip malformed messages
			continue
		}

		// Server requests have a method and typically ID=0 (or no ID)
		if raw.Method != "" && (raw.ID == 0 || raw.Method == "sampling/createMessage" || raw.Method == "elicitation/create") {
			var req Request
			if err := json.Unmarshal(line, &req); err == nil {
				c.handleServerRequest(req)
				continue
			}
		}

		// Otherwise, treat as a response
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}

		// Dispatch to pending request
		c.mu.Lock()
		if ch, ok := c.pending[resp.ID]; ok {
			ch <- &resp
		}
		c.mu.Unlock()
	}
}

// handleServerRequest processes requests from the server (sampling, elicitation).
func (c *StdioClient) handleServerRequest(req Request) {
	// Marshal params to raw JSON
	paramsJSON, err := json.Marshal(req.Params)
	if err != nil {
		return
	}

	switch req.Method {
	case "sampling/createMessage":
		c.mu.Lock()
		handler := c.samplingHandler
		c.mu.Unlock()

		if handler == nil {
			c.sendErrorResponse(req.ID, -32601, "sampling not supported")
			return
		}

		var samplingReq SamplingRequest
		if err := json.Unmarshal(paramsJSON, &samplingReq); err != nil {
			c.sendErrorResponse(req.ID, -32700, "parse error")
			return
		}

		// Call handler (blocking)
		ctx := context.Background()
		result, err := handler(ctx, c.name, &samplingReq)
		if err != nil {
			c.sendErrorResponse(req.ID, -32603, err.Error())
			return
		}

		c.sendResultResponse(req.ID, result)

	case "elicitation/create":
		c.mu.Lock()
		handler := c.elicitationHandler
		c.mu.Unlock()

		if handler == nil {
			c.sendErrorResponse(req.ID, -32601, "elicitation not supported")
			return
		}

		var elicitationReq ElicitationRequest
		if err := json.Unmarshal(paramsJSON, &elicitationReq); err != nil {
			c.sendErrorResponse(req.ID, -32700, "parse error")
			return
		}

		// Call handler (blocking)
		ctx := context.Background()
		result, err := handler(ctx, c.name, &elicitationReq)
		if err != nil {
			c.sendErrorResponse(req.ID, -32603, err.Error())
			return
		}

		c.sendResultResponse(req.ID, result)
	}
}

// sendErrorResponse sends a JSON-RPC error response.
func (c *StdioClient) sendErrorResponse(id int, code int, message string) {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: message},
	}
	c.sendResponse(resp)
}

// sendResultResponse sends a JSON-RPC success response.
func (c *StdioClient) sendResultResponse(id int, result interface{}) {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		c.sendErrorResponse(id, -32603, "failed to marshal result")
		return
	}

	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  resultJSON,
	}
	c.sendResponse(resp)
}

// sendResponse sends a JSON-RPC response.
func (c *StdioClient) sendResponse(resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	data = append(data, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdin != nil {
		c.stdin.Write(data)
	}
}
