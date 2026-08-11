package display

import (
	"github.com/decodo/tyci/eventbus"
	"github.com/decodo/tyci/jobs"
)

// SetJobEventBus subscribes the TUI to bus's "job.updated" topic (see
// tools.SetJobEventBus, the producer side) so background subagent jobs show
// up in the jobs panel/modal without the TUI polling jobs.Registry itself.
// Safe to call with bus == nil (no-op): a caller that never wires a bus gets
// today's behavior — no jobs panel, ever.
//
// The subscriber goroutine exits when t.done closes (program shutdown) or
// when bus.Close() closes the subscription channel, whichever comes first;
// it never blocks program exit.
func (t *TUI) SetJobEventBus(bus *eventbus.Bus) {
	if bus == nil {
		return
	}
	ch, unsubscribe := bus.Subscribe("job.updated")
	go func() {
		defer unsubscribe()
		for {
			select {
			case evt, ok := <-ch:
				if !ok {
					return
				}
				if j, ok := evt.Payload.(jobs.Job); ok {
					t.prog.Send(tuiMsgJobUpdate{Job: j})
				}
			case <-t.done:
				return
			}
		}
	}()
}
