package main

import (
	"context"
	"testing"
	"time"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/jobs"
)

// ─── forkMessagesForBtw ─────────────────────────────────────────────────

func TestForkMessagesForBtw_AppendsQuestionAsUserTurn(t *testing.T) {
	orig := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "hello"}}},
	}

	forked := forkMessagesForBtw(orig, "by the way, what time is it?")

	if len(forked) != len(orig)+1 {
		t.Fatalf("expected %d messages, got %d", len(orig)+1, len(forked))
	}
	last := forked[len(forked)-1]
	if last.Role != "user" {
		t.Errorf("expected last message role \"user\", got %q", last.Role)
	}
	if len(last.Content) != 1 || last.Content[0].Text != "by the way, what time is it?" {
		t.Errorf("unexpected last message content: %+v", last.Content)
	}
}

// TestForkMessagesForBtw_DoesNotMutateOriginal is the core safety property
// /btw depends on: appending to the fork (here, and later inside agent.Run's
// own tool-call turns) must never alias or change the main thread's history.
func TestForkMessagesForBtw_DoesNotMutateOriginal(t *testing.T) {
	orig := make([]connector.Message, 0, 4) // spare capacity to catch aliasing
	orig = append(orig, connector.Message{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}})

	forked := forkMessagesForBtw(orig, "side question")
	origLenBefore := len(orig)

	// Mutate the fork further, as agent.Run would while running tool-call
	// turns on it.
	forked = append(forked, connector.Message{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "answer"}}})

	if len(orig) != origLenBefore {
		t.Fatalf("original slice length changed: got %d, want %d", len(orig), origLenBefore)
	}
	if len(orig) != 1 {
		t.Fatalf("original should still have exactly 1 message, got %d", len(orig))
	}
	if len(forked) != 3 {
		t.Fatalf("expected fork to have 3 messages after append, got %d", len(forked))
	}

	// Mutating the ORIGINAL afterwards must not affect the fork either —
	// forkMessagesForBtw must have copied the backing array, not aliased it.
	orig = append(orig, connector.Message{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "unrelated follow-up"}}})
	if len(orig) != 2 {
		t.Fatalf("expected original to grow to 2 messages, got %d", len(orig))
	}
	if len(forked) != 3 {
		t.Fatalf("fork should be unaffected by appends to the original, got len %d", len(forked))
	}
	if forked[1].Content[0].Text != "side question" {
		t.Fatalf("fork's forked-in question was corrupted: %q", forked[1].Content[0].Text)
	}
}

func TestForkMessagesForBtw_EmptyHistory(t *testing.T) {
	forked := forkMessagesForBtw(nil, "question with no prior history")
	if len(forked) != 1 {
		t.Fatalf("expected 1 message, got %d", len(forked))
	}
	if forked[0].Role != "user" {
		t.Errorf("expected role \"user\", got %q", forked[0].Role)
	}
}

// ─── btwConfig ──────────────────────────────────────────────────────────

func TestBtwConfig_StripsMainThreadCallbacksButKeepsToolBehavior(t *testing.T) {
	base := agent.Config{
		System:        "you are tyci",
		MaxRetries:    5,
		MaxIterations: 10,
		NextMessages:  func() []string { return []string{"should never be called by a fork"} },
		PendingTodos:  func() []string { return []string{"todo"} },
		HasTodos:      func() bool { return true },
	}

	got := btwConfig(base)

	if got.System != base.System {
		t.Errorf("System should be preserved, got %q", got.System)
	}
	if got.MaxRetries != base.MaxRetries || got.MaxIterations != base.MaxIterations {
		t.Errorf("MaxRetries/MaxIterations should be preserved")
	}
	if got.Session != nil {
		t.Error("Session must be nil — a /btw fork writes no session log")
	}
	if got.NextMessages != nil {
		t.Error("NextMessages must be nil — that's the main TUI's pending-input queue")
	}
	if got.PendingTodos != nil {
		t.Error("PendingTodos must be nil — that's the main thread's todo list")
	}
	if got.HasTodos != nil {
		t.Error("HasTodos must be nil — that's the main thread's todo list")
	}
}

// ─── nextBtwID ──────────────────────────────────────────────────────────

func TestNextBtwID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := nextBtwID()
		if seen[id] {
			t.Fatalf("duplicate id generated: %q", id)
		}
		seen[id] = true
	}
}

// ─── startBtw / job registration ─────────────────────────────────────────

// TestStartBtw_RunsOnJobRegistryAndNeverBlocks verifies /btw's core
// contract: the fork runs as a real background job on the shared
// JobRegistry, and starting it returns immediately regardless of how long
// the underlying run takes.
func TestStartBtw_RegistersOnSharedJobRegistry(t *testing.T) {
	reg := jobs.NewRegistry()
	prevRegistry := JobRegistry
	JobRegistry = reg
	defer func() { JobRegistry = prevRegistry }()

	release := make(chan struct{})
	started := make(chan struct{})

	job := JobRegistry.Start(context.Background(), "test job", func(ctx context.Context) (string, bool, error) {
		close(started)
		<-release
		return "done", false, nil
	})

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job never started")
	}

	if _, ok := JobRegistry.Get(job.ID); !ok {
		t.Fatal("job should be registered in JobRegistry immediately")
	}

	close(release)

	result, ok := JobRegistry.Wait(context.Background(), job.ID, time.Second)
	if !ok {
		t.Fatal("expected job to be found")
	}
	if result.Status != jobs.StatusDone {
		t.Fatalf("expected job to finish as done, got status %q (err=%q)", result.Status, result.Err)
	}
}

func TestJobWaiterAdapter_TranslatesStatus(t *testing.T) {
	reg := jobs.NewRegistry()
	adapter := jobWaiterAdapter{reg: reg}

	job := reg.Start(context.Background(), "adapter test", func(ctx context.Context) (string, bool, error) {
		return "the answer", false, nil
	})

	status, ok := adapter.Wait(context.Background(), job.ID, time.Second)
	if !ok {
		t.Fatal("expected job to be found")
	}
	if !status.Done || !status.Success {
		t.Errorf("expected done+success, got %+v", status)
	}
	if status.Content != "the answer" {
		t.Errorf("expected content %q, got %q", "the answer", status.Content)
	}

	if _, ok := adapter.Wait(context.Background(), "no-such-job", time.Millisecond); ok {
		t.Error("expected unknown job id to report ok=false")
	}
}
