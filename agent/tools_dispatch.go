package agent

import (
	"context"
	"time"

	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/crazy-goat/tyci-agent/tools"
)

// executeToolsDispatched runs a batch through disp. Each call is fired as a
// goroutine and awaited for timeoutFor(name). A call that is still running
// when its wait ends is backgrounded: its result slot holds the placeholder,
// and the real result arrives later as a tail notice.
//
// Calls of a serial tool (MaxParallel 1) keep their order. Each one is fired
// and awaited without a deadline before the next one starts, so it never
// backgrounds. Other calls start at once.
func executeToolsDispatched(ctx context.Context, runner ToolRunner, toolCalls []stream.ToolCall, disp *dispatcher) ([]string, []time.Duration, []bool) {
	n := len(toolCalls)
	results := make([]string, n)
	durations := make([]time.Duration, n)
	failed := make([]bool, n)
	starts := make([]time.Time, n)
	ids := make([]string, n)

	fire := func(i int) {
		tc := toolCalls[i]
		starts[i] = time.Now()
		ids[i] = disp.Fire(ctx, func(c context.Context) (string, bool) {
			return runToolCall(c, runner, tc, i)
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
			finish(i, disp.Await(ctx, ids[i], noDeadline))
		}
	}
	for i, tc := range toolCalls {
		if tools.MaxParallelFor(tc.Name) != 1 {
			wait := timeoutFor(tc.Name) - time.Since(starts[i])
			if wait < 0 {
				wait = 0
			}
			finish(i, disp.Await(ctx, ids[i], wait))
		}
	}
	return results, durations, failed
}
