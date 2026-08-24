package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decodo/tyci/tools"
)

// TestIsStoppedByUser_AttributionRequiresNonHandoffCtx: a genuine
// context.Canceled in a normal (handoff-eligible) child is a kill_job stop;
// the SAME error in the no-handoff mode (AskUnroutableCtxKey stamped) is a
// run-mode cutoff, NOT "stopped by user". Revert check: drop the `&& !noHandoff`
// clause from isStoppedByUser and the second assertion flips to true.
func TestIsStoppedByUser_AttributionRequiresNonHandoffCtx(t *testing.T) {
	ctx := context.Background()
	if !isStoppedByUser(context.Canceled, ctx) {
		t.Error("context.Canceled in a normal child must be a kill_job stop")
	}

	noHandoff := context.WithValue(context.Background(), tools.AskUnroutableCtxKey{}, true)
	if isStoppedByUser(context.Canceled, noHandoff) {
		t.Error("context.Canceled in the no-handoff mode is a run-mode cutoff, not a kill_job stop — over-attribution")
	}
}

// TestIsStoppedByUser_FalseForNonCancel: only context.Canceled (not nil, not
// deadline) counts as a kill_job stop. Revert check: remove the errors.Is
// guard and a nil error would be misreported as stopped.
func TestIsStoppedByUser_FalseForNonCancel(t *testing.T) {
	ctx := context.Background()
	if isStoppedByUser(nil, ctx) {
		t.Error("a clean finish is not a kill_job stop")
	}
	if isStoppedByUser(context.DeadlineExceeded, ctx) {
		t.Error("the wall-clock deadline is not a kill_job stop")
	}
}

// TestSubagentStoppedMessage_CarriesPartialWork: a child stopped by
// kill_job keeps its partial output, annotated with the resume hint, and the
// messages sign an ErrSubagentStoppedByUser so tools/subagent.go's
// runSingleTask can surface it as a partial success. Revert check: make
// subagentStoppedMessage drop the note (return text only) and the
// "may be incomplete" + resume fragments disappear.
func TestSubagentStoppedMessage_CarriesPartialWork(t *testing.T) {
	content, err := subagentStoppedMessage("did three steps", "job-1-1")
	if !strings.Contains(content, "did three steps") {
		t.Errorf("partial content must be kept, got %q", content)
	}
	if !strings.Contains(content, "may be incomplete") {
		t.Errorf("expected an incompleteness note, got %q", content)
	}
	if !strings.Contains(content, `resume(job_id="job-1-1"`) {
		t.Errorf("expected a resume hint with the job id, got %q", content)
	}
	if !errors.Is(err, tools.ErrSubagentStoppedByUser) {
		t.Errorf("error must wrap ErrSubagentStoppedByUser, got %v", err)
	}
}

// TestSubagentStoppedMessage_EmptyTextWithJobID: no partial output but a
// resumable entry was stashed, so the message still points at resume rather
// than dead-ending. Revert check: make the empty-text branch not mention
// resume and this fails.
func TestSubagentStoppedMessage_EmptyTextWithJobID(t *testing.T) {
	_, err := subagentStoppedMessage("", "job-1-1")
	if !strings.Contains(err.Error(), "resumable") || !strings.Contains(err.Error(), `resume(job_id="job-1-1"`) {
		t.Errorf("expected a resumable pointer even with no output, got %q", err.Error())
	}
	if !errors.Is(err, tools.ErrSubagentStoppedByUser) {
		t.Errorf("error must wrap ErrSubagentStoppedByUser, got %v", err)
	}
}

// TestSubagentStoppedMessage_EmptyTextNoJobID: nothing and no-conversation
// to resume — a bare failure, no invented resume path. Revert check: add a
// resume(job_id=...) reference with an empty id and this fails.
func TestSubagentStoppedMessage_EmptyTextNoJobID(t *testing.T) {
	_, err := subagentStoppedMessage("", "")
	if strings.Contains(err.Error(), "resume(") {
		t.Errorf("no resume hint should appear when there is no job id, got %q", err.Error())
	}
	if !errors.Is(err, tools.ErrSubagentStoppedByUser) {
		t.Errorf("error must wrap ErrSubagentStoppedByUser, got %v", err)
	}
}
