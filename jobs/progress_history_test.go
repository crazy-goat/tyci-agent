package jobs

// Item 53: report_progress overwrote a single Job.Progress string, so a
// child that reported three times left the parent only the third note —
// the sequence, which is the entire point of progress reporting, was lost.
// These tests pin the bounded-history fix: order is preserved, Progress
// still tracks the latest entry, the cap evicts the oldest and says so,
// truncation is rune-safe, Snapshot deep-copies the slice, the tombstone
// path independently bounds it, and concurrent SetProgress calls are race
// clean.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// TestSetProgress_HistoryPreservesOrder_ProgressTracksLatest fails on
// pre-fix code because Job had no ProgressHistory field at all: SetProgress
// simply overwrote Progress in place on every call, so the second and
// third notes here would have left no trace of the first and second ever
// having happened.
func TestSetProgress_HistoryPreservesOrder_ProgressTracksLatest(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})

	job := r.Start(context.Background(), "progressive", KindOther, "", func(ctx context.Context, jobID string) (string, bool, error) {
		r.SetProgress(jobID, "step one")
		r.SetProgress(jobID, "step two")
		r.SetProgress(jobID, "step three")
		<-release
		return "done", false, nil
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := getSnapshot(t, r, job.ID)
		if len(snap.ProgressHistory) == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for all three progress notes to be recorded, last snapshot: %+v", snap)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)

	final, ok := r.Wait(context.Background(), job.ID, time.Second)
	if !ok {
		t.Fatal("expected wait to find job")
	}
	want := []string{"step one", "step two", "step three"}
	if len(final.ProgressHistory) != len(want) {
		t.Fatalf("expected history %v, got %v", want, final.ProgressHistory)
	}
	for i, w := range want {
		if final.ProgressHistory[i] != w {
			t.Fatalf("history out of order: want %v, got %v", want, final.ProgressHistory)
		}
	}
	if final.Progress != "step three" {
		t.Fatalf("expected Progress to track the latest note, got %q", final.Progress)
	}
	if final.ProgressHistoryTruncated {
		t.Fatalf("expected ProgressHistoryTruncated=false when nothing was evicted")
	}
}

// TestSetProgress_CapEvictsOldestAndMarksTruncated fails on pre-fix code
// two ways at once: there was no cap (an unbounded slice would grow
// forever for a chatty child), and there was no way to tell a reader that
// anything had ever been dropped.
func TestSetProgress_CapEvictsOldestAndMarksTruncated(t *testing.T) {
	r := NewRegistry()

	total := progressHistoryCap + 7
	job := r.Start(context.Background(), "chatty", KindOther, "", func(context.Context, string) (string, bool, error) {
		return "done", false, nil
	})
	// Report far more notes than the cap retains. The job function itself
	// returns immediately above; SetProgress works on any known id
	// regardless of the job's status (see its own doc comment), so calling
	// it here after Start — rather than from inside fn — keeps this test
	// deterministic instead of racing the job's own completion.
	for i := 0; i < total; i++ {
		if !r.SetProgress(job.ID, fmt.Sprintf("note %d", i)) {
			t.Fatalf("SetProgress failed for note %d", i)
		}
	}

	snap := getSnapshot(t, r, job.ID)
	if len(snap.ProgressHistory) != progressHistoryCap {
		t.Fatalf("expected history capped at %d entries, got %d: %v", progressHistoryCap, len(snap.ProgressHistory), snap.ProgressHistory)
	}
	if !snap.ProgressHistoryTruncated {
		t.Fatalf("expected ProgressHistoryTruncated=true once entries were evicted")
	}
	// The oldest surviving entry must be the one that just barely avoided
	// eviction — i.e. the FIFO drop kept the newest window, not an
	// arbitrary one.
	wantOldest := fmt.Sprintf("note %d", total-progressHistoryCap)
	if snap.ProgressHistory[0] != wantOldest {
		t.Fatalf("expected oldest surviving entry %q, got %q", wantOldest, snap.ProgressHistory[0])
	}
	wantNewest := fmt.Sprintf("note %d", total-1)
	if snap.ProgressHistory[len(snap.ProgressHistory)-1] != wantNewest {
		t.Fatalf("expected newest entry %q, got %q", wantNewest, snap.ProgressHistory[len(snap.ProgressHistory)-1])
	}
	if snap.Progress != wantNewest {
		t.Fatalf("expected Progress to still track the latest note %q, got %q", wantNewest, snap.Progress)
	}
}

// TestSetProgress_TruncatesEntryRuneSafely builds a note out of a single
// multi-byte rune repeated well past progressEntryRuneCap. Deliberately a
// single 3-byte-per-rune script (CJK 日), not an alternating pattern: an
// earlier version of this test alternated a 2-byte and a 3-byte rune, a
// 5-byte period that divides evenly into 2000 (progressEntryRuneCap), so a
// buggy byte-offset slice at that exact byte count would have happened to
// land on a rune boundary anyway and this test would have passed for the
// wrong reason. 2000 is not a multiple of 3, so a byte-offset cut at byte
// 2000 is GUARANTEED to land mid-rune here — this is what actually forces
// utf8.ValidString to be the assertion doing the work, not len([]rune(...)).
// Pre-fix code had no per-entry cap at all, so this would have passed the
// raw string through unbounded.
func TestSetProgress_TruncatesEntryRuneSafely(t *testing.T) {
	r := NewRegistry()
	job := r.Start(context.Background(), "multibyte", KindOther, "", func(context.Context, string) (string, bool, error) {
		return "done", false, nil
	})

	var b strings.Builder
	for i := 0; i < progressEntryRuneCap+300; i++ {
		b.WriteRune('日')
	}
	longNote := b.String()

	if !r.SetProgress(job.ID, longNote) {
		t.Fatal("expected SetProgress to succeed")
	}
	snap := getSnapshot(t, r, job.ID)
	if len(snap.ProgressHistory) != 1 {
		t.Fatalf("expected exactly one recorded entry, got %d", len(snap.ProgressHistory))
	}
	entry := snap.ProgressHistory[0]

	if !utf8.ValidString(entry) {
		t.Fatalf("truncated progress entry is not valid UTF-8: %q", entry)
	}
	if !strings.HasSuffix(entry, "…") {
		t.Fatalf("expected an ellipsis marking the cut, got tail %q", entry[max(0, len(entry)-10):])
	}
	runeCount := utf8.RuneCountInString(entry)
	if runeCount != progressEntryRuneCap+1 { // +1 for the appended ellipsis rune
		t.Fatalf("expected %d runes (cap + ellipsis), got %d", progressEntryRuneCap+1, runeCount)
	}
	// The rune immediately before the ellipsis must be the whole rune
	// written above, never a stray continuation byte.
	beforeEllipsis := []rune(entry)[progressEntryRuneCap-1]
	if beforeEllipsis != '日' {
		t.Fatalf("rune at the truncation boundary is corrupted: %q (%U)", beforeEllipsis, beforeEllipsis)
	}
	if snap.Progress != entry {
		t.Fatalf("expected Progress to hold the same truncated text as the history's latest entry")
	}
}

// TestJobSnapshot_ProgressHistoryIsDeepCopied fails on pre-fix code because
// Snapshot's struct literal simply assigned j.ProgressHistory (a slice
// header copy), which would leave the returned snapshot aliasing the exact
// backing array SetProgress keeps mutating on the live job — the same
// aliasing bug ResidualMailbox's own deep-copy fix (batch-2 review D4)
// exists to avoid for that field.
func TestJobSnapshot_ProgressHistoryIsDeepCopied(t *testing.T) {
	j := &Job{
		ID:              "j1",
		ProgressHistory: []string{"a", "b", "c"},
	}
	snap1 := j.Snapshot()

	// Mutate the returned snapshot's slice directly.
	snap1.ProgressHistory[0] = "MUTATED"
	snap1.ProgressHistory = append(snap1.ProgressHistory, "d")

	if j.ProgressHistory[0] != "a" {
		t.Fatalf("mutating a snapshot's ProgressHistory affected the live job's own slice: %v", j.ProgressHistory)
	}
	if len(j.ProgressHistory) != 3 {
		t.Fatalf("appending to a snapshot's ProgressHistory affected the live job's own slice length: %v", j.ProgressHistory)
	}

	snap2 := j.Snapshot()
	if snap2.ProgressHistory[0] != "a" || len(snap2.ProgressHistory) != 3 {
		t.Fatalf("a later snapshot was affected by mutating an earlier one: %v", snap2.ProgressHistory)
	}
}

// TestTombstoneLocked_BoundsWholeProgressHistoryIndependently constructs a
// snapshot with far more entries than progressHistoryCap and one oversized,
// multi-byte entry, then tombstones it directly. Before truncateTombstone-
// ProgressHistory existed, tombstoneLocked never touched ProgressHistory at
// all, so a tombstoned job — retained for the lifetime of the process —
// would carry an arbitrarily large history rather than the bounded one the
// rest of tombstoneLocked promises for every other field.
func TestTombstoneLocked_BoundsWholeProgressHistoryIndependently(t *testing.T) {
	r := NewRegistry()

	var history []string
	for i := 0; i < progressHistoryCap+50; i++ {
		history = append(history, fmt.Sprintf("note %d", i))
	}
	// Also push one entry well past the per-field rune cap, ending on a
	// multi-byte rune, to check tombstoneLocked's own truncation of each
	// surviving entry (not just the count).
	var b strings.Builder
	for i := 0; i < tombstoneFieldRuneCap+50; i++ {
		b.WriteRune('🎉')
	}
	history[len(history)-1] = b.String()

	snap := Job{ID: "tombstoned-progress", Status: StatusDone, Kind: KindSubagent, ProgressHistory: history}

	r.mu.Lock()
	r.tombstoneLocked(snap)
	got := r.tombstones[snap.ID]
	r.mu.Unlock()

	if len(got.ProgressHistory) != progressHistoryCap {
		t.Fatalf("expected tombstoned history capped at %d entries, got %d", progressHistoryCap, len(got.ProgressHistory))
	}
	last := got.ProgressHistory[len(got.ProgressHistory)-1]
	if !utf8.ValidString(last) {
		t.Fatalf("tombstoned progress entry is not valid UTF-8: %q", last)
	}
	if utf8.RuneCountInString(last) > tombstoneFieldRuneCap+1 {
		t.Fatalf("tombstoned progress entry exceeds tombstoneFieldRuneCap+ellipsis: %d runes", utf8.RuneCountInString(last))
	}
	if !strings.HasSuffix(last, "…") {
		t.Fatalf("expected the oversized tombstoned entry to end in an ellipsis marking the cut")
	}
	// The surviving window must be the newest entries, same FIFO rule
	// SetProgress's own live cap follows.
	wantOldest := fmt.Sprintf("note %d", (progressHistoryCap+50)-progressHistoryCap)
	if got.ProgressHistory[0] != wantOldest {
		t.Fatalf("expected oldest surviving tombstoned entry %q, got %q", wantOldest, got.ProgressHistory[0])
	}
}

// TestSetProgress_UnknownID_ReturnsFalse is a focused regression alongside
// the history changes above: SetProgress must still fail cleanly, without
// touching any history, for an id the registry has never seen.
func TestSetProgress_UnknownID_ReturnsFalse(t *testing.T) {
	r := NewRegistry()
	if r.SetProgress("no-such-job", "x") {
		t.Fatal("expected SetProgress to return false for an unknown id")
	}
}

// TestSetProgress_ConcurrentCallsAreRaceFree drives many goroutines calling
// SetProgress on the same live job at once — the scenario -race exists to
// catch: history append/eviction and the truncated flag are all read-modify-
// write operations on the same *Job, and the only thing making them safe is
// holding r.mu for the whole operation in SetProgress.
func TestSetProgress_ConcurrentCallsAreRaceFree(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job := r.Start(context.Background(), "concurrent", KindOther, "", func(context.Context, string) (string, bool, error) {
		<-release
		return "done", false, nil
	})

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			r.SetProgress(job.ID, fmt.Sprintf("concurrent note %d", i))
		}(i)
	}
	wg.Wait()
	close(release)

	final, ok := r.Wait(context.Background(), job.ID, time.Second)
	if !ok {
		t.Fatal("expected wait to find job")
	}
	if len(final.ProgressHistory) != progressHistoryCap {
		t.Fatalf("expected history capped at %d entries after %d concurrent calls, got %d", progressHistoryCap, n, len(final.ProgressHistory))
	}
	if !final.ProgressHistoryTruncated {
		t.Fatal("expected ProgressHistoryTruncated=true after more calls than the cap")
	}
	if final.Progress == "" {
		t.Fatal("expected Progress to hold one of the concurrent notes")
	}
}

// getSnapshot fetches a fresh, lock-safe Job snapshot for id via the
// registry's own List(), rather than Get() (which hands back the live
// *Job pointer, unsafe to read outside r.mu while other goroutines can
// still be calling SetProgress on it).
func getSnapshot(t *testing.T, r *Registry, id string) Job {
	t.Helper()
	for _, j := range r.List() {
		if j.ID == id {
			return j
		}
	}
	t.Fatalf("job %s not found", id)
	return Job{}
}
