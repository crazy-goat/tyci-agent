package jobs

import "sync"

// maxPendingNotices bounds the queue. A notice is a single short line, so 64
// is far above any realistic backlog; the cap only exists so a runaway
// producer (a script spawning background commands in a loop) can't grow the
// slice without bound while nobody drains it. Oldest notices are dropped
// first — the newest are the ones the model still needs to act on.
const maxPendingNotices = 64

// Notifier is a queue of short, model-facing notices produced by background
// work that finished while the agent was busy elsewhere or idle.
//
// It has two consumers, and both are needed:
//
//   - Drain is wired into agent.Config.NextMessages, so a notice queued
//     while a turn is in flight reaches the model at the next safe point
//     (see agent.Run's drain site) without waiting for the user.
//   - Signal wakes an idle REPL loop (see runTUI) so a notice queued while
//     nobody is running can start a fresh turn on its own, instead of
//     sitting in the queue until the user happens to type something.
//
// Notifier deliberately knows nothing about jobs.Registry: the producer
// formats its own one-line notice and hands over a string. That keeps it
// usable by anything backgrounded in future without teaching it new job
// shapes.
type Notifier struct {
	mu      sync.Mutex
	pending []string
	signal  chan struct{}
}

func NewNotifier() *Notifier {
	return &Notifier{signal: make(chan struct{}, 1)}
}

// Notify queues text and wakes anyone selecting on Signal. Never blocks:
// the signal channel has capacity 1 and is a level-triggered "something is
// pending" edge, not a per-notice stream — a consumer that wakes once and
// drains everything is the intended pattern.
//
// The signature matches the tools package's JobNotifier contract, so
// *Notifier satisfies it structurally with no adapter (same layering rule as
// the JobWaiter/JobStarter adapters in btw.go).
func (n *Notifier) Notify(text string) {
	if text == "" {
		return
	}
	n.mu.Lock()
	n.pending = append(n.pending, text)
	if len(n.pending) > maxPendingNotices {
		n.pending = n.pending[len(n.pending)-maxPendingNotices:]
	}
	n.mu.Unlock()

	select {
	case n.signal <- struct{}{}:
	default:
	}
}

// Drain returns every queued notice in FIFO order and empties the queue.
// Returns nil (not an empty slice) when there is nothing pending, so callers
// can test with len() or nil-ness interchangeably.
func (n *Notifier) Drain() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.pending) == 0 {
		return nil
	}
	out := n.pending
	n.pending = nil
	// Drop a stale wakeup edge: whoever drained here has already seen
	// everything the edge was announcing, so leaving it armed would wake an
	// idle loop for an empty queue.
	select {
	case <-n.signal:
	default:
	}
	return out
}

// Signal fires when a notice is queued. Receiving from it is only a hint
// that the queue *was* non-empty: always follow up with Drain and tolerate
// an empty result, since another consumer (NextMessages during an in-flight
// turn) may have taken the notices first.
func (n *Notifier) Signal() <-chan struct{} { return n.signal }
