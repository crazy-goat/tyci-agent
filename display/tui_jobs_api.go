package display

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/decodo/tyci/eventbus"
	"github.com/decodo/tyci/jobs"
)

// SetJobEventBus subscribes the TUI to bus's "job.updated" topic (see
// tools.SetJobEventBus, the producer side) so background subagent jobs show
// up in the jobs panel/modal without the TUI polling jobs.Registry itself.
// Safe to call with bus == nil (no-op): a caller that never wires a bus gets
// today's behavior — no jobs panel, ever.
//
// The subscription coalesces per job ID (eventbus.SubscribeCoalesced):
// prog.Send blocks while the Bubble Tea loop is busy, and a plain bounded
// subscription would then drop events once its buffer filled — including a
// job's terminal one, leaving it shown as running forever (#113). Coalesced,
// a busy TUI skips intermediate updates but always gets each job's latest
// state.
//
// The subscriber goroutine exits when t.done closes (program shutdown) or
// when bus.Close() ends the subscription, whichever comes first; it never
// blocks program exit.
func (t *TUI) SetJobEventBus(bus *eventbus.Bus) {
	if bus == nil {
		return
	}
	sub, unsubscribe := bus.SubscribeCoalesced("job.updated", jobEventKey)
	go func() {
		defer unsubscribe()
		forwardJobUpdates(sub, t.done, t.prog.Send)
	}()
}

// jobEventKey coalesces "job.updated" events by job ID. Payloads that are
// not a jobs.Job share one key; forwardJobUpdates ignores them anyway.
func jobEventKey(evt eventbus.Event) string {
	if j, ok := evt.Payload.(jobs.Job); ok {
		return j.ID
	}
	return ""
}

// forwardJobUpdates sends each coalesced job snapshot to send until done
// closes or the subscription ends. Events still pending when the
// subscription ends are delivered first, as a closed channel's buffer
// would be.
func forwardJobUpdates(sub *eventbus.Coalesced, done <-chan struct{}, send func(tea.Msg)) {
	deliver := func() {
		for _, evt := range sub.Drain() {
			if j, ok := evt.Payload.(jobs.Job); ok {
				send(tuiMsgJobUpdate{Job: j})
			}
		}
	}
	for {
		select {
		case <-sub.Ready():
			deliver()
		case <-sub.Done():
			deliver()
			return
		case <-done:
			return
		}
	}
}
