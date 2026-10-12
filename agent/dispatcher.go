package agent

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
)

// dispatcher runs tool calls in goroutines. The agent loop waits for a call
// for a bounded time. When the wait ends first, the call is backgrounded: the
// model gets a placeholder, and the goroutine keeps running. Its result then
// arrives as a KindToolResult bus message.
//
// Only the waiting side stops on a handoff. The goroutine is never restarted.
//
// Failure to Fire (bad JSON, unknown tool, a role that forbids the tool) is not
// a placeholder. The caller returns that error inline as the tool_result,
// before it calls Fire. Fire itself never fails.
type dispatcher struct {
	mu    sync.Mutex
	next  int
	calls map[string]*dispatchCall
	// bus receives the late results. It may be nil, and then no result is published.
	bus *bus.Bus
	// self is the address of the agent that owns the dispatcher.
	self bus.Addr
	// inbox carries the messages that the agent received while it waits.
	inbox <-chan bus.Message
}

// dispatchCall is one tool call. text, failed and background are guarded by
// dispatcher.mu. done is closed after text and failed are set.
type dispatchCall struct {
	name       string
	started    time.Time
	done       chan struct{}
	text       string
	failed     bool
	background bool
}

// dispatchOutcome is what Await returns. Background is true when the call is
// still running and Text is the placeholder.
type dispatchOutcome struct {
	Text       string
	Failed     bool
	Background bool
}

// placeholderPrefix starts the text that the model gets for a background call.
const placeholderPrefix = "started, id="

func newDispatcher(b *bus.Bus, agentID string, inbox <-chan bus.Message) *dispatcher {
	return &dispatcher{
		calls: make(map[string]*dispatchCall),
		bus:   b,
		self:  bus.Addr{Type: bus.AddrAgent, ID: agentID},
		inbox: inbox,
	}
}

// Fire starts run in a goroutine and returns its call id: c1, c2, and so on.
// The ids count per agent. They are not bus sequence numbers.
func (d *dispatcher) Fire(ctx context.Context, name string, run func(context.Context) (string, error)) string {
	d.mu.Lock()
	d.next++
	id := "c" + strconv.Itoa(d.next)
	c := &dispatchCall{name: name, started: time.Now(), done: make(chan struct{})}
	d.calls[id] = c
	d.mu.Unlock()

	go d.run(ctx, id, c, run)
	return id
}

// run calls run and stores its result. A panic becomes a failed result.
func (d *dispatcher) run(ctx context.Context, id string, c *dispatchCall, run func(context.Context) (string, error)) {
	var text string
	var failed bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				text, failed = fmt.Sprintf("Error: tool panicked: %v", r), true
			}
		}()
		out, err := run(ctx)
		if err != nil {
			text, failed = "Error: "+err.Error(), true
			return
		}
		text = out
	}()
	d.finish(id, c, text, failed)
}

// finish stores the result and closes done. A call that was backgrounded
// publishes its result on the bus.
func (d *dispatcher) finish(id string, c *dispatchCall, text string, failed bool) {
	d.mu.Lock()
	c.text, c.failed = text, failed
	close(c.done)
	background := c.background
	if background {
		delete(d.calls, id)
	}
	d.mu.Unlock()

	if background && d.bus != nil {
		// A publish error means that the bus is closed at shutdown. The
		// journal path for that case comes in a later issue.
		_, _ = bus.Publish(d.bus, bus.KindToolResult, d.self, d.self, bus.OriginSystem, bus.ToolResult{
			CallID:     id,
			Content:    text,
			Failed:     failed,
			DurationMS: time.Since(c.started).Milliseconds(),
		})
	}
}

// Await waits for call id until it finishes, a message wakes it (see
// wakesInline), timeout ends or ctx is done. It returns the real result when
// the call has finished. Otherwise it returns the placeholder and backgrounds
// the call. A timeout of 0 gives the placeholder at once.
func (d *dispatcher) Await(ctx context.Context, id string, timeout time.Duration) dispatchOutcome {
	d.mu.Lock()
	c, ok := d.calls[id]
	d.mu.Unlock()
	if !ok {
		return dispatchOutcome{Text: "Error: unknown call id " + id, Failed: true}
	}
	if out, ok := d.settle(id, c, false); ok {
		return out
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-c.done:
			out, _ := d.settle(id, c, false)
			return out
		case m := <-d.inbox:
			if !wakesInline(m) {
				continue
			}
		case <-timer.C:
		case <-ctx.Done():
		}
		out, _ := d.settle(id, c, true)
		return out
	}
}

// settle checks under the lock whether the call has finished. When it has,
// settle returns the real result and true. Otherwise it returns the
// placeholder and false. When hand is true, the call is also marked as
// background. Each finish checks the same flag under the same lock, so a
// result is either returned inline or published on the bus, never lost.
func (d *dispatcher) settle(id string, c *dispatchCall, hand bool) (dispatchOutcome, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	select {
	case <-c.done:
		delete(d.calls, id)
		return dispatchOutcome{Text: c.text, Failed: c.failed}, true
	default:
	}
	if hand {
		c.background = true
	}
	return dispatchOutcome{Text: placeholderPrefix + id, Background: true}, false
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
// once. The wait tool is not handled here, because it blocks by design.
func timeoutFor(toolName string) time.Duration {
	switch toolName {
	case "subagent", "ask_parent":
		return 0
	case "bash", "lua":
		return 60 * time.Second
	default:
		return 2500 * time.Millisecond
	}
}
