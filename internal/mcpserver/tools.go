package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/crazy-goat/tyci-agent/internal/mcp"
)

// The tools are read-only. Each one calls one Backend method.
const (
	toolRunsList  = "tyci_runs_list"
	toolRunStatus = "tyci_run_status"
)

func toolList() []mcp.Tool {
	return []mcp.Tool{
		{
			Name: toolRunsList,
			Description: "List the running and paused tyci workflow runs, newest first. With a running tyci daemon, " +
				"the runs of the daemon repositories. Without a daemon, the runs of the repository of the server " +
				"working directory. Read-only. Returns JSON.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name: toolRunStatus,
			Description: "Show the saved state of one tyci workflow run of any repository: status, current state, " +
				"visits, ask, pull request and the state file. Read-only. Returns JSON.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"run":{"type":"string","description":"run id, for example from tyci_runs_list"}},"required":["run"]}`),
		},
	}
}

// callTool runs a tools/call request. An unknown tool or bad arguments are a
// protocol error. A Backend error is a tool result with isError set.
func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, *mcp.RPCError) {
	var p mcp.CallToolParams
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, &mcp.RPCError{Code: codeInvalidParams, Message: "tools/call needs params {\"name\": \"<tool>\", \"arguments\": {...}}"}
	}
	var (
		res any
		err error
	)
	switch p.Name {
	case toolRunsList:
		res, err = s.Backend.RunsList(ctx)
	case toolRunStatus:
		var a struct {
			Run string `json:"run"`
		}
		if json.Unmarshal(p.Arguments, &a) != nil || a.Run == "" {
			return nil, &mcp.RPCError{Code: codeInvalidParams, Message: toolRunStatus + " needs arguments {\"run\": \"<run id>\"}"}
		}
		res, err = s.Backend.RunsGet(ctx, a.Run)
	default:
		return nil, &mcp.RPCError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name + " (tools: " + toolRunsList + ", " + toolRunStatus + ")"}
	}
	if err != nil {
		s.logf("%s: %v", p.Name, err)
		return textResult(err.Error(), true), nil
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	return textResult(string(b), false), nil
}

func textResult(text string, isError bool) mcp.CallToolResult {
	return mcp.CallToolResult{Content: []mcp.ContentBlock{{Type: "text", Text: text}}, IsError: isError}
}
