package tools

import (
	"context"
	"sync"
)

// Mutations of one worktree run in the order they start. The FIFO covers
// write, edit, bash and the Lua calls ctx.run and ctx.write. Reads do not
// take it, so they stay parallel. A bash call with parallel=true skips it.
//
// The FIFO is per worktree, so two agents in one worktree share it, and two
// worktrees do not block each other. It is in-process only: it orders the
// calls of one tyci process.

// fifoLock is a mutex that grants waiters in arrival order.
type fifoLock struct {
	mu    sync.Mutex
	held  bool
	queue []*MutationTicket
}

// MutationTicket is a place in the worktree FIFO. A dispatcher takes the
// ticket of each mutating call in call order, before the call's goroutine
// starts. So the mutations of one batch run in call order, whatever order
// their goroutines start in. Release frees the place on every path, and it
// is safe to call more than once.
type MutationTicket struct {
	l       *fifoLock
	ch      chan struct{}
	granted bool // guarded by l.mu
	done    bool // guarded by l.mu
}

type mutationTicketKey struct{}

// WithMutationTicket returns a context that carries t for the next
// AcquireMutation of the call. A nil t means no ticket: the call takes a place
// when it reaches AcquireMutation.
func WithMutationTicket(ctx context.Context, t *MutationTicket) context.Context {
	return context.WithValue(ctx, mutationTicketKey{}, t)
}

// ReserveMutation takes a FIFO ticket for the call name with args when the
// call changes files of the worktree of ctx. It returns nil for any other
// call. It does not wait. The caller calls Release when the call ends.
func ReserveMutation(ctx context.Context, name string, args map[string]any) *MutationTicket {
	if !mutates(name, args) {
		return nil
	}
	return reserve(ctx)
}

// mutates reports whether a call changes files: write (its edit mode too),
// and bash unless it says parallel=true. Lua is not reserved, so it takes a
// place when it reaches AcquireMutation.
func mutates(name string, args map[string]any) bool {
	switch name {
	case "write":
		return true
	case "bash":
		parallel, _ := args["parallel"].(bool)
		return !parallel
	}
	return false
}

// mutationFIFOs maps a worktree directory to its FIFO.
var mutationFIFOs = struct {
	mu sync.Mutex
	m  map[string]*fifoLock
}{m: map[string]*fifoLock{}}

func mutationFIFOFor(ctx context.Context) *fifoLock {
	key := Workdir(ctx)
	mutationFIFOs.mu.Lock()
	defer mutationFIFOs.mu.Unlock()
	l, ok := mutationFIFOs.m[key]
	if !ok {
		l = &fifoLock{}
		mutationFIFOs.m[key] = l
	}
	return l
}

// reserve takes a place in the FIFO of ctx. It is granted at once when the
// FIFO is free, and queued otherwise.
func reserve(ctx context.Context) *MutationTicket {
	l := mutationFIFOFor(ctx)
	t := &MutationTicket{l: l, ch: make(chan struct{})}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		l.held = true
		t.granted = true
		close(t.ch)
	} else {
		l.queue = append(l.queue, t)
	}
	return t
}

// wait blocks until the turn of t comes. It returns false when ctx ends
// first. The caller then calls Release, which passes on a turn that was
// granted just as ctx ended.
func (t *MutationTicket) wait(ctx context.Context) bool {
	select {
	case <-t.ch:
		return true
	case <-ctx.Done():
		return false
	}
}

// Release frees the place of t. It passes the turn on when t had it, and
// leaves the queue when t still waits. It does nothing after the first call.
func (t *MutationTicket) Release() {
	if t == nil {
		return
	}
	l := t.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	if t.granted {
		l.handOffLocked()
		return
	}
	for i, q := range l.queue {
		if q == t {
			l.queue = append(l.queue[:i], l.queue[i+1:]...)
			return
		}
	}
}

// handOffLocked gives the turn to the next waiter, or frees the FIFO.
// The caller holds l.mu.
func (l *fifoLock) handOffLocked() {
	if len(l.queue) == 0 {
		l.held = false
		return
	}
	next := l.queue[0]
	l.queue = l.queue[1:]
	next.granted = true
	close(next.ch)
}

// AcquireMutation waits for the worktree FIFO and returns its release func.
// It uses the ticket of ctx when the dispatcher took one. Otherwise it takes
// a place now. It returns nil when ctx ends before the turn comes; the caller
// must then not run the mutation.
func AcquireMutation(ctx context.Context) func() {
	t, _ := ctx.Value(mutationTicketKey{}).(*MutationTicket)
	if t == nil {
		t = reserve(ctx)
	}
	if t.wait(ctx) {
		return t.Release
	}
	t.Release()
	return nil
}
