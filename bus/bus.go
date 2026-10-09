// Package bus is a typed, in-process message bus. Messages carry JSON
// payloads, so they can later cross a process boundary. A subscriber takes
// its messages with Drain. Publish never blocks on a subscriber.
package bus

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"
)

// ErrClosed is returned by Publish after Bus.Close.
var ErrClosed = errors.New("bus: closed")

// ErrBroadcastNotDown is returned by Publish for a subtree broadcast that
// is not allowed. An agent may broadcast only to its own subtree. The
// orchestrator may broadcast to any subtree.
var ErrBroadcastNotDown = errors.New("bus: broadcast is allowed only down the tree")

// Option configures a Bus in New.
type Option func(*Bus)

// WithTree sets the agent hierarchy that the bus uses for subtree broadcast
// and for the rewrite of messages to agents that are not live.
func WithTree(t Tree) Option {
	return func(b *Bus) { b.tree = t }
}

// Bus routes published messages to the matching subscriptions.
type Bus struct {
	mu     sync.Mutex
	seq    uint64
	closed bool
	tree   Tree
	subs   []*Sub
}

// New returns an open bus with no subscriptions.
func New(opts ...Option) *Bus {
	b := &Bus{}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Subscribe adds a subscription called name for the messages that match f.
// On a closed bus the subscription gets no messages.
func (b *Bus) Subscribe(name string, f Filter) *Sub {
	s := &Sub{bus: b, name: name, filter: f, q: newQueue()}

	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.subs = append(b.subs, s)
	}
	return s
}

// Close stops the bus. Publish returns ErrClosed afterwards. Subscriptions
// can still Drain the messages they hold. Close can be called more than
// once.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
}

// unsubscribe removes s from the routing of the bus.
func (b *Bus) unsubscribe(s *Sub) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = slices.DeleteFunc(b.subs, func(x *Sub) bool { return x == s })
}

// Publish encodes payload as JSON and routes the message to the matching
// subscriptions. It returns the Seq of the message. The payload type T must
// be the type that kind was registered with.
//
// A message to an agent that is not live goes to the orchestrator with
// OriginSystem. It is not dropped. Publish returns an error, and assigns no
// Seq, when kind is not registered, when the payload type differs, when the
// payload cannot be encoded, when a subtree broadcast is not allowed, or
// when the bus is closed.
func Publish[T any](b *Bus, kind Kind, from, to Addr, origin Origin, payload T) (uint64, error) {
	info, ok := lookupKind(kind)
	if !ok {
		return 0, fmt.Errorf("bus: kind %q is not registered", kind)
	}
	if want := reflect.TypeFor[T](); info.typ != want {
		return 0, fmt.Errorf("bus: kind %q carries %v, not %v", kind, info.typ, want)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("bus: encode %q: %w", kind, err)
	}
	var key string
	if fn, ok := info.key.(func(T) string); ok {
		key = fn(payload)
	}

	m := Message{Kind: kind, From: from, To: to, Origin: origin, Payload: raw}
	return b.send(m, info.class, key)
}

// send routes m. The checks that can fail run before Seq is assigned, so a
// failed Publish uses no Seq. Seq, the recipient set and the queue appends
// happen in one critical section. A subscription that is added during a
// Publish therefore gets either every message of that Publish or none of
// them. The Tree callbacks run with bus.mu held.
func (b *Bus) send(m Message, class Class, key string) (uint64, error) {
	if m.To.Type == AddrSubtree && !mayBroadcast(m.From, m.To) {
		return 0, ErrBroadcastNotDown
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, ErrClosed
	}
	if m.To.Type == AddrAgent && !b.isLive(m.To.ID) {
		m.To = Addr{Type: AddrOrchestrator}
		m.Origin = OriginSystem
	}
	targets := b.recipients(b.subs, m)

	b.seq++
	m.Seq = b.seq
	m.At = time.Now()
	for _, s := range targets {
		s.q.put(m, class, key)
	}
	return m.Seq, nil
}

// Sub is a subscription. Its messages wait in a queue until Drain takes
// them.
type Sub struct {
	bus    *Bus
	name   string
	filter Filter
	q      *queue
}

// Filter selects the messages that a subscription receives.
type Filter struct {
	// To is the address that the subscription receives for. A message whose
	// To equals it is delivered. A subtree broadcast is delivered when To is
	// an agent below the broadcast root.
	To Addr
	// Kinds lists the kinds to receive. Empty means all kinds.
	Kinds []Kind
}

// Name returns the name that the subscription was created with.
func (s *Sub) Name() string { return s.name }

// Ready receives a value when messages are waiting that Drain has not taken
// yet. One value can stand for many messages. Drain clears a pending value,
// so Ready fires again only for messages that arrive after a Drain.
func (s *Sub) Ready() <-chan struct{} { return s.q.ready }

// Drain returns the waiting messages, Durable first in publish order, then
// the newest message per key in first-seen order. It clears the queue.
func (s *Sub) Drain() []Message { return s.q.drain() }

// Close stops the subscription. It receives no more messages, and the
// queue is discarded. Close can be called more than once.
func (s *Sub) Close() {
	s.bus.unsubscribe(s)
	s.q.close()
}
