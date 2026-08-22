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

// TestTUIAnswerFunc_NonNilAndRoutesAnswerToJobRegistry is the TUI leg of the
// same finding TestHandleCommand_AnswerRoutesToJobRegistry pins for the REPL
// (item 27 round 3, "composition root can silently pass nil" bloker):
// tuiCmd's RunE hands display.NewTUI a function built by tuiAnswerFunc as
// its onAnswer parameter, and nothing before this test asserted that
// function is (a) actually produced non-nil from a real registry, and
// (b) actually delivers an answer typed through it into that registry.
// display.NewTUI now panics if it's ever handed nil (see
// requireAnswerWiring in display/tui_api.go), but that alone doesn't prove
// commands.go passes it something real — a stub that always returns
// ("", false) is also non-nil and would slip past that guard silently.
//
// This does not start a real TUI (display.NewTUI launches an actual
// bubbletea program reading os.Stdin, which a unit test should not do) — it
// calls tuiAnswerFunc directly, the same function commands.go's tuiCmd
// passes as NewTUI's last argument, and drives it exactly the way
// tui_keys.go's handleLocalSlashCommand does: call it with the raw
// "/answer" argument text and nothing else.
func TestTUIAnswerFunc_NonNilAndRoutesAnswerToJobRegistry(t *testing.T) {
	reg, _ := withTestWiring(t)

	fn := tuiAnswerFunc(reg)
	if fn == nil {
		t.Fatal("tuiAnswerFunc must return a non-nil function — this is exactly what commands.go hands to display.NewTUI's onAnswer parameter")
	}

	askDone := make(chan struct{})
	var gotAnswer string
	reg.Start(context.Background(), "do a tui thing", func(ctx context.Context, jobID string) (string, bool, error) {
		gotAnswer, _, _ = reg.Ask(ctx, jobID, "which color?")
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

	msg, ok := fn("blue")
	if !ok {
		t.Fatalf("expected tuiAnswerFunc to report success, got ok=false, msg=%q", msg)
	}

	select {
	case <-askDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the job to receive the answer")
	}
	if gotAnswer != "blue" {
		t.Fatalf("expected the job to receive %q via tuiAnswerFunc, got %q", "blue", gotAnswer)
	}
}
