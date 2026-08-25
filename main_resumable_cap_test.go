package main

// B3(b): the resumable map (this file's resumableMu/resumable/resumableOrder)
// used to grow forever — every finished async job/btw conversation that ever
// ran stayed in memory, full transcript and model client included, for the
// lifetime of the process. stashResumable now bounds it to resumableCap,
// evicting the oldest entries first, while never pruning an entry whose job
// JobRegistry still reports running.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/jobs"
)

// resetResumableForTest snapshots resumable/resumableOrder, clears them for
// the test, and restores the originals on cleanup — so this file's tests
// don't leak entries into (or pick up entries left by) any other test in
// this package that also stashes a resumable conversation.
func resetResumableForTest(t *testing.T) {
	t.Helper()
	resumableMu.Lock()
	origMap, origOrder := resumable, resumableOrder
	resumable = map[string]resumableEntry{}
	resumableOrder = nil
	resumableMu.Unlock()

	t.Cleanup(func() {
		resumableMu.Lock()
		resumable, resumableOrder = origMap, origOrder
		resumableMu.Unlock()
	})
}

// fakeResumableEntry builds a minimal resumableEntry good enough to stash —
// nothing in these tests ever actually runs agent.Run against it.
func fakeResumableEntry() resumableEntry {
	return resumableEntry{
		msgs: []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}},
		mc:   nil,
		cfg:  agent.Config{},
	}
}

func TestStashResumable_BoundsMapSizePastCap(t *testing.T) {
	resetResumableForTest(t)

	total := resumableCap + 50
	for i := 0; i < total; i++ {
		stashResumable(fmt.Sprintf("job-cap-%d", i), fakeResumableEntry())
	}

	resumableMu.Lock()
	size := len(resumable)
	orderLen := len(resumableOrder)
	resumableMu.Unlock()

	if size > resumableCap {
		t.Fatalf("resumable grew past its cap: %d entries, want at most %d", size, resumableCap)
	}
	if orderLen != size {
		t.Fatalf("resumableOrder (len %d) drifted from resumable (len %d)", orderLen, size)
	}
}

func TestStashResumable_ExactlyAtCap_NothingPruned(t *testing.T) {
	resetResumableForTest(t)

	var ids []string
	for i := 0; i < resumableCap; i++ {
		id := fmt.Sprintf("job-exact-%d", i)
		ids = append(ids, id)
		stashResumable(id, fakeResumableEntry())
	}

	resumableMu.Lock()
	size := len(resumable)
	resumableMu.Unlock()
	if size != resumableCap {
		t.Fatalf("expected exactly %d entries at exactly the cap, got %d", resumableCap, size)
	}
	for _, id := range ids {
		resumableMu.Lock()
		_, ok := resumable[id]
		resumableMu.Unlock()
		if !ok {
			t.Fatalf("entry %q was pruned even though the map was never over cap", id)
		}
	}
}

func TestStashResumable_EvictsOldestFirst(t *testing.T) {
	resetResumableForTest(t)

	total := resumableCap + 10
	var ids []string
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("job-fifo-%d", i)
		ids = append(ids, id)
		stashResumable(id, fakeResumableEntry())
	}

	// The oldest 10 must be gone; the newest resumableCap must remain.
	for i := 0; i < 10; i++ {
		resumableMu.Lock()
		_, ok := resumable[ids[i]]
		resumableMu.Unlock()
		if ok {
			t.Errorf("expected oldest entry %q to have been evicted", ids[i])
		}
	}
	for i := 10; i < total; i++ {
		resumableMu.Lock()
		_, ok := resumable[ids[i]]
		resumableMu.Unlock()
		if !ok {
			t.Errorf("expected surviving entry %q to still be present", ids[i])
		}
	}
}

func TestStashResumable_NeverPrunesEntryOfLiveJob(t *testing.T) {
	resetResumableForTest(t)

	// A genuinely running job in the real, shared JobRegistry.
	block := make(chan struct{})
	defer close(block)
	live := JobRegistry.Start(context.Background(), "still going", jobs.KindSubagent, "", func(context.Context, string) (string, bool, error) {
		<-block
		return "", false, nil
	})
	t.Cleanup(func() { JobRegistry.Cancel(live.ID) })

	// Stash the live job's entry FIRST, so plain FIFO order would otherwise
	// make it the very first candidate for eviction.
	stashResumable(live.ID, fakeResumableEntry())

	total := resumableCap + 20
	for i := 0; i < total; i++ {
		stashResumable(fmt.Sprintf("job-live-guard-%d", i), fakeResumableEntry())
	}

	resumableMu.Lock()
	_, ok := resumable[live.ID]
	resumableMu.Unlock()
	if !ok {
		t.Fatalf("resumable entry for a still-running job was pruned")
	}
}

