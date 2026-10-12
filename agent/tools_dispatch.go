package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/crazy-goat/tyci-agent/tools"
)

// executeToolsDispatched runs a batch through disp. Each call is fired as a
// goroutine and awaited for timeoutFor(name). A call that is still running
// when its wait ends is backgrounded: its result slot holds the placeholder,
// and the real result arrives later as a tail notice.
//
// A call with arguments that are not valid JSON is answered inline before it
// is fired. It never gets a placeholder.
//
// Calls of a serial tool (MaxParallel 1) keep their order. Each one is fired
// and awaited without a deadline before the next one starts, so it never
// backgrounds. Other calls start at once.
func executeToolsDispatched(ctx context.Context, runner ToolRunner, toolCalls []stream.ToolCall, disp *Dispatcher) ([]string, []time.Duration, []bool) {
	n := len(toolCalls)
	results := make([]string, n)
	durations := make([]time.Duration, n)
	failed := make([]bool, n)
	starts := make([]time.Time, n)
	ids := make([]string, n)
	answered := make([]bool, n)

	for i, tc := range toolCalls {
		if msg, bad := badArguments(tc); bad {
			results[i], failed[i], answered[i] = msg, true, true
		}
	}
	fire := func(i int) {
		if answered[i] {
			return
		}
		tc := toolCalls[i]
		starts[i] = time.Now()
		// The FIFO place of a mutation is taken here, in call order, before the
		// goroutine starts. Its release is deferred in the call, so it is freed
		// on every path.
		var args map[string]any
		_ = json.Unmarshal([]byte(tc.Arguments), &args)
		ticket := tools.ReserveMutation(ctx, tc.Name, args)
		ids[i] = disp.Fire(ctx, func(c context.Context) (string, bool) {
			defer ticket.Release()
			out, bad := runToolCallIn(tools.WithMutationTicket(c, ticket), ctx, runner, tc, i)
			if bad && c.Err() != nil {
				return "cancelled: the turn was interrupted", true
			}
			return out, bad
		})
	}
	finish := func(i int, out dispatchOutcome) {
		results[i] = out.Text
		failed[i] = out.Failed
		durations[i] = time.Since(starts[i])
	}

	// Parallel calls start first, so they run while the serial ones wait.
	for i, tc := range toolCalls {
		if tools.MaxParallelFor(tc.Name) != 1 {
			fire(i)
		}
	}
	for i, tc := range toolCalls {
		if tools.MaxParallelFor(tc.Name) == 1 {
			fire(i)
			if !answered[i] {
				finish(i, disp.Await(ctx, ids[i], noDeadline))
			}
		}
	}
	for i, tc := range toolCalls {
		if answered[i] || tools.MaxParallelFor(tc.Name) == 1 {
			continue
		}
		wait := timeoutFor(disp.tools, tc.Name) - time.Since(starts[i])
		if wait < 0 {
			wait = 0
		}
		finish(i, disp.Await(ctx, ids[i], wait))
	}
	return results, durations, failed
}

// badArguments reports the inline error for a call whose arguments are not
// valid JSON. It uses the same text as runToolCall.
func badArguments(tc stream.ToolCall) (string, bool) {
	if tc.Arguments == "" {
		return "", false
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
		return "Error: invalid arguments: " + err.Error() + "\n\n" + tools.ValidationHint(tc.Name), true
	}
	return "", false
}
