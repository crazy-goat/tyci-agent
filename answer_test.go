package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

// TestResolveAnswerTarget_BareTextWithOneWaitingJob covers the common case
// the bug report called out explicitly: with exactly one job waiting,
// "/answer <text>" with no id must resolve against it.
func TestResolveAnswerTarget_BareTextWithOneWaitingJob(t *testing.T) {
	waiting := []jobs.Job{{ID: "job-1-7", Status: jobs.StatusWaitingAnswer}}
	job, text, errMsg := resolveAnswerTarget(waiting, "go ahead")
	if errMsg != "" {
		t.Fatalf("expected no error, got %q", errMsg)
	}
	if job.ID != "job-1-7" || text != "go ahead" {
		t.Fatalf("expected (job-1-7, %q), got (%s, %q)", "go ahead", job.ID, text)
	}
}

// TestResolveAnswerTarget_LeadingNumberWithOneWaitingJobIsNotEaten is
// review finding 4, row 1: with exactly one job waiting, a leading number
// in the answer text that happens to equal that job's short id must NOT be
// consumed as an id — there is nothing to disambiguate, so nothing to
// parse. Before the fix this silently dropped "5" from the answer text.
func TestResolveAnswerTarget_LeadingNumberWithOneWaitingJobIsNotEaten(t *testing.T) {
	waiting := []jobs.Job{{ID: "job-100-5", Status: jobs.StatusWaitingAnswer}}
	job, text, errMsg := resolveAnswerTarget(waiting, "5 minutes should be enough")
	if errMsg != "" {
		t.Fatalf("expected no error, got %q", errMsg)
	}
	if job.ID != "job-100-5" || text != "5 minutes should be enough" {
		t.Fatalf("expected (job-100-5, %q), got (%s, %q)", "5 minutes should be enough", job.ID, text)
	}
}

// TestResolveAnswerTarget_LeadingNumberOfADifferentJobWithOneWaiting is
// review finding 4, row 3: a leading number that happens to name a
// DIFFERENT (non-waiting) job, with only one job actually waiting, must
// still resolve against the one job waiting and keep the number as part of
// the text — it is not an id reference at all in this context.
func TestResolveAnswerTarget_LeadingNumberOfADifferentJobWithOneWaiting(t *testing.T) {
	waiting := []jobs.Job{{ID: "job-100-5", Status: jobs.StatusWaitingAnswer}}
	job, text, errMsg := resolveAnswerTarget(waiting, "3 go ahead")
	if errMsg != "" {
		t.Fatalf("expected no error, got %q", errMsg)
	}
	if job.ID != "job-100-5" || text != "3 go ahead" {
		t.Fatalf("expected (job-100-5, %q), got (%s, %q)", "3 go ahead", job.ID, text)
	}
}

// TestResolveAnswerTarget_BareNumberIsAnIDWhenMultipleWaiting is review
// finding 4, row 2: with more than one job waiting, a bare (non-"#")
// number IS read as an id — there is something to disambiguate, so a
// leading number is exactly what "/answer <id> <text>" without "#" means.
func TestResolveAnswerTarget_BareNumberIsAnIDWhenMultipleWaiting(t *testing.T) {
	waiting := []jobs.Job{
		{ID: "job-100-3", Status: jobs.StatusWaitingAnswer},
		{ID: "job-200-5", Status: jobs.StatusWaitingAnswer},
	}
	job, text, errMsg := resolveAnswerTarget(waiting, "5 minutes should be enough")
	if errMsg != "" {
		t.Fatalf("expected no error, got %q", errMsg)
	}
	if job.ID != "job-200-5" || text != "minutes should be enough" {
		t.Fatalf("expected (job-200-5, %q), got (%s, %q)", "minutes should be enough", job.ID, text)
	}
}

