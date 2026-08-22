package agent

import (
	"strings"
	"testing"
)

// TestBuildJobReminder_TellsModelToRelayNotInvent guards the wording fix:
// the reminder used to instruct the model to "Answer anything marked
// WAITING FOR ANSWER now with answer(...)" unconditionally — which is
// exactly how the model came to invent answers on the user's behalf and
// then honestly (and wrongly) report "we answered". It must now tell the
// model to relay the question to the user, and reserve calling answer()
// itself for when it already knows the answer.
func TestBuildJobReminder_TellsModelToRelayNotInvent(t *testing.T) {
	got := buildJobReminder([]string{"WAITING FOR ANSWER: some job (job_id=job-1-1) asks: \"which way?\""}, true)

	if !strings.Contains(got, "relay") {
		t.Fatalf("expected the reminder to tell the model to relay the question to the user, got %q", got)
	}
	if !strings.Contains(got, "/answer") {
		t.Fatalf("expected the reminder to point at the /answer command, got %q", got)
	}
	if strings.Contains(got, "Answer anything marked WAITING FOR ANSWER now with answer(") {
		t.Fatalf("expected the old unconditional instruction to be gone, got %q", got)
	}
}

// TestBuildJobReminder_NonInteractiveDoesNotPromiseAnswerCommand guards the
// item-27 round-3 fix: `tyci run` (and cron, which shells out to it) wires
// PendingJobs the same as console/TUI, but has no "/answer" command and no
// human present to type it. Telling the model to "relay to the user...they
// can reply with /answer" there describes a control that does not exist and
// would make the model wait for a reply that will never come. The
// non-interactive wording must not mention /answer or tell the model a user
// will answer — it must be reachable in a call it can actually take: answer
// it itself if it already knows, or accept it goes unanswered.
func TestBuildJobReminder_NonInteractiveDoesNotPromiseAnswerCommand(t *testing.T) {
	got := buildJobReminder([]string{"WAITING FOR ANSWER: some job (job_id=job-1-1) asks: \"which way?\""}, false)

	if strings.Contains(got, "/answer") {
		t.Fatalf("non-interactive reminder must not point the model at /answer (no user can type it here), got %q", got)
	}
	if strings.Contains(got, "relay the question to the user") {
		t.Fatalf("non-interactive reminder must not tell the model to relay to a user who isn't there, got %q", got)
	}
	if !strings.Contains(got, "answer(job_id=") {
		t.Fatalf("expected the reminder to still offer answer(job_id=...) for when the model already knows, got %q", got)
	}
	if !strings.Contains(got, "unanswered") {
		t.Fatalf("expected the reminder to tell the model it may finish without an answer, got %q", got)
	}
}
