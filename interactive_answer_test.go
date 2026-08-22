package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestHandleCommand_AnswerRoutesToJobRegistry is finding 3's REPL leg: the
// reviewer deleted the "/answer" case from interactive.go's handleCommand
// and the whole suite stayed green, because nothing asserted the literal
// "/answer" string is routed anywhere. This pins the route itself, not just
// resolveAnswerTarget/handleAnswerCommand in isolation.
func TestHandleCommand_AnswerRoutesToJobRegistry(t *testing.T) {
	reg, _ := withTestWiring(t)

	askDone := make(chan struct{})
	var gotAnswer string
	reg.Start(context.Background(), "do a thing", func(ctx context.Context, jobID string) (string, bool, error) {
		gotAnswer, _, _ = reg.Ask(ctx, jobID, "which way?")
		close(askDone)
		return "done", false, nil
	})

	deadline := time.Now().Add(2 * time.Second)
	for len(waitingJobs(reg)) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the job to reach StatusWaitingAnswer")
		}
		time.Sleep(5 * time.Millisecond)
	}

	s := &interactiveState{}
	exit, handled := s.handleCommand("/answer go left", func() {})
	if exit {
		t.Fatal("/answer must not end the REPL loop")
	}
	if !handled {
		t.Fatal("/answer must be recognized as a handled slash command, not fall through as a prompt to the model")
	}

	select {
	case <-askDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the job to receive the answer")
	}
	if gotAnswer != "go left" {
		t.Fatalf("expected the job to receive %q via handleCommand, got %q", "go left", gotAnswer)
	}
}

// TestHandleCommand_AnswerBarePrefixAlsoRoutes covers the bare "/answer"
// form still being recognized as the actual command — not merely as
// "handled", which the switch's default (unrecognized command) case also
// returns, but specifically routed to handleAnswerCommand. Captures stderr
// to tell those two apart: an unrecognized command prints "Unknown
// command: /answer", while the real route prints resolveAnswerTarget's
// "no job is currently waiting" message.
func TestHandleCommand_AnswerBarePrefixAlsoRoutes(t *testing.T) {
	withTestWiring(t) // no job waiting, so the real route's own error is distinctive

	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()

	s := &interactiveState{}
	_, handled := s.handleCommand("/answer", func() {})

	w.Close()
	os.Stderr = origStderr
	out, _ := io.ReadAll(r)

	if !handled {
		t.Fatal("bare /answer must be recognized as a handled slash command")
	}
	if strings.Contains(string(out), "Unknown command") {
		t.Fatalf("bare /answer fell through to the unrecognized-command branch: %q", out)
	}
	if !strings.Contains(string(out), "no job is currently waiting") {
		t.Fatalf("expected the real /answer route's own message, got %q", out)
	}
}