// TestResolveAnswerTarget_ShortIDWithHash covers the form the panel
// actually displays: shortJobID renders "job-<nanos>-<n>" as just "<n>", so
// a person types "#3", not the full id.
func TestResolveAnswerTarget_ShortIDWithHash(t *testing.T) {
	waiting := []jobs.Job{
		{ID: "job-100-3", Status: jobs.StatusWaitingAnswer},
		{ID: "job-200-9", Status: jobs.StatusWaitingAnswer},
	}
	job, text, errMsg := resolveAnswerTarget(waiting, "#3 go ahead")
	if errMsg != "" {
		t.Fatalf("expected no error, got %q", errMsg)
	}
	if job.ID != "job-100-3" || text != "go ahead" {
		t.Fatalf("expected (job-100-3, %q), got (%s, %q)", "go ahead", job.ID, text)
	}
}

// TestResolveAnswerTarget_HashedIDNotFoundIsAnError covers a "#"-prefixed
// id that matches nothing: an explicit id typo must be rejected, never
// silently answered against the wrong (or only) job with the "#id" text
// left dangling in the answer.
func TestResolveAnswerTarget_HashedIDNotFoundIsAnError(t *testing.T) {
	waiting := []jobs.Job{{ID: "job-100-5", Status: jobs.StatusWaitingAnswer}}
	_, _, errMsg := resolveAnswerTarget(waiting, "#7 do it")
	if errMsg == "" {
		t.Fatal("expected an error for an id that matches no waiting job")
	}
	if !strings.Contains(errMsg, "#7") {
		t.Fatalf("expected the error to name the unmatched id, got %q", errMsg)
	}
}

// TestResolveAnswerTarget_AmbiguousWithoutIDRejected covers the case the
// task explicitly calls out: with more than one job waiting and no id given
// (and the first word matching none), reject rather than guess.
func TestResolveAnswerTarget_AmbiguousWithoutIDRejected(t *testing.T) {
	waiting := []jobs.Job{
		{ID: "job-100-3", Status: jobs.StatusWaitingAnswer},
		{ID: "job-200-9", Status: jobs.StatusWaitingAnswer},
	}
	job, _, errMsg := resolveAnswerTarget(waiting, "go ahead with it")
	if errMsg == "" {
		t.Fatalf("expected an ambiguity error, got a resolved job %q", job.ID)
	}
	if !strings.Contains(errMsg, "multiple jobs waiting") {
		t.Fatalf("expected the error to explain the ambiguity, got %q", errMsg)
	}
}

// TestResolveAnswerTarget_NoneWaiting covers there being nothing to answer.
func TestResolveAnswerTarget_NoneWaiting(t *testing.T) {
	_, _, errMsg := resolveAnswerTarget(nil, "anything")
	if errMsg == "" {
		t.Fatal("expected an error when no job is waiting")
	}
}

// TestHandleAnswerCommand_DeliversAndUnblocks is an end-to-end check that
// the slash command actually reaches Registry.Answer: without any REPL/TUI
// caller wired to it, Answer was unreachable by a human at all (grep it —
// only the "answer" tool called it, and only the model can invoke a tool).
func TestHandleAnswerCommand_DeliversAndUnblocks(t *testing.T) {
	reg := jobs.NewRegistry()
	askDone := make(chan struct{})
	var gotAnswer string
	var gotFromUser bool
	reg.Start(context.Background(), "do a thing", func(ctx context.Context, jobID string) (string, bool, error) {
		gotAnswer, gotFromUser, _ = reg.Ask(ctx, jobID, "which way?")
		close(askDone)
		return "done", false, nil
	})

	// Wait for the job to actually reach StatusWaitingAnswer before answering.
	deadline := time.Now().Add(2 * time.Second)
	for len(waitingJobs(reg)) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the job to reach StatusWaitingAnswer")
		}
		time.Sleep(5 * time.Millisecond)
	}

	msg, ok := handleAnswerCommand(reg, "left")
	if !ok {
		t.Fatalf("expected handleAnswerCommand to succeed, got message %q", msg)
	}
	if !strings.Contains(msg, "which way?") {
		t.Fatalf("expected the confirmation to echo the answered question, got %q", msg)
	}

	<-askDone
	if gotAnswer != "left" {
		t.Fatalf("expected the job to receive %q, got %q", "left", gotAnswer)
	}
	if !gotFromUser {
		t.Fatal("expected an answer delivered via handleAnswerCommand to be marked fromUser=true")
	}
}
