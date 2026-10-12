package display

import (
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/jobs"
)

// TestJobStatusIcon_WaitingAnswerHasOwnIcon guards against
// StatusWaitingAnswer falling through to the same grey "?" default every
// unrecognized status gets. Revert the added case and this fails because the
// waiting icon becomes indistinguishable from an unknown status.
func TestJobStatusIcon_WaitingAnswerHasOwnIcon(t *testing.T) {
	icon, _ := jobStatusIcon(jobs.StatusWaitingAnswer)
	defaultIcon, _ := jobStatusIcon(jobs.Status("some-unrecognized-status"))
	if icon == defaultIcon {
		t.Fatalf("expected StatusWaitingAnswer to have its own icon, got the same default %q as an unrecognized status", icon)
	}
}

// TestJobDuration_WaitingAnswerIsNotNegative guards against the garbage
// negative duration a waiting job produced before this fix: FinishedAt is
// zero for any unfinished job, and jobDuration used to special-case only
// StatusRunning, so StatusWaitingAnswer fell into
// FinishedAt.Sub(StartedAt) against a zero FinishedAt — a duration around
// -2562047h47m. Revert the IsZero check (restore the StatusRunning-only
// special case) and this fails with exactly that negative value.
func TestJobDuration_WaitingAnswerIsNotNegative(t *testing.T) {
	j := jobs.Job{
		Status:    jobs.StatusWaitingAnswer,
		StartedAt: time.Now().Add(-3 * time.Second),
	}
	d := jobDuration(j)
	if d < 0 {
		t.Fatalf("expected a non-negative duration for a waiting job, got %s", d)
	}
}

// TestFormatJobLine_QuietTextOnlyPastThreshold guards item 25's "quiet Xs"
// signal: a RUNNING job whose LastActivity is recent (well under
// quietActivityThreshold) must render with no "quiet" text at all — a busy
// child streaming tokens every 100ms would otherwise have its jobs-panel
// line's text change on literally every render tick. Only once LastActivity
// falls behind by at least the threshold should "quiet" appear. Revert the
// threshold check in formatJobLine and this fails because a job active a
// second ago already shows "quiet".
func TestFormatJobLine_QuietTextOnlyPastThreshold(t *testing.T) {
	now := time.Now()
	fresh := jobs.Job{
		ID:           "job-1-1",
		Status:       jobs.StatusRunning,
		Description:  "streaming child",
		StartedAt:    now.Add(-30 * time.Second),
		LastActivity: now.Add(-1 * time.Second),
	}
	line := formatJobLine(fresh, 200)
	if strings.Contains(line, "quiet") {
		t.Fatalf("expected no 'quiet' text for a job active 1s ago, got %q", line)
	}

	stale := fresh
	stale.LastActivity = now.Add(-30 * time.Second)
	line = formatJobLine(stale, 200)
	if !strings.Contains(line, "quiet") {
		t.Fatalf("expected 'quiet' text for a job idle 30s, got %q", line)
	}
}

// TestFormatJobLine_QuietWordingIsNotAlarming guards item 25's explicit
// wording requirement: the panel must never describe a quiet RUNNING job as
// "hung" or "stuck" — a legitimate multi-minute test run producing no
// output is silent and fine, not a malfunction. Revert to alarming wording
// and this fails.
func TestFormatJobLine_QuietWordingIsNotAlarming(t *testing.T) {
	now := time.Now()
	j := jobs.Job{
		ID:           "job-1-1",
		Status:       jobs.StatusRunning,
		Description:  "long test run",
		StartedAt:    now.Add(-5 * time.Minute),
		LastActivity: now.Add(-3 * time.Minute),
	}
	line := formatJobLine(j, 200)
	for _, alarming := range []string{"hung", "stuck", "stalled", "dead"} {
		if strings.Contains(strings.ToLower(line), alarming) {
			t.Fatalf("expected non-alarming wording, but line contains %q: %q", alarming, line)
		}
	}
	if !strings.Contains(line, "quiet") {
		t.Fatalf("expected the quiet signal to still be present, got %q", line)
	}
}

// TestFormatJobLine_QuietOnlyForRunning guards against the quiet signal
// leaking onto non-running statuses, which already have their own status
// representation (done/failed/etc). Revert the Status check in quietSince
// and this fails because a long-finished job with a stale LastActivity
// starts showing "quiet" too.
func TestFormatJobLine_QuietOnlyForRunning(t *testing.T) {
	now := time.Now()
	j := jobs.Job{
		ID:           "job-1-1",
		Status:       jobs.StatusDone,
		Description:  "finished child",
		StartedAt:    now.Add(-5 * time.Minute),
		FinishedAt:   now.Add(-4 * time.Minute),
		LastActivity: now.Add(-4 * time.Minute),
	}
	line := formatJobLine(j, 200)
	if strings.Contains(line, "quiet") {
		t.Fatalf("expected no 'quiet' text for a non-running job, got %q", line)
	}
}

// TestQuietSince_ZeroLastActivityIsNotOk guards against a zero-value
// LastActivity (which should never happen for a job that went through
// Registry.Start, but must never be trusted blindly) rendering as some huge
// bogus duration the way the pre-fix jobDuration did for FinishedAt. Revert
// the IsZero guard in quietSince and this fails because ok becomes true with
// a multi-decade duration.
func TestQuietSince_ZeroLastActivityIsNotOk(t *testing.T) {
	j := jobs.Job{Status: jobs.StatusRunning, StartedAt: time.Now()}
	if _, ok := quietSince(j); ok {
		t.Fatalf("expected quietSince to report not-ok for a zero LastActivity")
	}
}
