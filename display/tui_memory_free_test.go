package display

import (
	"testing"
	"time"
)

// countReleaseOSMemory replaces releaseOSMemory with a counter for the test.
// The real call starts a goroutine, so the test counts calls instead.
func countReleaseOSMemory(t *testing.T) *int {
	t.Helper()
	calls := 0
	old := releaseOSMemory
	releaseOSMemory = func() { calls++ }
	t.Cleanup(func() { releaseOSMemory = old })
	return &calls
}

// TestFreeOSMemory_StreamedDeltasDoNotForceScavenge sends 100 streamed text
// deltas, each followed by a flush check, to a model whose older blocks are
// already flushed. A streamed delta alone must not force a scavenge. The flush
// checks may force at most one, because the interval has not passed.
func TestFreeOSMemory_StreamedDeltasDoNotForceScavenge(t *testing.T) {
	calls := countReleaseOSMemory(t)
	m := newWheelBenchModel(t, 500)
	*calls = 0
	m.lastFreeOSMemory = time.Time{}

	for i := 0; i < 100; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "streamed token "})
		m.maybeFlushOldBlocks()
	}
	if *calls > 1 {
		t.Fatalf("100 streamed deltas forced %d scavenges, want at most 1", *calls)
	}
}

// TestFreeOSMemory_AtMostOncePerInterval checks the rate limit with a fake
// clock: the stored time of the last call is moved back instead of waiting.
func TestFreeOSMemory_AtMostOncePerInterval(t *testing.T) {
	calls := countReleaseOSMemory(t)
	var m TuiModel

	m.maybeFreeOSMemory()
	m.maybeFreeOSMemory()
	if *calls != 1 {
		t.Fatalf("two calls in one interval forced %d scavenges, want 1", *calls)
	}

	m.lastFreeOSMemory = time.Now().Add(-freeOSMemoryInterval + time.Second)
	m.maybeFreeOSMemory()
	if *calls != 1 {
		t.Fatalf("call 9 s after the last one forced a scavenge, want none")
	}

	m.lastFreeOSMemory = time.Now().Add(-freeOSMemoryInterval)
	m.maybeFreeOSMemory()
	if *calls != 2 {
		t.Fatalf("calls = %d after the interval passed, want 2", *calls)
	}
}
