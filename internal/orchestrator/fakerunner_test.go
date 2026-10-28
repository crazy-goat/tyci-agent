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
	started     chan int      // issue numbers in start order
	resumed     []int         // issues of runs resumed after a restart (Adopter)
	paused      []int         // issues of paused runs (Adopter); their handles ask at once
	closes      chan []string // params of each CloseWorkflow start
	closeH      *fakeHandle   // the handle of the last CloseWorkflow start
	released    int           // calls of ReleaseAdopted (Adopter)
	closeErr    error         // when set, Start for CloseWorkflow fails with it
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{handles: map[int]*fakeHandle{}, count: map[int]int{}, startErr: map[int]error{}, started: make(chan int, 100), closes: make(chan []string, 10)}
}

func (r *fakeRunner) Start(_ context.Context, workflow string, in map[string]string, params []string) (RunHandle, error) {
	if len(params) > 0 {
		if r.closeErr != nil {
			return nil, r.closeErr
		}
		// The test ends the run by sending on the handle's done channel.
		h := &fakeHandle{id: workflow, done: make(chan RunResult, 1), asks: make(chan struct{}, 1), owner: r}
		r.mu.Lock()
		r.closeH = h
		r.mu.Unlock()
		r.closes <- params
		return h, nil
	}
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

func (r *fakeRunner) Adoptable() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(append([]int(nil), r.resumed...), r.paused...)
}

func (r *fakeRunner) ReleaseAdopted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released++
}

func (r *fakeRunner) releases() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.released
}

func (r *fakeRunner) AdoptableResumed() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.resumed...)
}

func (r *fakeRunner) Adopt(_ context.Context, n int) (RunHandle, bool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, m := range r.resumed {
		if m != n {
			continue
		}
		r.resumed = append(r.resumed[:i], r.resumed[i+1:]...)
		r.inFlight++
		if r.inFlight > r.maxInFlight {
			r.maxInFlight = r.inFlight
		}
		h := &fakeHandle{id: "resumed-" + strconv.Itoa(n), done: make(chan RunResult, 1), asks: make(chan struct{}, 4), owner: r}
		r.handles[n] = h
		return h, false, true
	}
	for i, m := range r.paused {
		if m != n {
			continue
		}
		r.paused = append(r.paused[:i], r.paused[i+1:]...)
		r.inFlight++
		h := &fakeHandle{id: "paused-" + strconv.Itoa(n), done: make(chan RunResult, 1), asks: make(chan struct{}, 4), owner: r}
		h.asks <- struct{}{}
		r.handles[n] = h
		return h, true, true
	}
	return nil, false, false
}
