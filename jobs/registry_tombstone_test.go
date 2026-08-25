package jobs

// B3(a): once a terminal SUBAGENT job is pruned from the registry's main
// jobs map (pruneTerminalLocked, past maxRetainedTerminalJobs), its id and
// full result must remain retrievable via Wait — via a small, separately
// bounded tombstone pool (see tombstoneCap's doc comment in registry.go) —
// instead of "unknown job_id". BASH jobs are deliberately never tombstoned
// (see the same doc comment for why), and the two pools must not be able to
// evict each other.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// runAndWaitDone starts a job of the given kind that immediately returns
// result, waits for it to finish, and returns its id.
func runAndWaitDone(t *testing.T, r *Registry, kind Kind, label, result string) string {
	t.Helper()
	job := r.Start(context.Background(), label, kind, "", func(context.Context, string) (string, bool, error) {
		return result, false, nil
	})
	if _, ok := r.Wait(context.Background(), job.ID, 5*time.Second); !ok {
		t.Fatalf("job %q not found while waiting for it to finish", label)
	}
	return job.ID
}

func TestPruneTerminalLocked_TombstonesSubagentResultsPastCap(t *testing.T) {
	r := NewRegistry()

	total := maxRetainedTerminalJobs + 5
	var ids []string
	for i := 0; i < total; i++ {
		ids = append(ids, runAndWaitDone(t, r, KindSubagent, fmt.Sprintf("sub %d", i), fmt.Sprintf("subagent result %d", i)))
	}

	oldest := ids[0]
	if _, ok := r.Get(oldest); ok {
		t.Fatalf("expected the oldest subagent job to have been pruned from the live map")
	}

	got, ok := r.Wait(context.Background(), oldest, time.Second)
	if !ok {
		t.Fatalf("expected Wait to still find a pruned subagent job via its tombstone, got ok=false")
	}
	if got.Status != StatusDone {
		t.Fatalf("expected tombstoned job status done, got %s", got.Status)
	}
	if got.Result != "subagent result 0" {
		t.Fatalf("expected the tombstone to carry the job's real result, got %q", got.Result)
	}
}

func TestPruneTerminalLocked_ExactlyAtCap_NothingPrunedOrTombstoned(t *testing.T) {
	r := NewRegistry()

	var ids []string
	for i := 0; i < maxRetainedTerminalJobs; i++ {
		ids = append(ids, runAndWaitDone(t, r, KindSubagent, fmt.Sprintf("sub %d", i), "ok"))
	}

	for _, id := range ids {
		if _, ok := r.Get(id); !ok {
			t.Fatalf("job %s should still be live in the main map at exactly the cap", id)
		}
	}

	r.mu.Lock()
	tombstones := len(r.tombstones)
	r.mu.Unlock()
	if tombstones != 0 {
		t.Fatalf("expected zero tombstones at exactly the cap, got %d", tombstones)
	}
}

func TestPruneTerminalLocked_BashJobsAreNeverTombstoned(t *testing.T) {
	r := NewRegistry()

	total := maxRetainedTerminalJobs + 5
	var ids []string
	for i := 0; i < total; i++ {
		ids = append(ids, runAndWaitDone(t, r, KindBash, fmt.Sprintf("bash %d", i), "output"))
	}

	oldest := ids[0]
	if _, ok := r.Get(oldest); ok {
		t.Fatalf("expected the oldest bash job to have been pruned from the live map")
	}
	// Unlike the subagent case above, a pruned bash job must stay unknown —
	// bash jobs are deliberately excluded from tombstoning (see
	// tombstoneCap's doc comment).
	if _, ok := r.Wait(context.Background(), oldest, time.Second); ok {
		t.Fatalf("expected a pruned bash job to remain unknown; bash jobs must never be tombstoned")
	}

	r.mu.Lock()
	tombstones := len(r.tombstones)
	r.mu.Unlock()
	if tombstones != 0 {
		t.Fatalf("expected zero tombstones from bash-only pruning, got %d", tombstones)
	}
}

// TestPruneTerminalLocked_BashAndSubagentPruningDoNotEvictEachOther proves
// the two pools are actually independent: a large burst of pruned bash jobs
// (which never create tombstones) must not be able to push a genuinely
// tombstoned subagent result out early, and vice versa is structurally
// impossible since bash jobs are never tombstoned at all.
func TestPruneTerminalLocked_BashAndSubagentPruningDoNotEvictEachOther(t *testing.T) {
	r := NewRegistry()

	// Prune a subagent job first, so it is tombstoned.
	for i := 0; i < maxRetainedTerminalJobs+1; i++ {
		runAndWaitDone(t, r, KindSubagent, fmt.Sprintf("sub-%d", i), "ok")
	}
	// The very first subagent job (index 0) is now pruned+tombstoned; confirm it.
	// (We don't have its id handy here, so redo the same shape but capture ids.)
	r2 := NewRegistry()
	var subIDs []string
	for i := 0; i < maxRetainedTerminalJobs+1; i++ {
		subIDs = append(subIDs, runAndWaitDone(t, r2, KindSubagent, fmt.Sprintf("sub2-%d", i), "subagent survives"))
	}
	tombstonedSubID := subIDs[0]
	if _, ok := r2.Wait(context.Background(), tombstonedSubID, time.Second); !ok {
		t.Fatalf("expected the pruned subagent job to be tombstoned")
	}

	// Now push a large burst of BASH job prunings through the SAME registry.
	// None of these create tombstones, so they must not disturb the
	// subagent tombstone recorded above.
	for i := 0; i < tombstoneCap*2; i++ {
		runAndWaitDone(t, r2, KindBash, fmt.Sprintf("bash2-%d", i), "output")
	}

	got, ok := r2.Wait(context.Background(), tombstonedSubID, time.Second)
	if !ok {
		t.Fatalf("a burst of pruned bash jobs evicted an unrelated subagent tombstone")
	}
	if got.Result != "subagent survives" {
		t.Fatalf("tombstoned subagent result corrupted: got %q", got.Result)
	}
}

