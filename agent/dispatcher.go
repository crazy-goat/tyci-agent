package agent

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/tools"
)

// Dispatcher runs tool calls in goroutines. The agent loop waits for a call
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
// backgrounded, and each call runs inline as it did before the Dispatcher.
type Dispatcher struct {
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
	// tools holds the background_after settings that timeoutFor reads, and
	// the result_tail_bytes limit that publishResult applies.
	tools ToolsConfig
	// resultDir receives the full output of a large late result. It is set
	// with SetResultDir and is guarded by mu. Empty means no file is written.
	resultDir string
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
// Dispatcher.mu. done is closed after text and failed are set.
type dispatchCall struct {
	started    time.Time
	done       chan struct{}
	text       string
	failed     bool
	seen       uint64
	background bool
	// cancel ends the call's own context. It is set by Fire and is never nil.
	cancel context.CancelFunc
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
	// placeholderSuffix ends the text that the model gets for a background call.
	placeholderSuffix = "; the result arrives later as a tool.result notice; wait does not take this id yet"
	// pollInterval is how often Await checks userPending.
	pollInterval = 50 * time.Millisecond
	// noDeadline is the timeout of a call that never backgrounds on time.
	noDeadline = time.Duration(math.MaxInt64)
)

// NewDispatcher returns a Dispatcher that receives its late results at self.
// tools sets when calls move to the background (see timeoutFor).
// It returns nil when b is nil: then every call runs inline. The owner calls
// Close when it no longer needs the Dispatcher. self must be unique per
// Dispatcher, because call ids are only unique inside one Dispatcher.
func NewDispatcher(b *bus.Bus, self bus.Addr, tools ToolsConfig) *Dispatcher {
	if b == nil {
		return nil
	}
	sub := b.Subscribe("tool-results", bus.Filter{
		To:    self,
		Kinds: []bus.Kind{bus.KindToolResult, bus.KindAgentMessage, bus.KindAskRequest, bus.KindPingMissed},
	})
	d := newDispatcher(b, self, sub)
	d.tools = tools
	return d
}

var dispatchSeq atomic.Uint64

// UniqueDispatchAddr returns an address that no other Dispatcher of this
// process uses.
func UniqueDispatchAddr() bus.Addr {
	return bus.Addr{Type: bus.AddrAgent, ID: fmt.Sprintf("tools-%d", dispatchSeq.Add(1))}
}

// Close stops the subscription of d and cancels the calls still running. It is
// safe on a nil Dispatcher.
func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.cancelBackground()
	if d.sub != nil {
		d.sub.Close()
	}
}

// Wait blocks until no backgrounded call is in flight, or until ctx is done.
// A headless run uses it so that the process does not exit mid-call.
func (d *Dispatcher) Wait(ctx context.Context) {
	if d == nil {
		return
	}
	for {
		d.mu.Lock()
		if d.pending == 0 {
			d.mu.Unlock()
			return
		}
		changed := d.changed
		d.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return
		}
	}
}

// NewChildDispatcher returns the Dispatcher of a subagent. Its calls never
// background: each one blocks until it finishes or its context ends, so the
// subagent never loses a late result when its Run ends. A child has no bus
// and no user poll. The main conversation uses NewDispatcher.
func NewChildDispatcher() *Dispatcher {
	d := newDispatcher(nil, bus.Addr{}, nil)
	d.userPending = nil
	return d
}

