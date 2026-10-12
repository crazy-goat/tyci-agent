package bus

import (
	"slices"
	"sync"
	"testing"
	"time"
)

// states returns the delivery states of evs, in order.
func states(evs []Event) []DeliveryState {
	out := make([]DeliveryState, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.State)
	}
	return out
}

func TestEvents_SentThenReceivedAfterDrain(t *testing.T) {
	b := New()
	defer b.Close()
	sub := b.Subscribe("a", Filter{To: agent("a")})

	seq, err := Publish(b, kindDurable, orchestrator, agent("a"), OriginSystem, item{ID: "1"})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := states(b.Events()); !slices.Equal(got, []DeliveryState{Sent}) {
		t.Fatalf("states before Drain = %v, want [Sent]", got)
	}

	sub.Drain()
	evs := b.Events()
	if got := states(evs); !slices.Equal(got, []DeliveryState{Sent, Received}) {
		t.Fatalf("states after Drain = %v, want [Sent Received]", got)
	}
	if evs[1].Msg.Seq != seq {
		t.Fatalf("Received Seq = %d, want %d", evs[1].Msg.Seq, seq)
	}
}

func TestEvents_DirectToUnknownAgentFailed(t *testing.T) {
	// No subscription exists, so the message reaches nobody. The agent is
	// not live, so the bus reroutes it to the orchestrator, which has no
	// subscription either.
	b := New(WithTree(Tree{IsLive: func(string) bool { return false }}))
	defer b.Close()

	if _, err := Publish(b, kindDurable, orchestrator, agent("gone"), OriginSystem, item{ID: "1"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := states(b.Events()); !slices.Equal(got, []DeliveryState{Failed}) {
		t.Fatalf("states = %v, want [Failed]", got)
	}
}

func TestEvents_PublishErrorFailed(t *testing.T) {
	b := New()
	b.Close()

	if _, err := Publish(b, kindDurable, orchestrator, agent("a"), OriginSystem, item{ID: "1"}); err == nil {
		t.Fatal("Publish on a closed bus returned no error")
	}
	evs := b.Events()
	if got := states(evs); !slices.Equal(got, []DeliveryState{Failed}) {
		t.Fatalf("states = %v, want [Failed]", got)
	}
	if evs[0].Msg.Kind != kindDurable {
		t.Fatalf("Failed Kind = %q, want %q", evs[0].Msg.Kind, kindDurable)
	}
}

func TestEvents_BroadcastSentUntilAllDrained(t *testing.T) {
	tree := Tree{ParentOf: func(id string) (string, bool) {
		if id == "a" || id == "b" {
			return "p", true
		}
		return "", false
	}}
	b := New(WithTree(tree))
	defer b.Close()
	sa := b.Subscribe("a", Filter{To: agent("a")})
	sb := b.Subscribe("b", Filter{To: agent("b")})

	if _, err := Publish(b, kindDurable, orchestrator, subtree("p"), OriginSystem, item{ID: "1"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	sa.Drain()
	if got := states(b.Events()); !slices.Equal(got, []DeliveryState{Sent}) {
		t.Fatalf("states after one of two receivers drained = %v, want [Sent]", got)
	}

	sb.Drain()
	if got := states(b.Events()); !slices.Equal(got, []DeliveryState{Sent, Received}) {
		t.Fatalf("states after both receivers drained = %v, want [Sent Received]", got)
	}
}

func TestEvents_RingDropsOldest(t *testing.T) {
	b := New()
	defer b.Close()
	b.Subscribe("a", Filter{To: agent("a")})

	const n = observeLimit + 1
	for i := range n {
		if _, err := Publish(b, kindDurable, orchestrator, agent("a"), OriginSystem, item{N: i}); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}

	evs := b.Events()
	if len(evs) != observeLimit {
		t.Fatalf("len(Events) = %d, want %d", len(evs), observeLimit)
	}
	if evs[0].Msg.Seq != 2 {
		t.Fatalf("oldest Seq = %d, want 2 (Seq 1 dropped)", evs[0].Msg.Seq)
	}
	if last := evs[len(evs)-1].Msg.Seq; last != n {
		t.Fatalf("newest Seq = %d, want %d", last, n)
	}
}

func TestEvents_PublishDoesNotWaitForObserver(t *testing.T) {
	b := New()
	defer b.Close()
	b.Subscribe("a", Filter{To: agent("a")}) // never drained

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 1000 {
			if _, err := Publish(b, kindDurable, orchestrator, agent("a"), OriginSystem, item{N: i}); err != nil {
				t.Errorf("Publish %d: %v", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 1000 {
			_ = b.Events()
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Publish or Events did not finish: the observer blocks the bus")
	}
}
