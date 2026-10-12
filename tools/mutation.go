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
	queue []chan struct{}
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

// acquireMutation waits for the worktree FIFO and returns its release func.
// It returns nil when ctx ends before the turn comes; the caller must then
// not run the mutation.
func acquireMutation(ctx context.Context) func() {
	l := mutationFIFOFor(ctx)
	l.mu.Lock()
	if !l.held {
		l.held = true
		l.mu.Unlock()
		return l.release
	}
	ch := make(chan struct{})
	l.queue = append(l.queue, ch)
	l.mu.Unlock()

	select {
	case <-ch:
		return l.release
	case <-ctx.Done():
	}
	l.mu.Lock()
	for i, c := range l.queue {
		if c == ch {
			l.queue = append(l.queue[:i], l.queue[i+1:]...)
			l.mu.Unlock()
			return nil
		}
	}
	// The turn was granted just as ctx ended: pass it on.
	l.mu.Unlock()
	l.release()
	return nil
}

// release hands the FIFO to the next waiter, or frees it.
func (l *fifoLock) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.queue) > 0 {
		ch := l.queue[0]
		l.queue = l.queue[1:]
		close(ch)
		return
	}
	l.held = false
}