// newDispatcher returns a Dispatcher. sub must receive the messages of self,
// and it may be nil when there is no bus.
func newDispatcher(b *bus.Bus, self bus.Addr, sub *bus.Sub) *Dispatcher {
	d := &Dispatcher{
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
func (d *Dispatcher) pump() {
	for {
		select {
		case <-d.sub.Ready():
			d.absorb()
		case <-d.sub.Done():
			return
		}
	}
}

// absorb moves the waiting messages of sub into the Dispatcher state. It is
// safe to call from any goroutine, and it never consumes a message that the
// loop needs: a wake only counts, and a tool.result waits in late.
func (d *Dispatcher) absorb() {
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
func (d *Dispatcher) notifyLocked() {
	close(d.changed)
	d.changed = make(chan struct{})
}

// Fire starts run in a goroutine and returns its call id: c1, c2, and so on.
// The ids count per agent. They are not bus sequence numbers.
func (d *Dispatcher) Fire(ctx context.Context, run func(context.Context) (string, bool)) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	id := "c" + strconv.Itoa(d.next)
	// The call must outlive the Run that started it: a backgrounded call
	// delivers its result in a later turn. So it runs on a context that the
	// end of the Run does not cancel. Its own cancel ends it on ESC (see
	// Await and cancelBackground) and on Close.
	callCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c := &dispatchCall{started: time.Now(), done: make(chan struct{}), seen: d.wakes, cancel: cancel}
	d.calls[id] = c
	go d.run(callCtx, id, c, run)
	return id
}

// run calls run and stores its result. A panic becomes a failed result.
func (d *Dispatcher) run(ctx context.Context, id string, c *dispatchCall, run func(context.Context) (string, bool)) {
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
func (d *Dispatcher) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pending
}

// finish stores the result and closes done. A call that was backgrounded
// publishes its result before it counts as finished, so that waitLate never
// misses it.
func (d *Dispatcher) finish(id string, c *dispatchCall, text string, failed bool) {
	d.mu.Lock()
	background := c.background
	c.text, c.failed = text, failed
	close(c.done)
	d.mu.Unlock()
	c.cancel()

	if !background {
		return
	}
	d.publishResult(id, c.started, text, failed)
	// The result is on the bus queue, not yet in late. Move it there before
	// pending drops, or waitLate could see no pending call and no result.
	d.absorb()
	d.mu.Lock()
	d.pending--
	delete(d.calls, id)
	d.notifyLocked()
	d.mu.Unlock()
}

// SetResultDir sets the directory that receives the full output of a large
// late result. The agent loop sets it at the start of each Run, from the open
// session. It is safe on a nil Dispatcher.
func (d *Dispatcher) SetResultDir(dir string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.resultDir = dir
	d.mu.Unlock()
}

// publishResult delivers the late result of call id. A content longer than
// the result_tail_bytes limit keeps only its last bytes. The full output goes
// to the result directory, and the notice names its path.
func (d *Dispatcher) publishResult(id string, started time.Time, text string, failed bool) {
	r := bus.ToolResult{
		CallID:     id,
		Content:    text,
		Failed:     failed,
		DurationMS: time.Since(started).Milliseconds(),
	}
	limit := d.tools.ResultTailLimit()
	if len(text) > limit {
		d.mu.Lock()
		dir := d.resultDir
		d.mu.Unlock()
		path, err := writeFull(dir, id, text)
		if err != nil {
			path = ""
		}
		tail, _ := tailResult(text, limit)
		r.Content = truncationNote(limit, len(text), path) + "\n" + tail
		r.Truncated = true
		r.FullPath = path
	}
	d.deliver(r)
}

// deliver publishes a late result on the bus. When the bus is closed, the
// result is kept in memory, so that the next turn still takes it.
func (d *Dispatcher) deliver(r bus.ToolResult) {
	if d.bus != nil {
		if _, err := bus.Publish(d.bus, bus.KindToolResult, d.self, d.self, bus.OriginSystem, r); err == nil {
			return
		}
	}
	d.mu.Lock()
	d.late = append(d.late, r)
	d.notifyLocked()
	d.mu.Unlock()
}

// Await waits for call id until it finishes, a wake message arrives, the
// user is pending, timeout ends or ctx is done. It returns the real result
// when the call has finished. Otherwise it returns the placeholder and
// backgrounds the call. A timeout of 0 gives the placeholder at once.
//
// A timeout of noDeadline is a blocking call. It never backgrounds: Await
// ignores wake messages and the user, and waits for the call to finish or
// for ctx to end. A serial tool depends on this, because the next call of
// its batch starts only after this one returns.
//
// When ctx ends first (ESC, or the Run is cancelled), the call is cancelled
// and Await returns its result. This is the explicit cancel path. A call
// that is already backgrounded is cancelled by cancelBackground instead.
//
// Without a bus the call cannot be backgrounded, so Await waits for it.
// A child dispatcher (NewChildDispatcher) has no bus, so its calls block too.
func (d *Dispatcher) Await(ctx context.Context, id string, timeout time.Duration) dispatchOutcome {
	d.mu.Lock()
	c, ok := d.calls[id]
	d.mu.Unlock()
	if !ok {
		return dispatchOutcome{Text: "Error: unknown call id " + id, Failed: true}
	}

	blocking := timeout == noDeadline || d.bus == nil
	// A blocking call has no timer: it never backgrounds on time.
	var timeoutC <-chan time.Time
	if !blocking {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutC = timer.C
	}
	var poll <-chan time.Time
	if !blocking && d.userPending != nil {
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
		if !blocking && d.wakes != c.seen {
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
		case <-timeoutC:
			return d.handOff(id, c)
		case <-ctx.Done():
			return d.cancelAndWait(id, c)
		}
	}
}

// cancelAndWait cancels call c and waits for its result. The call honours its
// context, so it finishes soon after the cancel. The result is returned inline.
func (d *Dispatcher) cancelAndWait(id string, c *dispatchCall) dispatchOutcome {
	c.cancel()
	<-c.done
	d.mu.Lock()
	defer d.mu.Unlock()
	out, _ := d.finishedLocked(id, c)
	return out
}

// cancelBackground cancels every call that is backgrounded and still running.
// The agent loop calls it when its Run is cancelled (ESC), so that a
// background call stops with the turn the user cancelled. A call is not
// cancelled when a turn ends normally.
func (d *Dispatcher) cancelBackground() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.calls {
		if c.background {
			c.cancel()
		}
	}
}

// finishedLocked returns the real result when the call has finished. The
// caller holds mu.
func (d *Dispatcher) finishedLocked(id string, c *dispatchCall) (dispatchOutcome, bool) {
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
func (d *Dispatcher) handOff(id string, c *dispatchCall) dispatchOutcome {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.handOffLocked(id, c)
}

// handOffLocked is handOff for a caller that holds mu.
func (d *Dispatcher) handOffLocked(id string, c *dispatchCall) dispatchOutcome {
	if out, ok := d.finishedLocked(id, c); ok {
		return out
	}
	c.background = true
	d.pending++
	return dispatchOutcome{Text: placeholderPrefix + id + placeholderSuffix, Background: true}
}

// takeLate returns the late results that have arrived and not been taken yet.
func (d *Dispatcher) takeLate() []bus.ToolResult {
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
func (d *Dispatcher) waitLate(ctx context.Context) []bus.ToolResult {
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

// fastTools are the built-in tools with a fast class. Every other tool is
// slow, including MCP tools and unknown names.
var fastTools = map[string]bool{
	"read":            true,
	"find":            true,
	"skills":          true,
	"agents":          true,
	"todo":            true,
	"write":           true,
	"edit":            true,
	"lock":            true,
	"unlock":          true,
	"report_progress": true,
}

// timeoutFor returns how long a call of toolName waits before it is
// backgrounded. The order is: a blocking tool, then the per-tool value in
// cfg.BackgroundAfterSec, then the class value (fast or slow), then the
// built-in default of the class.
//
// subagent, ask_parent and wait block by design: the model waits for the
// answer or the child result, so they never background (noDeadline, see
// Await). Their overrides and class values are ignored.
//
// This wait is not a kill deadline. A tool keeps its own timeout in
// runToolCall, which does not change here.
func timeoutFor(cfg ToolsConfig, toolName string) time.Duration {
	switch toolName {
	case "subagent", "ask_parent", "wait":
		return noDeadline
	}
	if sec, ok := cfg.BackgroundAfterSec[toolName]; ok {
		return time.Duration(sec * float64(time.Second))
	}
	if isFastTool(toolName) {
		return classDuration(cfg.BackgroundAfterFastSec, defaultBackgroundAfterFast)
	}
	return classDuration(cfg.BackgroundAfterSlowSec, defaultBackgroundAfterSlow)
}

// isFastTool reports whether toolName has the fast class. An MCP tool is
// slow, even when its name matches a built-in fast tool.
func isFastTool(toolName string) bool {
	if mcpRunner := tools.GetMCPToolRunner(); mcpRunner != nil && mcpRunner.HasTool(toolName) {
		return false
	}
	return fastTools[toolName]
}

// classDuration returns the configured class wait, or def when it is absent.
func classDuration(sec *float64, def time.Duration) time.Duration {
	if sec == nil {
		return def
	}
	return time.Duration(*sec * float64(time.Second))
}
