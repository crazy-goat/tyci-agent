package agent

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/tools"
)

// dispatcher runs tool calls in goroutines. The agent loop waits for a call
// for a bounded time. When the wait ends first, the call is backgrounded: the
// model gets a placeholder, and the goroutine keeps running. Its result then
// arrives as a KindToolResult bus message, which the loop takes with takeLate
// or waitLate and appends as a tail notice.
//
// Only the waiting side stops on a handoff. The goroutine is never restarted.
//
// Failure to Fire (bad JSON, unknown tool, a role that forbids the tool) is not
// a placeholder. The caller returns that error inline as the tool_result,
// before it calls Fire. Fire itself never fails.
//
// Without a bus, nothing can deliver a late result. Then no call is ever
// backgrounded, and each call runs inline as it did before the dispatcher.
type dispatcher struct {
	mu    sync.Mutex
	next  int
	calls map[string]*dispatchCall
	// bus publishes the late results. It is nil when no bus is wired.
	bus *bus.Bus
	// self is the address that receives the late results.
	self bus.Addr
	// sub is the subscription of self. Only pump and absorb read it.
	sub *bus.Sub
	// userPending reports that a person is waiting for the agent. It may be nil.
	userPending func() bool
	// wakes counts the wake messages absorbed so far. A call backgrounds when
	// a wake arrives after it was fired.
	wakes uint64
	// late holds the tool.result messages that the loop has not taken yet.
	late []bus.ToolResult
	// pending counts the background calls that have not finished yet.
	pending int
	// changed is closed and replaced on each change of wakes, late or pending.
	changed chan struct{}
}

// dispatchCall is one tool call. text, failed and background are guarded by
// dispatcher.mu. done is closed after text and failed are set.
type dispatchCall struct {
	started    time.Time
	done       chan struct{}
	text       string
	failed     bool
	seen       uint64
	background bool
}

// dispatchOutcome is what Await returns. Background is true when the call is
// still running and Text is the placeholder.
type dispatchOutcome struct {
	Text       string
	Failed     bool
	Background bool
}

const (
	// placeholderPrefix starts the text that the model gets for a background call.
	placeholderPrefix = "started, id="
	// pollInterval is how often Await checks userPending.
	pollInterval = 50 * time.Millisecond
	// noDeadline is the timeout of a call that never backgrounds on time.
	noDeadline = time.Duration(math.MaxInt64)
)

// newDispatcher returns a dispatcher. sub must receive the messages of self,
// and it may be nil when there is no bus.
func newDispatcher(b *bus.Bus, self bus.Addr, sub *bus.Sub) *dispatcher {
	d := &dispatcher{
		calls:       make(map[string]*dispatchCall),
		bus:         b,
		self:        self,
		sub:         sub,
		userPending: tools.UserPending,
		changed:     make(chan struct{}),
	}
	if sub != nil {
		go d.pump()
	}
	return d
}

// pump absorbs the messages of sub as they arrive, until sub is closed.
func (d *dispatcher) pump() {
	for {
		select {
		case <-d.sub.Ready():
			d.absorb()
		case <-d.sub.Done():
			return
		}
	}
}

// absorb moves the waiting messages of sub into the dispatcher state. It is
// safe to call from any goroutine, and it never consumes a message that the
// loop needs: a wake only counts, and a tool.result waits in late.
func (d *dispatcher) absorb() {
	if d.sub == nil {
		return
	}
	msgs := d.sub.Drain()
	if len(msgs) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, m := range msgs {
		if wakesInline(m) {
			d.wakes++
		}
		if m.Kind == bus.KindToolResult {
			if r, err := bus.Decode[bus.ToolResult](m); err == nil {
				d.late = append(d.late, r)
			}
		}
	}
	d.notifyLocked()
}

// notifyLocked wakes every goroutine that waits on changed. The caller holds mu.
func (d *dispatcher) notifyLocked() {
	close(d.changed)
	d.changed = make(chan struct{})
}

// Fire starts run in a goroutine and returns its call id: c1, c2, and so on.
// The ids count per agent. They are not bus sequence numbers.
func (d *dispatcher) Fire(ctx context.Context, run func(context.Context) (string, bool)) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	id := "c" + strconv.Itoa(d.next)
	c := &dispatchCall{started: time.Now(), done: make(chan struct{}), seen: d.wakes}
	d.calls[id] = c
	go d.run(ctx, id, c, run)
	return id
}

// run calls run and stores its result. A panic becomes a failed result.
func (d *dispatcher) run(ctx context.Context, id string, c *dispatchCall, run func(context.Context) (string, bool)) {
	var text string
	var failed bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				text, failed = fmt.Sprintf("Error: tool panicked: %v", r), true
			}
		}()
		text, failed = run(ctx)
	}()
	d.finish(id, c, text, failed)
}

// Pending returns how many backgrounded calls have not finished yet.
func (d *dispatcher) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pending
}