func TestPruneTerminalLocked_TombstoneCapEvictsOldestSubagentTombstone(t *testing.T) {
	r := NewRegistry()

	total := maxRetainedTerminalJobs + tombstoneCap + 5
	var ids []string
	for i := 0; i < total; i++ {
		ids = append(ids, runAndWaitDone(t, r, KindSubagent, fmt.Sprintf("sub %d", i), fmt.Sprintf("result %d", i)))
	}

	r.mu.Lock()
	tombstones := len(r.tombstones)
	r.mu.Unlock()
	if tombstones > tombstoneCap {
		t.Fatalf("expected at most %d tombstones, got %d", tombstoneCap, tombstones)
	}

	// The very earliest pruned job's tombstone must have aged out by now.
	if _, ok := r.Wait(context.Background(), ids[0], time.Second); ok {
		t.Fatalf("expected the oldest tombstone to have been evicted past tombstoneCap")
	}
	// A job pruned recently enough to still be within tombstoneCap of the
	// live/terminal boundary must still be retrievable.
	recentlyPrunedIdx := total - maxRetainedTerminalJobs - 1
	recentlyPruned := ids[recentlyPrunedIdx]
	got, ok := r.Wait(context.Background(), recentlyPruned, time.Second)
	if !ok {
		t.Fatalf("expected a recently pruned subagent job's tombstone to still be retrievable")
	}
	if got.Result != fmt.Sprintf("result %d", recentlyPrunedIdx) {
		t.Fatalf("tombstoned result mismatch: got %q", got.Result)
	}
}

func TestPruneTerminalLocked_LiveSubagentJobNeverPrunedOrTombstoned(t *testing.T) {
	r := NewRegistry()

	block := make(chan struct{})
	defer close(block)
	live := r.Start(context.Background(), "long runner", KindSubagent, "", func(context.Context, string) (string, bool, error) {
		<-block
		return "", false, nil
	})

	total := maxRetainedTerminalJobs + 10
	for i := 0; i < total; i++ {
		runAndWaitDone(t, r, KindSubagent, fmt.Sprintf("sub %d", i), "ok")
	}

	if _, ok := r.Get(live.ID); !ok {
		t.Fatalf("a still-running subagent job was pruned from the live map")
	}
	r.mu.Lock()
	_, tombstoned := r.tombstones[live.ID]
	r.mu.Unlock()
	if tombstoned {
		t.Fatalf("a still-running job must never be tombstoned")
	}
}

// TestPruneTerminalLocked_TombstoneTruncatesLongResultAndIsUTF8Safe pins
// F2: a tombstone's string fields (Result above all — the whole point of
// tombstoning a subagent job) are NOT retained in full without limit — that
// would make tombstoneCap's "200 entries" comment a lie the moment a single
// subagent returns a large result. truncateTombstoneField caps each field
// at tombstoneFieldRuneCap runes, and must never split a multi-byte rune
// (this repo has had byte-slicing truncation bugs before).
func TestPruneTerminalLocked_TombstoneTruncatesLongResultAndIsUTF8Safe(t *testing.T) {
	r := NewRegistry()

	// Build a result whose rune count is well past the cap, ending on a
	// multi-byte rune sequence (emoji, 4 bytes each in UTF-8) right at the
	// truncation boundary — a byte-offset slice here would produce invalid
	// UTF-8; a rune-offset slice must not.
	var b strings.Builder
	for i := 0; i < tombstoneFieldRuneCap+500; i++ {
		b.WriteRune('🎉')
	}
	longResult := b.String()

	total := maxRetainedTerminalJobs + 1
	var ids []string
	for i := 0; i < total; i++ {
		result := "short"
		if i == 0 {
			result = longResult
		}
		ids = append(ids, runAndWaitDone(t, r, KindSubagent, fmt.Sprintf("sub %d", i), result))
	}

	got, ok := r.Wait(context.Background(), ids[0], time.Second)
	if !ok {
		t.Fatalf("expected the tombstoned job to still be retrievable")
	}
	if !utf8.ValidString(got.Result) {
		t.Fatalf("tombstoned Result is not valid UTF-8 — truncation split a multi-byte rune: %q", got.Result)
	}
	runeCount := utf8.RuneCountInString(got.Result)
	// +1 for the trailing ellipsis rune truncateTombstoneField appends.
	if runeCount > tombstoneFieldRuneCap+1 {
		t.Fatalf("tombstoned Result has %d runes, want at most %d (+ellipsis)", runeCount, tombstoneFieldRuneCap+1)
	}
	if !strings.HasSuffix(got.Result, "…") {
		t.Errorf("expected a truncated tombstoned Result to end with an ellipsis marker, got suffix %q", got.Result[len(got.Result)-10:])
	}
}
