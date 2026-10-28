// Package mcpserver is a Model Context Protocol (MCP) server over stdio. It
// exposes read-only tools that call a daemon.Backend. The protocol types come
// from internal/mcp.
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/crazy-goat/tyci-agent/internal/daemon"
	"github.com/crazy-goat/tyci-agent/internal/mcp"
)

// JSON-RPC 2.0 error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeNoMethod       = -32601
	codeInvalidParams  = -32602
)

// defaultProtocolVersion is the MCP version that the server answers when the
// client sends none.
const defaultProtocolVersion = "2024-11-05"

// maxLine is the longest message line the server reads.
const maxLine = 1 << 20

// Server answers MCP requests. Out carries the protocol only; logs go to Log.
type Server struct {
	Backend daemon.Backend
	Version string
	// Log receives the log lines. It must not be the protocol output.
	Log io.Writer
}

// request is a JSON-RPC 2.0 message from the client. The id stays raw, so a
// string id comes back unchanged.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcp.RPCError   `json:"error,omitempty"`
}

// Serve reads newline-delimited JSON-RPC messages from in and writes the
// answers to out, one per line, until in ends.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		resp, ok := s.handle(ctx, line)
		if !ok {
			continue // a notification gets no answer
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		_, _ = fmt.Fprintf(s.Log, "tyci mcp: "+format+"\n", args...)
	}
}

func (s *Server) handle(ctx context.Context, line []byte) (response, bool) {
	resp := response{JSONRPC: "2.0", ID: json.RawMessage("null")}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.logf("parse error: %v", err)
		resp.Error = &mcp.RPCError{Code: codeParse, Message: "parse error: " + err.Error()}
		return resp, true
	}
	if len(req.ID) > 0 {
		resp.ID = req.ID
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		s.logf("invalid request: %s", line)
		resp.Error = &mcp.RPCError{Code: codeInvalidRequest, Message: "invalid request: jsonrpc must be \"2.0\" and method must be set"}
		return resp, true
	}
	if len(req.ID) == 0 {
		return resp, false // notifications/initialized and other notifications
	}
	var err *mcp.RPCError
	switch req.Method {
	case "initialize":
		resp.Result = initialize(req.Params, s.Version)
	case "ping":
		resp.Result = struct{}{}
	case "tools/list":
		resp.Result = mcp.ToolListResult{Tools: toolList()}
	case "tools/call":
		resp.Result, err = s.callTool(ctx, req.Params)
	default:
		err = &mcp.RPCError{Code: codeNoMethod, Message: "method not found: " + req.Method}
	}
	if err != nil {
		s.logf("%s: %s", req.Method, err.Message)
		resp.Result, resp.Error = nil, err
	}
	return resp, true
}

func initialize(params json.RawMessage, version string) mcp.InitializeResult {
	var p mcp.InitializeParams
	_ = json.Unmarshal(params, &p) // a bad params value gets the default version
	v := p.ProtocolVersion
	if v == "" {
		v = defaultProtocolVersion
	}
	return mcp.InitializeResult{
		ProtocolVersion: v,
		Capabilities:    mcp.ServerCaps{Tools: map[string]any{}},
		ServerInfo:      mcp.ServerInfo{Name: "tyci", Version: version},
	}
}