// finish stores the result and closes done. A call that was backgrounded
// publishes its result before it counts as finished, so that waitLate never
// misses it.
func (d *dispatcher) finish(id string, c *dispatchCall, text string, failed bool) {
	d.mu.Lock()
	background := c.background
	c.text, c.failed = text, failed
	close(c.done)
	d.mu.Unlock()

	if !background {
		return
	}
	if d.bus != nil {
		// A publish error means that the bus is closed. The journal-only path
		// for that case comes in a later stage.
		_, _ = bus.Publish(d.bus, bus.KindToolResult, d.self, d.self, bus.OriginSystem, bus.ToolResult{
			CallID:     id,
			Content:    text,
			Failed:     failed,
			DurationMS: time.Since(c.started).Milliseconds(),
		})
	}
	d.mu.Lock()
	d.pending--
	delete(d.calls, id)
	d.notifyLocked()
	d.mu.Unlock()
}

// Await waits for call id until it finishes, a wake message arrives, the
// user is pending, timeout ends or ctx is done. It returns the real result
// when the call has finished. Otherwise it returns the placeholder and
// backgrounds the call. A timeout of 0 gives the placeholder at once.
//
// Without a bus the call cannot be backgrounded, so Await waits for it.
func (d *dispatcher) Await(ctx context.Context, id string, timeout time.Duration) dispatchOutcome {
	d.mu.Lock()
	c, ok := d.calls[id]
	d.mu.Unlock()
	if !ok {
		return dispatchOutcome{Text: "Error: unknown call id " + id, Failed: true}
	}

	var ctxDone <-chan struct{}
	if d.bus == nil {
		timeout = noDeadline
	} else {
		ctxDone = ctx.Done()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var poll <-chan time.Time
	if d.userPending != nil {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		poll = ticker.C
	}

	for {
		d.mu.Lock()
		if out, ok := d.finishedLocked(id, c); ok {
			d.mu.Unlock()
			return out
		}
		if d.wakes != c.seen {
			out := d.handOffLocked(id, c)
			d.mu.Unlock()
			return out
		}
		changed := d.changed
		d.mu.Unlock()

		select {
		case <-c.done:
		case <-changed:
		case <-poll:
			if d.userPending() {
				return d.handOff(id, c)
			}
		case <-timer.C:
			return d.handOff(id, c)
		case <-ctxDone:
			return d.handOff(id, c)
		}
	}
}

// finishedLocked returns the real result when the call has finished. The
// caller holds mu.
func (d *dispatcher) finishedLocked(id string, c *dispatchCall) (dispatchOutcome, bool) {
	select {
	case <-c.done:
		delete(d.calls, id)
		return dispatchOutcome{Text: c.text, Failed: c.failed}, true
	default:
		return dispatchOutcome{}, false
	}
}

// handOff returns the real result when the call has finished. Otherwise it
// backgrounds the call and returns the placeholder.
func (d *dispatcher) handOff(id string, c *dispatchCall) dispatchOutcome {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.handOffLocked(id, c)
}

// handOffLocked is handOff for a caller that holds mu.
func (d *dispatcher) handOffLocked(id string, c *dispatchCall) dispatchOutcome {
	if out, ok := d.finishedLocked(id, c); ok {
		return out
	}
	c.background = true
	d.pending++
	return dispatchOutcome{Text: placeholderPrefix + id, Background: true}
}

// takeLate returns the late results that have arrived and not been taken yet.
func (d *dispatcher) takeLate() []bus.ToolResult {
	d.absorb()
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.late
	d.late = nil
	return out
}

// waitLate blocks until a late result has arrived or no call is in flight.
// It also returns when ctx is done or a user is pending, so that a person
// is never kept waiting for a background tool. The loop calls it at the end
// of a turn. Calls still running then deliver their results at the next turn.
func (d *dispatcher) waitLate(ctx context.Context) []bus.ToolResult {
	var poll <-chan time.Time
	if d.userPending != nil {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		poll = ticker.C
	}
	for {
		d.absorb()
		d.mu.Lock()
		if len(d.late) > 0 || d.pending == 0 {
			out := d.late
			d.late = nil
			d.mu.Unlock()
			return out
		}
		changed := d.changed
		d.mu.Unlock()

		select {
		case <-changed:
		case <-poll:
			if d.userPending() {
				return d.takeLate()
			}
		case <-ctx.Done():
			return d.takeLate()
		}
	}
}

// wakesInline reports whether a message received while a call is awaited
// ends the wait, so that the call is backgrounded at once. This is the rule of
// SDR 4.3. job.status does not wake, because a running job reports progress
// often. The awaited call's own result never reaches this check: it is seen
// through the done channel.
func wakesInline(msg bus.Message) bool {
	switch msg.Kind {
	case bus.KindToolResult, bus.KindAgentMessage, bus.KindAskRequest, bus.KindPingMissed:
		return true
	}
	return false
}

// timeoutFor returns how long a call of toolName waits before it is
// backgrounded. The values are a stub. Config comes in issue #198.
//
// subagent and ask_parent are never inline: they get 0 and the placeholder at
// once. wait blocks by design, so it never backgrounds.
func timeoutFor(toolName string) time.Duration {
	switch toolName {
	case "subagent", "ask_parent":
		return 0
	case "wait":
		return noDeadline
	case "bash", "lua":
		return 60 * time.Second
	default:
		return 2500 * time.Millisecond
	}
}
