package eventbus

import (
	"testing"
	"time"
)

func keyByPayloadID(evt Event) string {
	return evt.Payload.(item).id
}

type item struct {
	id string
	n  int
}

func waitReady(t *testing.T, c *Coalesced) {
	t.Helper()
	select {
	case <-c.Ready():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Ready")
	}
}

func TestCoalescedKeepsLatestPerKeyInFirstSeenOrder(t *testing.T) {
	b := New(1)
	defer b.Close()

	c, unsub := b.SubscribeCoalesced("topic", keyByPayloadID)
	defer unsub()

	// Far more events than the bus's buffer size: none of the latest
	// states may be lost, and Publish must not block.
	for n := 1; n <= 100; n++ {
		b.Publish("topic", item{id: "a", n: n})
		b.Publish("topic", item{id: "b", n: n})
	}
	b.Publish("topic", item{id: "c", n: 1})

	waitReady(t, c)
	got := c.Drain()
	want := []item{{"a", 100}, {"b", 100}, {"c", 1}}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i, evt := range got {
		if evt.Payload.(item) != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, evt.Payload, want[i])
		}
	}

	if again := c.Drain(); again != nil {
		t.Fatalf("second Drain returned %+v, want nothing", again)
	}

	// A key that was drained becomes pending again on its next event.
	b.Publish("topic", item{id: "a", n: 101})
	waitReady(t, c)
	if got := c.Drain(); len(got) != 1 || got[0].Payload.(item) != (item{"a", 101}) {
		t.Fatalf("after re-publish got %+v", got)
	}
}

func TestCoalescedAndPlainSubscribersShareTopic(t *testing.T) {
	b := New(4)
	defer b.Close()

	ch, unsubPlain := b.Subscribe("topic")
	defer unsubPlain()
	c, unsubCoalesced := b.SubscribeCoalesced("topic", keyByPayloadID)
	defer unsubCoalesced()

	b.Publish("topic", item{id: "a", n: 1})

	select {
	case evt := <-ch:
		if evt.Payload.(item) != (item{"a", 1}) {
			t.Fatalf("plain subscriber got %+v", evt.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("plain subscriber timed out")
	}
	waitReady(t, c)
	if got := c.Drain(); len(got) != 1 {
		t.Fatalf("coalesced subscriber got %+v", got)
	}
}

func TestCoalescedUnsubscribeClosesDoneKeepsPendingAndIsIdempotent(t *testing.T) {
	b := New(4)
	defer b.Close()

	c, unsub := b.SubscribeCoalesced("topic", keyByPayloadID)
	b.Publish("topic", item{id: "a", n: 1})
	unsub()

	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed after unsubscribe")
	}
	if got := c.Drain(); len(got) != 1 {
		t.Fatalf("event published before unsubscribe lost: %+v", got)
	}

	// Should not panic, and must not reach the ended subscription.
	unsub()
	b.Publish("topic", item{id: "a", n: 2})
	if got := c.Drain(); got != nil {
		t.Fatalf("event published after unsubscribe delivered: %+v", got)
	}
}

func TestCoalescedCloseEndsSubscriptionAndSubscribeAfterClose(t *testing.T) {
	b := New(4)
	c, _ := b.SubscribeCoalesced("topic", keyByPayloadID)

	b.Close()
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed after Bus.Close")
	}

	c2, unsub2 := b.SubscribeCoalesced("topic", keyByPayloadID)
	defer unsub2()
	select {
	case <-c2.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed for a subscription made after Bus.Close")
	}
}

func TestCoalescedWithReplacesKeepsTheEventThePredicatePrefers(t *testing.T) {
	b := New(1)
	defer b.Close()

	c, unsub := b.SubscribeCoalesced("topic", keyByPayloadID, WithReplaces(func(pending, incoming Event) bool {
		return incoming.Payload.(item).n >= pending.Payload.(item).n
	}))
	defer unsub()

	// "a" arrives out of order: n=3 is published before n=2.
	b.Publish("topic", item{id: "a", n: 1})
	b.Publish("topic", item{id: "a", n: 3})
	b.Publish("topic", item{id: "a", n: 2})
	b.Publish("topic", item{id: "b", n: 1})

	waitReady(t, c)
	got := c.Drain()
	want := []item{{"a", 3}, {"b", 1}}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i, evt := range got {
		if evt.Payload.(item) != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, evt.Payload, want[i])
		}
	}
}
