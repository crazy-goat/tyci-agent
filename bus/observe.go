package bus

import "slices"

// observeLimit is how many events Bus.Events keeps. The oldest event goes
// first.
const observeLimit = 200

// DeliveryState is the state of a message on the bus.
type DeliveryState uint8

const (
	// Sent means the bus queued the message for at least one receiver, and
	// some receiver has not taken it yet.
	Sent DeliveryState = iota + 1
	// Received means that every receiver of the message took it with Drain.
	Received
	// Failed means that Publish returned an error, or that no subscription
	// received the message. A message to an agent that is not live gets one
	// Failed event for that agent, with Msg.To set to the agent. The bus then
	// reroutes the message to the orchestrator, and that delivery has its own
	// state: Sent, or Failed when no orchestrator subscription exists.
	Failed
)

// Event is one change of the delivery state of a message. Msg.Seq is zero
// when Publish failed before it assigned a Seq.
type Event struct {
	Msg   Message
	State DeliveryState
}

// Events returns the recent delivery events, oldest first. It keeps the last
// 200 events. It is read-only: it does not take messages away from Drain,
// and it never blocks Publish for longer than a copy of the ring.
func (b *Bus) Events() []Event {
	b.obsMu.Lock()
	defer b.obsMu.Unlock()
	return slices.Clone(b.events)
}

// observe appends ev to the ring.
func (b *Bus) observe(ev Event) {
	b.obsMu.Lock()
	defer b.obsMu.Unlock()
	b.appendLocked(ev)
}

// observeSent records that the bus queued m for n receivers. Zero receivers
// is a failure. The caller holds b.mu, so Seq is already set.
func (b *Bus) observeSent(m Message, n int) {
	b.obsMu.Lock()
	defer b.obsMu.Unlock()
	if n == 0 {
		b.appendLocked(Event{Msg: m, State: Failed})
		return
	}
	if b.pending == nil {
		b.pending = make(map[uint64]int)
	}
	b.pending[m.Seq] = n
	b.appendLocked(Event{Msg: m, State: Sent})
}

// observeReceived records that a subscription took the messages msgs. The
// last receiver of a message turns its state into Received. A message that
// Sent has already left the ring for is not tracked.
func (b *Bus) observeReceived(msgs []Message) {
	b.obsMu.Lock()
	defer b.obsMu.Unlock()
	for _, m := range msgs {
		n, ok := b.pending[m.Seq]
		if !ok {
			continue
		}
		if n > 1 {
			b.pending[m.Seq] = n - 1
			continue
		}
		delete(b.pending, m.Seq)
		b.appendLocked(Event{Msg: m, State: Received})
	}
}

// appendLocked adds ev and drops the oldest event at the limit. The caller
// holds b.obsMu. A dropped Sent event stops the tracking of its receivers.
func (b *Bus) appendLocked(ev Event) {
	if len(b.events) == observeLimit {
		if b.events[0].State == Sent {
			delete(b.pending, b.events[0].Msg.Seq)
		}
		copy(b.events, b.events[1:])
		b.events = b.events[:observeLimit-1]
	}
	b.events = append(b.events, ev)
}
