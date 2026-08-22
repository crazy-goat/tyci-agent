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
	id, text, errMsg := resolveAnswerTarget(waiting, "go ahead")
	if errMsg != "" {
		t.Fatalf("expected no error, got %q", errMsg)
	}
	if id != "job-1-7" || text != "go ahead" {
		t.Fatalf("expected (job-1-7, %q), got (%s, %q)", "go ahead", id, text)
	}
}

// TestResolveAnswerTarget_ShortIDWithHash covers the form the panel
// actually displays: shortJobID renders "job-<nanos>-<n>" as just "<n>", so
// a person types "#3" (or "3"), not the full id.
func TestResolveAnswerTarget_ShortIDWithHash(t *testing.T) {
	waiting := []jobs.Job{
		{ID: "job-100-3", Status: jobs.StatusWaitingAnswer},
		{ID: "job-200-9", Status: jobs.StatusWaitingAnswer},
	}
	id, text, errMsg := resolveAnswerTarget(waiting, "#3 go ahead")
	if errMsg != "" {
		t.Fatalf("expected no error, got %q", errMsg)
	}
	if id != "job-100-3" || text != "go ahead" {
		t.Fatalf("expected (job-100-3, %q), got (%s, %q)", "go ahead", id, text)
	}
}

// TestResolveAnswerTarget_ShortIDWithoutHash covers the bare-digit form.
func TestResolveAnswerTarget_ShortIDWithoutHash(t *testing.T) {
	waiting := []jobs.Job{
		{ID: "job-100-3", Status: jobs.StatusWaitingAnswer},
		{ID: "job-200-9", Status: jobs.StatusWaitingAnswer},
	}
	id, text, errMsg := resolveAnswerTarget(waiting, "9 blue")
	if errMsg != "" {
		t.Fatalf("expected no error, got %q", errMsg)
	}
	if id != "job-200-9" || text != "blue" {
		t.Fatalf("expected (job-200-9, %q), got (%s, %q)", "blue", id, text)
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
	id, _, errMsg := resolveAnswerTarget(waiting, "go ahead with it")
	if errMsg == "" {
		t.Fatalf("expected an ambiguity error, got a resolved id %q", id)
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

	<-askDone
	if gotAnswer != "left" {
		t.Fatalf("expected the job to receive %q, got %q", "left", gotAnswer)
	}
	if !gotFromUser {
		t.Fatal("expected an answer delivered via handleAnswerCommand to be marked fromUser=true")
	}
}
