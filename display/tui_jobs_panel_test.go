package display

import (
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// TestRunningBackgroundJobs_IncludesWaitingAnswer guards against the bug
// where a job that asked a question vanished from the always-visible inline
// panel the instant it did — StatusWaitingAnswer was filtered out along with
// the terminal statuses. Revert the fix and this fails because the waiting
// job is absent from the result.
func TestRunningBackgroundJobs_IncludesWaitingAnswer(t *testing.T) {
	m := TuiModel{backgroundJobs: map[string]jobs.Job{
		"job-1-1": {ID: "job-1-1", Status: jobs.StatusWaitingAnswer, Question: "should I proceed?", StartedAt: time.Now()},
		"job-1-2": {ID: "job-1-2", Status: jobs.StatusDone, StartedAt: time.Now()},
	}}

	got := m.runningBackgroundJobs()
	if len(got) != 1 {
		t.Fatalf("expected exactly the waiting job to remain visible, got %d jobs: %+v", len(got), got)
	}
	if got[0].ID != "job-1-1" {
		t.Fatalf("expected the waiting job, got %+v", got[0])
	}
}

// TestRunningBackgroundJobs_WaitingSortedBeforeRunning guards the ordering
// that keeps a blocked job from being pushed out of jobsPanelMaxLines by
// jobs that need nobody's attention. Revert the reordering (fall back to
// sortedBackgroundJobs' plain newest-first order) and this fails because the
// older waiting job would sort after the newer running one.
func TestRunningBackgroundJobs_WaitingSortedBeforeRunning(t *testing.T) {
	older := time.Now().Add(-time.Minute)
	newer := time.Now()
	m := TuiModel{backgroundJobs: map[string]jobs.Job{
		"job-1-1": {ID: "job-1-1", Status: jobs.StatusWaitingAnswer, Question: "q?", StartedAt: older},
		"job-1-2": {ID: "job-1-2", Status: jobs.StatusRunning, StartedAt: newer},
	}}

	got := m.runningBackgroundJobs()
	if len(got) != 2 {
		t.Fatalf("expected both jobs visible, got %d: %+v", len(got), got)
	}
	if got[0].Status != jobs.StatusWaitingAnswer {
		t.Fatalf("expected the waiting job first regardless of start time, got %+v", got[0])
	}
}

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

// TestFormatJobLine_ShowsQuestionWhenWaiting guards against the panel line
// showing only Description (which is often just the job's task, not the
// question) for a job that is blocked. Revert the fix and this fails because
// the rendered line contains the description text and not the question.
func TestFormatJobLine_ShowsQuestionWhenWaiting(t *testing.T) {
	j := jobs.Job{
		ID:          "job-1-42",
		Description: "review the auth flow",
		Status:      jobs.StatusWaitingAnswer,
		Question:    "should I also touch the tests?",
		StartedAt:   time.Now(),
	}
	line := formatJobLine(j, 200)
	if !strings.Contains(line, "should I also touch the tests?") {
		t.Fatalf("expected the panel line to show the pending question, got %q", line)
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

// TestFormatJobLine_WaitingAnswerAtNormalWidthKeepsQuestionReadable guards
// against the negative-duration suffix (27 chars of "(-2562047h47m...)")
// eating the width budget before the question is truncated, which left
// nothing useful on screen at a normal 80-column terminal. This is a
// budget check, not an exact-content check: it fails if the rendered line
// contains the garbage negative-duration text, which is what happens when
// jobDuration is reverted to its pre-fix form.
func TestFormatJobLine_WaitingAnswerAtNormalWidthKeepsQuestionReadable(t *testing.T) {
	j := jobs.Job{
		ID:        "job-1-42",
		Status:    jobs.StatusWaitingAnswer,
		Question:  "should I also touch the tests, or leave them alone for now?",
		StartedAt: time.Now(),
	}
	line := formatJobLine(j, 80)
	if strings.Contains(line, "-2562047h") {
		t.Fatalf("expected no garbage negative duration in the rendered line, got %q", line)
	}
	if !strings.Contains(line, "should I also touch") {
		t.Fatalf("expected the start of the question to survive truncation at width 80, got %q", line)
	}
}