func TestStashResumable_MapStopsGrowingAcrossManyRounds(t *testing.T) {
	resetResumableForTest(t)

	// Several rounds, each individually well past the cap, simulating a long
	// session with many resumed conversations over time.
	for round := 0; round < 5; round++ {
		for i := 0; i < resumableCap; i++ {
			stashResumable(fmt.Sprintf("job-round-%d-%d", round, i), fakeResumableEntry())
		}
		resumableMu.Lock()
		size := len(resumable)
		resumableMu.Unlock()
		if size > resumableCap {
			t.Fatalf("round %d: resumable size = %d, want at most %d", round, size, resumableCap)
		}
	}
}

// TestStashResumable_ResumingTwiceDoesNotMutateSharedEntry pins the other
// half of B1's fix from this package's side: looking up the SAME stashed
// entry twice (as jobResumerAdapter.Resume does whenever a job is resumed
// more than once) must never mutate the copy stored in the map — every
// consumer must copy before changing anything (see resumableEntry's doc
// comment).
func TestStashResumable_ResumingTwiceDoesNotMutateSharedEntry(t *testing.T) {
	resetResumableForTest(t)

	origID := "job-resumed-twice"
	entry := fakeResumableEntry()
	stashResumable(origID, entry)

	readBack := func() resumableEntry {
		resumableMu.Lock()
		defer resumableMu.Unlock()
		return resumable[origID]
	}

	before := readBack()

	// Simulate two independent "resume" lookups, each rebinding NextMessages
	// on its own local copy — exactly what jobResumerAdapter.Resume does —
	// and re-stashing under a brand-new job id, never touching origID's slot.
	for i, newID := range []string{"job-resumed-twice-a", "job-resumed-twice-b"} {
		looked := readBack()
		runCfg := looked.cfg
		runCfg.NextMessages = func() []string { return []string{fmt.Sprintf("hop-%d", i)} }
		stashResumable(newID, resumableEntry{msgs: looked.msgs, mc: looked.mc, cfg: runCfg})
	}

	after := readBack()
	if after.cfg.NextMessages != nil {
		t.Fatalf("original entry's cfg.NextMessages should remain nil (untouched); got a non-nil closure")
	}
	_ = before

	// And the two rebound copies must be independent of each other.
	resumableMu.Lock()
	a := resumable["job-resumed-twice-a"].cfg.NextMessages
	b := resumable["job-resumed-twice-b"].cfg.NextMessages
	resumableMu.Unlock()
	if a == nil || b == nil {
		t.Fatalf("expected both rebound copies to carry their own NextMessages closure")
	}
	gotA := a()
	gotB := b()
	if len(gotA) != 1 || gotA[0] != "hop-0" {
		t.Errorf("hop A closure returned %v, want [hop-0]", gotA)
	}
	if len(gotB) != 1 || gotB[0] != "hop-1" {
		t.Errorf("hop B closure returned %v, want [hop-1]", gotB)
	}
}

// TestStashResumable_ConcurrentStashes_RaceFree exercises stashResumable
// from many goroutines at once under `-race` — stashResumable is on the
// hot path of every real resume/fork/promotion re-stash, several of which
// can genuinely run concurrently (see TestWiring_B1_ConcurrentResumesOfSameJobDoNotCrossTalk).
func TestStashResumable_ConcurrentStashes_RaceFree(t *testing.T) {
	resetResumableForTest(t)

	done := make(chan struct{})
	const goroutines = 8
	const perGoroutine = 200
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			for i := 0; i < perGoroutine; i++ {
				stashResumable(fmt.Sprintf("job-concurrent-%d-%d", g, i), fakeResumableEntry())
			}
			done <- struct{}{}
		}(g)
	}
	deadline := time.After(10 * time.Second)
	for i := 0; i < goroutines; i++ {
		select {
		case <-done:
		case <-deadline:
			t.Fatal("concurrent stashes did not finish within 10s")
		}
	}

	resumableMu.Lock()
	size := len(resumable)
	resumableMu.Unlock()
	if size > resumableCap {
		t.Fatalf("resumable grew past its cap under concurrent stashes: %d, want at most %d", size, resumableCap)
	}
}
