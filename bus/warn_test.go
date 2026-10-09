package bus

import (
	"runtime"
	"testing"
)

func TestKinds_ClassTable(t *testing.T) {
	want := map[Kind]Class{
		KindNoticeCompletion: Durable,
		KindAskRequest:       Durable,
		KindAgentMessage:     Durable,
		KindBtwAnswer:        Durable,
	}
	for kind, class := range want {
		info, ok := lookupKind(kind)
		if !ok {
			t.Errorf("kind %q is not registered", kind)
			continue
		}
		if info.class != class {
			t.Errorf("kind %q has class %d, want %d", kind, info.class, class)
		}
	}
}

// checkWarning fails unless msgs holds exactly one notice with text want,
// addressed to the orchestrator or to the agent parent.
func checkWarning(t *testing.T, msgs []Message, to Addr, want string) {
	t.Helper()
	if len(msgs) != 1 {
		t.Fatalf("got %d warnings, want 1", len(msgs))
	}
	m := msgs[0]
	if m.Kind != KindNoticeCompletion || m.To != to || m.Origin != OriginSystem {
		t.Fatalf("warning = %+v, want kind %q to %+v", m, KindNoticeCompletion, to)
	}
	c, err := Decode[Completion](m)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if c.Text != want || c.Agent != "x" {
		t.Fatalf("warning payload = %+v, want text %q for agent x", c, want)
	}
}

func TestWarnDepth_FiresOncePerCrossing(t *testing.T) {
	const want = "agent x inbox has 1024 unread messages"
	b := New()
	inbox := b.Subscribe("x", Filter{To: agent("x")})
	orch := b.Subscribe("orch", Filter{To: orchestrator, Kinds: []Kind{KindNoticeCompletion}})
	publish := func(n int) {
		t.Helper()
		for range n {
			mustPublish(t, b, kindDurable, item{ID: "a"})
		}
	}

	publish(warnDepth - 1)
	if got := orch.Drain(); got != nil {
		t.Fatalf("warning before the depth is reached: %+v", got)
	}
	publish(1)
	checkWarning(t, orch.Drain(), orchestrator, want)

	publish(warnDepth)
	if got := orch.Drain(); got != nil {
		t.Fatalf("second warning without a new crossing: %+v", got)
	}

	inbox.Drain()
	publish(warnDepth)
	checkWarning(t, orch.Drain(), orchestrator, want)
}

func TestWarnDepth_GoesToParent(t *testing.T) {
	b := New(WithTree(testTree(map[string]string{"x": "p"})))
	parent := b.Subscribe("p", Filter{To: agent("p"), Kinds: []Kind{KindNoticeCompletion}})
	orch := b.Subscribe("orch", Filter{To: orchestrator, Kinds: []Kind{KindNoticeCompletion}})
	b.Subscribe("x", Filter{To: agent("x")})

	for range warnDepth {
		mustPublish(t, b, kindDurable, item{ID: "a"})
	}
	checkWarning(t, parent.Drain(), agent("p"), "agent x inbox has 1024 unread messages")
	if got := orch.Drain(); got != nil {
		t.Fatalf("orchestrator got a warning for an agent with a parent: %+v", got)
	}
}

func TestDurable_FiftyThousandUnconsumed(t *testing.T) {
	const n = 50_000
	// A loose bound on the heap growth, to catch a leak and not a small change.
	const maxGrowth = 200 << 20

	b := New()
	s := b.Subscribe("x", Filter{To: agent("x")})
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range n {
		mustPublish(t, b, kindDurable, item{ID: "a", N: i})
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if growth := int64(after.HeapAlloc) - int64(before.HeapAlloc); growth > maxGrowth {
		t.Fatalf("heap grew by %d bytes, want at most %d", growth, maxGrowth)
	}

	got := s.Drain()
	if len(got) != n {
		t.Fatalf("Drain has %d messages, want %d", len(got), n)
	}
	// The warning at warnDepth takes one Seq, so Seq grows but has a gap.
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Fatalf("message %d has Seq %d, not after Seq %d", i, got[i].Seq, got[i-1].Seq)
		}
	}
}
