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
	got := buildJobReminder([]string{"WAITING FOR ANSWER: some job (job_id=job-1-1) asks: \"which way?\""})

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
