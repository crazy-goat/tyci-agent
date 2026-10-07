package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flow"
)

// Runner starts workflow runs. NewRunner adapts the v0.3.0 flow.Manager.
type Runner interface {
	Start(ctx context.Context, workflow string, inputs map[string]string) (RunHandle, error)
}

// RunHandle is one started run. ONE final result arrives on Done; ask is a
// separate event stream.
type RunHandle interface {
	ID() string
	// Done delivers exactly once: the final result, then closes.
	Done() <-chan RunResult
	// Asks signals each time the run waits for a human answer.
	Asks() <-chan struct{}
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
	Status(runID string) (*flow.RunState, error)
}

// flowRunner adapts a FlowManager. The v0.3.0 Manager has no end event, so
// the handle reads the saved run state every poll interval. The input key for
// "work on #N" is "issue" (flow.StartRequest.Issue).
type flowRunner struct {
	m    FlowManager
	poll time.Duration
}

// NewRunner returns a Runner over the v0.3.0 flow.Manager.
func NewRunner(m FlowManager) Runner { return &flowRunner{m: m, poll: 500 * time.Millisecond} }

func (r *flowRunner) Start(ctx context.Context, workflow string, inputs map[string]string) (RunHandle, error) {
	n, err := strconv.Atoi(inputs["issue"])
	if err != nil || len(inputs) != 1 {
		// v0.3.0 starts a run from an issue number only; the roadmap run needs
		// a JSON input and returns its text, which the Manager cannot do yet.
		return nil, fmt.Errorf("workflow %q: the flow manager supports only the \"issue\" input", workflow)
	}
	id, _, err := r.m.Start(ctx, flow.StartRequest{Workflow: workflow, Issue: n})
	if err != nil {
		return nil, err
	}
	h := &flowHandle{id: id, done: make(chan RunResult, 1), asks: make(chan struct{}, 1)}
	go h.watch(ctx, r.m, r.poll)
	return h, nil
}

type flowHandle struct {
	id   string
	done chan RunResult
	asks chan struct{}
}

func (h *flowHandle) ID() string             { return h.id }
func (h *flowHandle) Done() <-chan RunResult { return h.done }
func (h *flowHandle) Asks() <-chan struct{}  { return h.asks }

func (h *flowHandle) watch(ctx context.Context, m FlowManager, poll time.Duration) {
	defer close(h.done)
	t := time.NewTicker(poll)
	defer t.Stop()
	var lastAsk time.Time
	for {
		select {
		case <-ctx.Done():
			h.done <- RunResult{Outcome: "cancelled", Err: ctx.Err()}
			return
		case <-t.C:
		}
		st, err := m.Status(h.id)
		if err != nil {
			h.done <- RunResult{Outcome: "failed", Err: err}
			return
		}
		switch st.Status {
		case "paused":
			if !st.UpdatedAt.Equal(lastAsk) {
				lastAsk = st.UpdatedAt
				select {
				case h.asks <- struct{}{}:
				default:
				}
			}
		case "done":
			if st.PR > 0 {
				h.done <- RunResult{Outcome: "merged"}
			} else {
				h.done <- RunResult{Outcome: "ended"}
			}
			return
		case "failed":
			out := "failed"
			if strings.Contains(strings.ToLower(st.Reason), "cancel") {
				out = "cancelled"
			}
			h.done <- RunResult{Outcome: out, Err: errors.New(st.Reason)}
			return
		}
	}
}
