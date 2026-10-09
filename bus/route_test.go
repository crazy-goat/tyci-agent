package bus

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// testTree returns a Tree from a map of parents. An empty parent means the
// orchestrator. The agents in dead are not live.
func testTree(parents map[string]string, dead ...string) Tree {
	return Tree{
		ParentOf: func(id string) (string, bool) {
			p, ok := parents[id]
			return p, ok
		},
		IsLive: func(id string) bool {
			return !slices.Contains(dead, id)
		},
	}
}

// subscribeAll subscribes one agent subscription per id, and one for the orchestrator.
func subscribeAll(b *Bus, ids ...string) map[string]*Sub {
	subs := map[string]*Sub{"orchestrator": b.Subscribe("orchestrator", Filter{To: orchestrator})}
	for _, id := range ids {
		subs[id] = b.Subscribe(id, Filter{To: agent(id)})
	}
	return subs
}

// receivers drains every subscription and returns the names of those that
// had messages, sorted.
func receivers(subs map[string]*Sub) []string {
	var names []string
	for name, s := range subs {
		if len(s.Drain()) > 0 {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func TestRoute_DirectAddress(t *testing.T) {
	b := New(WithTree(testTree(map[string]string{"a": "", "b": ""})))
	subs := subscribeAll(b, "a", "b")

	if _, err := Publish(b, kindDurable, orchestrator, agent("a"), OriginSystem, item{ID: "y"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := receivers(subs); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("receivers = %v, want [a]", got)
	}
}

func TestRoute_SubtreeReachesDescendantsOnly(t *testing.T) {
	// orchestrator -> a -> b -> c, and d is a sibling of a.
	parents := map[string]string{"a": "", "b": "a", "c": "b", "d": ""}
	tests := []struct {
		name string
		from Addr
		to   Addr
		want []string
	}{
		{"agent a to its subtree", agent("a"), subtree("a"), []string{"b", "c"}},
		{"orchestrator to subtree a", orchestrator, subtree("a"), []string{"b", "c"}},
		{"orchestrator to the whole tree", orchestrator, subtree(""), []string{"a", "b", "c", "d"}},
		{"agent b to its subtree", agent("b"), subtree("b"), []string{"c"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := New(WithTree(testTree(parents)))
			subs := subscribeAll(b, "a", "b", "c", "d")

			if _, err := Publish(b, kindDurable, tc.from, tc.to, OriginAgent, item{ID: "x"}); err != nil {
				t.Fatalf("Publish: %v", err)
			}
			if got := receivers(subs); !slices.Equal(got, tc.want) {
				t.Fatalf("receivers = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRoute_SubtreeUpIsRejected(t *testing.T) {
	parents := map[string]string{"a": "", "b": "a"}
	tests := []struct {
		name string
		from Addr
		to   Addr
	}{
		{"child broadcasts to its parent's subtree", agent("b"), subtree("a")},
		{"agent broadcasts to the whole tree", agent("a"), subtree("")},
		{"user broadcasts to the whole tree", Addr{Type: AddrUser}, subtree("")},
		{"tui broadcasts to a subtree", Addr{Type: AddrTUI}, subtree("a")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := New(WithTree(testTree(parents)))
			subs := subscribeAll(b, "a", "b")

			_, err := Publish(b, kindDurable, tc.from, tc.to, OriginAgent, item{ID: "x"})
			if !errors.Is(err, ErrBroadcastNotDown) {
				t.Fatalf("err = %v, want ErrBroadcastNotDown", err)
			}
			if got := receivers(subs); got != nil {
				t.Fatalf("rejected broadcast reached %v", got)
			}
			if seq := mustPublish(t, b, kindDurable, item{}); seq != 1 {
				t.Fatalf("Seq after a rejected broadcast = %d, want 1", seq)
			}
		})
	}
}

func TestRoute_DeadRecipientGoesToOrchestrator(t *testing.T) {
	b := New(WithTree(testTree(map[string]string{"a": ""}, "a")))
	subs := subscribeAll(b, "a")

	seq, err := Publish(b, kindDurable, Addr{Type: AddrUser}, agent("a"), OriginHuman, item{ID: "late"})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if seq == 0 {
		t.Fatal("message to a dead agent got no Seq")
	}

	msgs := subs["orchestrator"].Drain()
	if len(msgs) != 1 {
		t.Fatalf("orchestrator got %d messages, want 1", len(msgs))
	}
	if msgs[0].To != orchestrator || msgs[0].Origin != OriginSystem {
		t.Fatalf("message = To %+v Origin %q, want orchestrator and system", msgs[0].To, msgs[0].Origin)
	}
	if got := subs["a"].Drain(); got != nil {
		t.Fatalf("dead agent got %d messages, want none", len(got))
	}
}

func TestRoute_CycleInTreeDoesNotHang(t *testing.T) {
	// a and b are each other's parent, so the walk up never reaches the
	// orchestrator.
	parents := map[string]string{"a": "b", "b": "a", "z": ""}
	done := make(chan []string)
	go func() {
		b := New(WithTree(testTree(parents)))
		subs := subscribeAll(b, "a", "b", "z")
		if _, err := Publish(b, kindDurable, orchestrator, subtree("a"), OriginSystem, item{ID: "x"}); err != nil {
			t.Errorf("Publish: %v", err)
		}
		done <- receivers(subs)
	}()

	select {
	case got := <-done:
		if !slices.Equal(got, []string{"b"}) {
			t.Fatalf("receivers = %v, want [b]", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast in a cyclic tree did not return")
	}
}
