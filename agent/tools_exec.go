package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// planRequiredError is the message returned to the LLM when it attempts
// to use non-todo tools without first creating a plan. The message is
// deliberately actionable: it tells the model exactly what to do next.
const planRequiredError = "Error: You must create a plan using the todo tool before using other tools. Call todo(action=\"add\", content=\"...\") or todo(action=\"add_batch\", items=[...]) to outline your approach, then proceed with the actual work."

// enforcePlanGuard checks whether a plan (todo items) exists before
// allowing non-todo tool calls. When cfg.HasTodos is set and returns
// false, any tool call whose name is not "todo" is replaced with an
// error result in the returned results slice. Todo calls are collected
// for normal execution. Returns:
//   - toExecute: the todo tool calls that still need to run (nil if all blocked)
//   - origIdx:   the original indices in toolCalls for each entry in toExecute
//   - results:   the full results array with errors pre-filled for blocked calls
//
// When the guard is not active (nil callback or plan exists), returns
// (toolCalls, sequential indices, nil) so the caller falls through to
// the normal execution path.
func enforcePlanGuard(cfg Config, toolCalls []stream.ToolCall) ([]stream.ToolCall, []int, []string) {
	if cfg.HasTodos == nil || cfg.HasTodos() {
		return toolCalls, nil, nil
	}

	results := make([]string, len(toolCalls))
	var toExecute []stream.ToolCall
	var origIdx []int

	for i, tc := range toolCalls {
		if tc.Name == "todo" {
			toExecute = append(toExecute, tc)
			origIdx = append(origIdx, i)
		} else {
			results[i] = planRequiredError
		}
	}

	return toExecute, origIdx, results
}

func executeTools(ctx context.Context, runner ToolRunner, toolCalls []stream.ToolCall) []string {
	results := make([]string, len(toolCalls))

	// Group indices by tool name. Calls within the same group share a
	// MaxParallel cap; groups run independently of each other so two
	// different tools can still execute concurrently.
	type group struct {
		name  string
		idxs  []int
		limit int
	}
	var groups []*group
	groupByName := make(map[string]*group, len(toolCalls))
	for i, tc := range toolCalls {
		g, ok := groupByName[tc.Name]
		if !ok {
			g = &group{
				name:  tc.Name,
				limit: tools.MaxParallelFor(tc.Name),
				idxs:  make([]int, 0, 1),
			}
			groupByName[tc.Name] = g
			groups = append(groups, g)
		}
		g.idxs = append(g.idxs, i)
	}

	var wg sync.WaitGroup
	for _, g := range groups {
		switch {
		case g.limit <= 0:
			// Unbounded: every call of this tool runs in its own goroutine.
			for _, idx := range g.idxs {
				wg.Add(1)
				go func(i int, tc stream.ToolCall) {
					defer wg.Done()
					results[i] = runToolCall(ctx, runner, tc, i)
				}(idx, toolCalls[idx])
			}
		case g.limit == 1:
			// Serial: all calls of this tool in this batch run in order inside
			// a single goroutine. Per-tool Run locks its own state, but races
			// between CONCURRENT Run invocations in the same batch are not
			// caught by any per-Run mutex — the dispatcher must serialise.
			wg.Add(1)
			go func(idxs []int) {
				defer wg.Done()
				for _, i := range idxs {
					results[i] = runToolCall(ctx, runner, toolCalls[i], i)
				}
			}(g.idxs)
		default:
			// limit > 1 reserved for future use; today, fall back to per-call.
			sem := make(chan struct{}, g.limit)
			for _, idx := range g.idxs {
				wg.Add(1)
				go func(i int, tc stream.ToolCall) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					results[i] = runToolCall(ctx, runner, tc, i)
				}(idx, toolCalls[idx])
			}
		}
	}
	wg.Wait()
	return results
}

// runToolCall decodes args, applies the per-tool timeout, and writes the
// result (or an error string) back to results[idx]. Extracted from
// executeTools so the serial and parallel branches share one code path.
func runToolCall(ctx context.Context, runner ToolRunner, call stream.ToolCall, idx int) string {
	var args map[string]any
	if call.Arguments != "" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return "Error: invalid arguments: " + err.Error()
		}
	}
	if args == nil {
		args = make(map[string]any)
	}

	var toolTimeout time.Duration
	switch call.Name {
	case "read", "write":
		toolTimeout = 30 * time.Second
	case "bash":
		toolTimeout = 120 * time.Second
		if to, ok := args["timeout"]; ok {
			switch v := to.(type) {
			case float64:
				toolTimeout = time.Duration(v) * time.Second
			case int:
				toolTimeout = time.Duration(v) * time.Second
			}
		}
	case "subagent":
		toolTimeout = 0
	case "wait":
		// wait manages its own duration (clamped to MaxWaitSeconds) and is
		// context-aware internally; an external timeout here would cut it
		// off before it reports back, defeating the point of the tool.
		toolTimeout = 0
	default:
		toolTimeout = 60 * time.Second
	}

	toolCtx := ctx
	var cancel context.CancelFunc
	if toolTimeout > 0 {
		toolCtx, cancel = context.WithTimeout(ctx, toolTimeout)
		defer cancel()
	}

	if call.Name == "bash" || call.Name == "subagent" {
		toolCtx = context.WithValue(toolCtx, stream.ToolIdxCtxKey{}, idx)
	}

	body, err := runner.Run(toolCtx, call.Name, args)
	if err != nil {
		if toolCtx.Err() == context.DeadlineExceeded {
			return fmt.Sprintf("Error: %s tool timed out after %v", call.Name, toolTimeout)
		}
		return "Error: " + err.Error()
	}
	return body
}
