package bus

import "sync"

// queue holds the waiting messages of one subscription. Durable messages
// form a FIFO with no cap. Latest messages keep the newest message per key,
// in the order in which their keys first became pending. This is the
// coalescing rule of eventbus/coalesce.go.
type queue struct {
	mu      sync.Mutex
	closed  bool
	durable []Message
	order   []latestKey
	latest  map[latestKey]Message
	// ready holds at most one signal. It is set while messages wait.
	ready chan struct{}
}

// latestKey keeps the keys of two kinds apart, so that one kind never
// replaces a message of another kind.
type latestKey struct {
	kind Kind
	key  string
}

func newQueue() *queue {
	return &queue{
		latest: make(map[latestKey]Message),
		ready:  make(chan struct{}, 1),
	}
}

// put adds m. key is used only for Latest. put never blocks. It does
// nothing on a closed queue.
func (q *queue) put(m Message, class Class, key string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	switch class {
	case Durable:
		q.durable = append(q.durable, m)
	case Latest:
		q.putLatest(latestKey{kind: m.Kind, key: key}, m)
	}
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

// putLatest stores m for k. An older message never replaces a newer one.
func (q *queue) putLatest(k latestKey, m Message) {
	old, ok := q.latest[k]
	if !ok {
		q.order = append(q.order, k)
	} else if m.Seq <= old.Seq {
		return
	}
	q.latest[k] = m
}

// drain returns the waiting messages, Durable first, and clears the queue.
// It also clears a pending ready signal.
func (q *queue) drain() []Message {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.takeReady()
	if len(q.durable) == 0 && len(q.order) == 0 {
		return nil
	}
	out := make([]Message, 0, len(q.durable)+len(q.order))
	out = append(out, q.durable...)
	for _, k := range q.order {
		out = append(out, q.latest[k])
	}
	q.reset()
	return out
}

// close discards the queue, clears a pending ready signal and stops put.
func (q *queue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.takeReady()
	q.reset()
}

// takeReady clears a pending ready signal. It never blocks. The caller
// holds q.mu.
func (q *queue) takeReady() {
	select {
	case <-q.ready:
	default:
	}
}

// reset empties the queue. The caller holds q.mu.
func (q *queue) reset() {
	q.durable = nil
	q.order = nil
	clear(q.latest)
}
