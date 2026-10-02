package eventbus

import "sync"

// Coalesced is a subscription that keeps only the latest event per key
// instead of a bounded queue. Publish stores the event in a map and never
// blocks, and a burst can never push a key's latest state out: a slow
// consumer sees fewer intermediate events, but always the last one per key.
// Use it when every event carries the full current state of something (for
// example a job snapshot) and only the newest state matters.
//
// The consumer waits on Ready, then calls Drain. Done closes when the
// subscription ends (unsubscribe or Bus.Close); events published before
// that are still returned by Drain.
type Coalesced struct {
	key func(Event) string

	mu      sync.Mutex
	order   []string
	pending map[string]Event

	ready     chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newCoalesced(key func(Event) string) *Coalesced {
	return &Coalesced{
		key:     key,
		pending: make(map[string]Event),
		ready:   make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
}

// SubscribeCoalesced subscribes to topic with per-key coalescing (see
// Coalesced). key maps an event to its identity; events with the same key
// replace each other until the next Drain.
func (b *Bus) SubscribeCoalesced(topic string, key func(Event) string) (*Coalesced, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	c := newCoalesced(key)
	if b.closed {
		c.close()
		return c, func() {}
	}
	if b.coalesced == nil {
		b.coalesced = make(map[string][]*Coalesced)
	}
	b.coalesced[topic] = append(b.coalesced[topic], c)

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.closed {
				return
			}
			subs := b.coalesced[topic]
			for i, s := range subs {
				if s == c {
					b.coalesced[topic] = append(subs[:i], subs[i+1:]...)
					break
				}
			}
			if len(b.coalesced[topic]) == 0 {
				delete(b.coalesced, topic)
			}
			c.close()
		})
	}

	return c, unsubscribe
}

// Ready receives a value after new events become pending. One signal can
// stand for many events, and a signal may find nothing left to drain (an
// earlier Drain already took it), so Drain fully and tolerate an empty result.
func (c *Coalesced) Ready() <-chan struct{} { return c.ready }

// Done closes when the subscription ends.
func (c *Coalesced) Done() <-chan struct{} { return c.done }

// Drain returns the pending events, the latest one per key, in the order
// their keys first became pending, and clears them.
func (c *Coalesced) Drain() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.order) == 0 {
		return nil
	}
	out := make([]Event, 0, len(c.order))
	for _, k := range c.order {
		out = append(out, c.pending[k])
	}
	c.order = nil
	c.pending = make(map[string]Event)
	return out
}

func (c *Coalesced) put(evt Event) {
	k := c.key(evt)

	c.mu.Lock()
	if _, ok := c.pending[k]; !ok {
		c.order = append(c.order, k)
	}
	c.pending[k] = evt
	c.mu.Unlock()

	select {
	case c.ready <- struct{}{}:
	default:
	}
}

func (c *Coalesced) close() {
	c.closeOnce.Do(func() { close(c.done) })
}
