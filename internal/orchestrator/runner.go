package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/crazy-goat/tyci-agent/internal/flow"
)

// Runner starts workflow runs. NewRunner adapts flow.Manager.
type Runner interface {
	Start(ctx context.Context, workflow string, inputs map[string]string) (RunHandle, error)
}

// RunHandle is one started run. ONE final result arrives on Done; ask and
// resume are separate event streams.
type RunHandle interface {
	ID() string
	// Done delivers exactly once: the final result, then closes.
	Done() <-chan RunResult
	// Asks signals each time the run waits for a human answer.
	Asks() <-chan struct{}
	// Resumed signals each time a waiting run gets its answer and runs again.
	Resumed() <-chan struct{}
}

// RunResult is the final result of a run.
type RunResult struct {
	Outcome string // merged | ended | failed | cancelled
	Output  string // final agent text; the roadmap run returns the oracle JSON here
	Err     error
}

// FlowManager is the part of flow.Manager the adapter uses.
type FlowManager interface {
	Start(ctx context.Context, req flow.StartRequest) (string, []string, error)
	RunText(ctx context.Context, workflow, input string) (string, error)
	Subscribe(fn func(flow.RunEvent)) func()
}

// flowRunner adapts a FlowManager. The input key for "work on #N" is "issue"
// (flow.StartRequest.Issue). The input key "input" runs a one-agent workflow
// on a text and returns the final agent text.
type flowRunner struct {
	m FlowManager
}

// NewRunner returns a Runner over flow.Manager.
func NewRunner(m FlowManager) Runner { return &flowRunner{m: m} }

func (r *flowRunner) Start(ctx context.Context, workflow string, inputs map[string]string) (RunHandle, error) {
	if text, ok := inputs["input"]; ok && len(inputs) == 1 {
		return r.startText(ctx, workflow, text), nil
	}
	n, err := strconv.Atoi(inputs["issue"])
	if err != nil || len(inputs) != 1 {
		return nil, fmt.Errorf("workflow %q: use exactly one input, \"issue\" or \"input\"", workflow)
	}
	h := newFlowHandle()
	unsub := r.m.Subscribe(h.add)
	id, _, err := r.m.Start(ctx, flow.StartRequest{Workflow: workflow, Issue: n})
	if err != nil {
		unsub()
		return nil, err
	}
	h.id = id
	go func() {
		defer unsub()
		h.watch(ctx)
	}()
	return h, nil
}

// startText runs the workflow in a goroutine and delivers the text.
func (r *flowRunner) startText(ctx context.Context, workflow, text string) RunHandle {
	h := newFlowHandle()
	h.id = workflow
	go func() {
		defer close(h.done)
		out, err := r.m.RunText(ctx, workflow, text)
		switch {
		case ctx.Err() != nil:
			h.done <- RunResult{Outcome: "cancelled", Err: ctx.Err()}
		case err != nil:
			h.done <- RunResult{Outcome: "failed", Err: err}
		default:
			h.done <- RunResult{Outcome: "ended", Output: out}
		}
	}()
	return h
}

type flowHandle struct {
	id      string
	done    chan RunResult
	asks    chan struct{}
	resumed chan struct{}

	mu   sync.Mutex
	q    []flow.RunEvent
	wake chan struct{}
}

func newFlowHandle() *flowHandle {
	return &flowHandle{
		done: make(chan RunResult, 1), asks: make(chan struct{}, 1), resumed: make(chan struct{}, 1),
		wake: make(chan struct{}, 1),
	}
}

func (h *flowHandle) ID() string               { return h.id }
func (h *flowHandle) Done() <-chan RunResult   { return h.done }
func (h *flowHandle) Asks() <-chan struct{}    { return h.asks }
func (h *flowHandle) Resumed() <-chan struct{} { return h.resumed }

// add queues an event. It never blocks. The queue keeps events of other runs
// until watch filters them by id, because the id is known only after Start.
func (h *flowHandle) add(ev flow.RunEvent) {
	h.mu.Lock()
	h.q = append(h.q, ev)
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func drain(c chan struct{}) {
	select {
	case <-c:
	default:
	}
}

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

func (h *flowHandle) watch(ctx context.Context) {
	defer close(h.done)
	for {
		select {
		case <-ctx.Done():
			h.done <- RunResult{Outcome: "cancelled", Err: ctx.Err()}
			return
		case <-h.wake:
		}
		h.mu.Lock()
		q := h.q
		h.q = nil
		h.mu.Unlock()
		for _, ev := range q {
			if ev.Run != h.id {
				continue
			}
			switch ev.Status {
			case "running":
				drain(h.asks)
				signal(h.resumed)
			case "paused":
				drain(h.resumed)
				signal(h.asks)
			case "done":
				if ev.PR > 0 {
					h.done <- RunResult{Outcome: "merged"}
				} else {
					h.done <- RunResult{Outcome: "ended"}
				}
				return
			case "failed":
				out := "failed"
				if strings.Contains(strings.ToLower(ev.Reason), "cancel") {
					out = "cancelled"
				}
				h.done <- RunResult{Outcome: out, Err: errors.New(ev.Reason)}
				return
			}
		}
	}
}
