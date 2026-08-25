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
const planRequiredError = "Error: create a plan first. Call todo(action=\"add_batch\", items=[{\"content\": \"...\"}, ...]) — one call for the whole plan — then get on with the work."

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

// executeTools runs a batch and returns each call's result, duration and
// structured failure status. The status is kept separately from result text:
// successful tools are allowed to print arbitrary text, including text that
// starts with "Error:" or "❌ exit code".
//
// The durations have to be measured here, around each call, because the
// display cannot work them out for itself: every ToolCallStart for a batch is
// emitted before the batch runs and every ToolCallEnd after it finishes, so a
// display timing from start to end would show the whole batch's wall-clock on
// every row — four tools taking 1ms, 1ms, 1ms and 4.3s all reported as 4.3s.
func executeTools(ctx context.Context, runner ToolRunner, toolCalls []stream.ToolCall) ([]string, []time.Duration, []bool) {
	results := make([]string, len(toolCalls))
	durations := make([]time.Duration, len(toolCalls))
	failed := make([]bool, len(toolCalls))

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
					results[i], durations[i], failed[i] = timeToolCall(ctx, runner, tc, i)
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
					results[i], durations[i], failed[i] = timeToolCall(ctx, runner, toolCalls[i], i)
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
					results[i], durations[i], failed[i] = timeToolCall(ctx, runner, tc, i)
				}(idx, toolCalls[idx])
			}
		}
	}
	wg.Wait()
	return results, durations, failed
}

// timeToolCall is runToolCall plus a stopwatch. Each goroutine writes only its
// own slot, so no synchronisation is needed beyond the WaitGroup that already
// orders these writes against the read after wg.Wait().
func timeToolCall(ctx context.Context, runner ToolRunner, call stream.ToolCall, idx int) (string, time.Duration, bool) {
	start := time.Now()
	out, failed := runToolCall(ctx, runner, call, idx)
	return out, time.Since(start), failed
}

// runToolCall decodes args, applies the per-tool timeout, and writes the
// result (or an error string) back to results[idx]. Extracted from
// executeTools so the serial and parallel branches share one code path.
func runToolCall(ctx context.Context, runner ToolRunner, call stream.ToolCall, idx int) (string, bool) {
	var args map[string]any
	if call.Arguments != "" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return "Error: invalid arguments: " + err.Error() + "\n\n" + tools.ValidationHint(call.Name), true
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
		// The bash tool owns its own deadline (tools.BashDefaultTimeoutSec,
		// overridable per call with "timeout"). It has to: a bash command
		// that runs long can be moved to the background and outlive this
		// tool call, and an external deadline here would cancel toolCtx on
		// return and kill the process we just detached. The tool enforces
		// the same 120s default and honours the same "timeout" argument, so
		// a foreground command behaves exactly as it did before.
		toolTimeout = 0
	case "subagent":
		toolTimeout = 0
	case "wait":
		// wait manages its own duration (clamped to MaxWaitSeconds) and is
		// context-aware internally; an external timeout here would cut it
		// off before it reports back, defeating the point of the tool.
		toolTimeout = 0
	case "lock", "unlock", "ask_parent", "request_timeout_extension":
		// A lock acquired without an explicit "seconds" is documented to
		// live "until you release it or your session ends" (see the "lock"
		// tool schema, tools/tool.go) — locks.Registry.Acquire implements
		// that by releasing when ctx.Done() fires. Falling through to the
		// 60s default here silently broke that contract: every no-TTL lock
		// was actually bound to this per-call ctx, not the session, and
		// auto-released 60s after being acquired regardless of what the
		// caller intended. toolTimeout=0 lets ctx be whatever the caller's
		// own session/run scope is. unlock doesn't need this (Release
		// returns immediately, never outliving any timeout), grouped here
		// only for the pair's symmetry. "ask_parent" also must not be cut
		// off by the generic default: it is meant to block until the job's
		// own context is cancelled (see jobs.Registry.Ask), not this
		// function's 60s default — a 60s external timeout here would
		// silently truncate every ask_parent call regardless of how long
		// the job is allowed to wait.
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
			return fmt.Sprintf("Error: %s tool timed out after %v", call.Name, toolTimeout), true
		}
		return "Error: " + err.Error(), true
	}
	return body, false
}
