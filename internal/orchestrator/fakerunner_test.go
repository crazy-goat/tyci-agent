package orchestrator

import (
	"context"
	"errors"
	"strconv"
	"sync"
)

// fakeHandle is a run the test finishes by hand.
type fakeHandle struct {
	id    string
	done  chan RunResult
	asks  chan struct{}
	owner *fakeRunner
}

func (h *fakeHandle) ID() string               { return h.id }
func (h *fakeHandle) Done() <-chan RunResult   { return h.done }
func (h *fakeHandle) Asks() <-chan struct{}    { return h.asks }
func (h *fakeHandle) Resumed() <-chan struct{} { return nil }
func (h *fakeHandle) ask()                     { h.asks <- struct{}{} }
func (h *fakeHandle) finish(outcome string) {
	h.owner.mu.Lock()
	h.owner.inFlight--
	h.owner.mu.Unlock()
	h.done <- RunResult{Outcome: outcome}
	close(h.done)
}

// fakeRunner records starts. Test code reads started to wait for a start.
type fakeRunner struct {
	mu          sync.Mutex
	handles     map[int]*fakeHandle
	count       map[int]int
	inFlight    int
	maxInFlight int
	startErr    map[int]error
	gate        chan struct{} // when set, Start for issue gateIssue blocks until it is closed
	gateIssue   int
	entered     chan struct{} // gets a value when Start for gateIssue reaches the gate
	oracle      func(input string) RunResult
	started     chan int // issue numbers in start order
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{handles: map[int]*fakeHandle{}, count: map[int]int{}, startErr: map[int]error{}, started: make(chan int, 100)}
}

func (r *fakeRunner) Start(_ context.Context, workflow string, in map[string]string) (RunHandle, error) {
	if workflow == "roadmap" {
		if r.oracle == nil {
			return nil, errors.New("oracle down")
		}
		h := &fakeHandle{id: "oracle", done: make(chan RunResult, 1), asks: make(chan struct{}, 1), owner: r}
		h.done <- r.oracle(in["input"])
		close(h.done)
		return h, nil
	}
	n, _ := strconv.Atoi(in["issue"])
	r.mu.Lock()
	r.count[n]++
	gate := r.gate
	gateIssue := r.gateIssue
	r.mu.Unlock()
	if gate != nil && n == gateIssue {
		r.entered <- struct{}{}
		<-gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.startErr[n]; err != nil {
		return nil, err
	}
	r.inFlight++
	if r.inFlight > r.maxInFlight {
		r.maxInFlight = r.inFlight
	}
	h := &fakeHandle{id: "run-" + in["issue"], done: make(chan RunResult, 1), asks: make(chan struct{}, 4), owner: r}
	r.handles[n] = h
	r.started <- n
	return h, nil
}

func (r *fakeRunner) handle(n int) *fakeHandle {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.handles[n]
}

func (r *fakeRunner) starts(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count[n]
}
