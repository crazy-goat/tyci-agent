package agent

import (
	"context"
	"errors"
	"sync/atomic"
)

// errToolCallLimit is the error result of a tool call that is past the limit.
// The call does not run.
var errToolCallLimit = errors.New("tool call limit reached for this run; the call was not run")

// toolCallLimit wraps the tool runner of one Run and counts its calls. A call
// past max is not run: it gets errToolCallLimit, and exceeded is set. Run
// then stops after the batch that holds that call.
type toolCallLimit struct {
	runner   ToolRunner
	max      int64
	count    atomic.Int64
	exceeded atomic.Bool
}

func (l *toolCallLimit) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	if l.count.Add(1) > l.max {
		l.exceeded.Store(true)
		return "", errToolCallLimit
	}
	return l.runner.Run(ctx, name, args)
}
