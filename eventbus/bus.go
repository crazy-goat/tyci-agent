// Package eventbus provides a simple in-process pub/sub bus built on Go
// channels, with fan-out delivery and non-blocking publish.
package eventbus

import (
	"sync"
	"time"
)

type Event struct {
	Topic   string
	Payload any
	At      time.Time
}

type Bus struct {
	mu      sync.RWMutex
	subs    map[string][]chan Event
	bufSize int
	closed  bool
}

func New(bufSize int) *Bus {
	return &Bus{
		subs:    make(map[string][]chan Event),
		bufSize: bufSize,
	}
}

func (b *Bus) Subscribe(topic string) (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan Event, b.bufSize)
	if b.closed {
		close(ch)
		return ch, func() {}
	}
	b.subs[topic] = append(b.subs[topic], ch)

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.closed {
				return
			}
			chans := b.subs[topic]
			for i, c := range chans {
				if c == ch {
					b.subs[topic] = append(chans[:i], chans[i+1:]...)
					break
				}
			}
			if len(b.subs[topic]) == 0 {
				delete(b.subs, topic)
			}
			close(ch)
		})
	}

	return ch, unsubscribe
}

// Publish never blocks on a slow or stalled subscriber: a full channel means
// that subscriber simply misses this event, rather than the producer
// stalling on behalf of one consumer.
func (b *Bus) Publish(topic string, payload any) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return
	}

	chans := b.subs[topic]
	if len(chans) == 0 {
		return
	}

	evt := Event{Topic: topic, Payload: payload, At: time.Now()}
	for _, ch := range chans {
		select {
		case ch <- evt:
		default:
		}
	}
}

func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true

	for topic, chans := range b.subs {
		for _, ch := range chans {
			close(ch)
		}
		delete(b.subs, topic)
	}
}
