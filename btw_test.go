package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
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
	if got.MaxRetries != base.MaxRetries {
		t.Errorf("MaxRetries should be preserved")
	}
	if got.MaxIterations != 0 {
		t.Errorf("MaxIterations should be unlimited for a /btw child, got %d", got.MaxIterations)
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

// TestBtwConfig_RestoresAskParent guards item 29's role-gating: the
// top-level agent.Config.Schema (base.Schema here, built in commands.go via
// tools.GetTopLevelToolsSchemaJSON) excludes ask_parent because the
// top-level conversation is not itself a job. A /btw side-conversation IS a
// job (startBtw stamps jobCtx with tools.JobIDCtxKey before running it), so
// btwConfig must restore ask_parent onto its forked Schema rather than
// inheriting the top-level one verbatim — otherwise /btw would offer a tool
// that always fails immediately for it, exactly the bug item 29 fixed for
// the top-level agent.
func TestBtwConfig_RestoresAskParent(t *testing.T) {
	base := agent.Config{
		Schema: tools.GetTopLevelToolsSchemaJSON(),
	}
	if schemaHasTool(t, base.Schema, "ask_parent") {
		t.Fatal("test setup: the top-level schema should not include ask_parent")
	}

	got := btwConfig(base)

	if !schemaHasTool(t, got.Schema, "ask_parent") {
		t.Error("btwConfig must restore ask_parent onto /btw's schema — a /btw side-conversation is a job and can use it")
	}
}

// schemaHasTool reports whether a marshaled tool schema (json.RawMessage)
// contains a function named name.
func schemaHasTool(t *testing.T, raw json.RawMessage, name string) bool {
	t.Helper()
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	for _, e := range entries {
		fn, ok := e["function"].(map[string]any)
		if !ok {
			continue
		}
		if n, _ := fn["name"].(string); n == name {
			return true
		}
	}
	return false
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

	job := JobRegistry.Start(context.Background(), "test job", jobs.KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
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

	job := reg.Start(context.Background(), "adapter test", jobs.KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
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

// TestJobWaiterAdapter_TranslatesProgressHistoryAndTruncation pins the real
// wiring end to end (review E6): the two tests above this one only ever
// exercised mockJobWaiter in tools/wait_test.go, so nothing pinned that
// jobWaiterAdapter actually carries jobs.Job.ProgressHistory and
// ProgressHistoryTruncated through to tools.JobStatus. Reports enough
// notes to exceed the registry's live cap, the same way a chatty
// backgrounded shell command does in practice (see tools/bash.go's
// bashRun.setProgress), and checks both fields survive the real
// jobWaiterAdapter.Wait translation, not just a hand-built status struct.
func TestJobWaiterAdapter_TranslatesProgressHistoryAndTruncation(t *testing.T) {
	reg := jobs.NewRegistry()
	adapter := jobWaiterAdapter{reg: reg}

	job := reg.Start(context.Background(), "chatty adapter test", jobs.KindOther, "", func(context.Context, string) (string, bool, error) {
		return "done", false, nil
	})

	const notes = 30
	for i := 0; i < notes; i++ {
		if !reg.SetProgress(job.ID, fmt.Sprintf("note %d", i)) {
			t.Fatalf("SetProgress failed for note %d", i)
		}
	}

	status, ok := adapter.Wait(context.Background(), job.ID, time.Second)
	if !ok {
		t.Fatal("expected job to be found")
	}
	if len(status.ProgressHistory) == 0 || len(status.ProgressHistory) >= notes {
		t.Fatalf("expected a bounded, non-empty ProgressHistory (< %d entries), got %d: %v", notes, len(status.ProgressHistory), status.ProgressHistory)
	}
	wantNewest := fmt.Sprintf("note %d", notes-1)
	if got := status.ProgressHistory[len(status.ProgressHistory)-1]; got != wantNewest {
		t.Fatalf("expected newest entry %q, got %q", wantNewest, got)
	}
	if !status.ProgressHistoryTruncated {
		t.Error("expected ProgressHistoryTruncated=true after reporting far more notes than the cap retains")
	}
	if status.Progress != wantNewest {
		t.Fatalf("expected Progress to still track the latest note %q, got %q", wantNewest, status.Progress)
	}
}

// TestJobObserverAdapter_TranslatesProgressHistoryAndTruncation is
// jobWaiterAdapter's test above, mirrored for jobObserverAdapter — the two
// adapters translate the same jobs.Job fields independently (see
// jobObserverAdapter's own doc comment for why they are not allowed to
// share a method signature), so a fix to one's translation would not
// necessarily catch a miss in the other's.
func TestJobObserverAdapter_TranslatesProgressHistoryAndTruncation(t *testing.T) {
	reg := jobs.NewRegistry()
	adapter := jobObserverAdapter{reg: reg}

	job := reg.Start(context.Background(), "chatty observer test", jobs.KindOther, "", func(context.Context, string) (string, bool, error) {
		return "done", false, nil
	})

	const notes = 30
	for i := 0; i < notes; i++ {
		if !reg.SetProgress(job.ID, fmt.Sprintf("note %d", i)) {
			t.Fatalf("SetProgress failed for note %d", i)
		}
	}

	status, ok := adapter.Observe(context.Background(), job.ID, time.Second)
	if !ok {
		t.Fatal("expected job to be found")
	}
	if len(status.ProgressHistory) == 0 || len(status.ProgressHistory) >= notes {
		t.Fatalf("expected a bounded, non-empty ProgressHistory (< %d entries), got %d: %v", notes, len(status.ProgressHistory), status.ProgressHistory)
	}
	if !status.ProgressHistoryTruncated {
		t.Error("expected ProgressHistoryTruncated=true after reporting far more notes than the cap retains")
	}
}

// TestBtwPromotionAdapter_PreservesTranscriptAndCreatesOneSubthread pins the
// handoff contract: promotion consumes a completed evaluation once, starts one
// real subagent job, and gives it every message from the side conversation
// before the continuation instruction.
func TestBtwPromotionAdapter_PreservesTranscriptAndCreatesOneSubthread(t *testing.T) {
	reg := jobs.NewRegistry()
	prevRegistry, prevNotices := JobRegistry, JobNotices
	JobRegistry, JobNotices = reg, jobs.NewNotifier()
	defer func() { JobRegistry, JobNotices = prevRegistry, prevNotices }()

	fake := connectortest.Text("promoted result")
	evaluationID := "btw-evaluation-test"
	transcript := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "main context"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "side analysis"}}},
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "yes, do it"}}},
	}
	btwEvaluationsMu.Lock()
	btwEvaluations[evaluationID] = &btwEvaluation{
		msgs: transcript, mc: fake, cfg: agent.Config{MaxRetries: 1}, question: "implement the idea",
	}
	btwEvaluationsMu.Unlock()
	defer func() {
		btwEvaluationsMu.Lock()
		delete(btwEvaluations, evaluationID)
		btwEvaluationsMu.Unlock()
	}()

	before := len(reg.List())
	handle, err := (btwPromotionAdapter{}).Promote(context.Background(), evaluationID)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if handle == nil || handle.ID() == "" {
		t.Fatal("Promote returned no real job handle")
	}
	if got := len(reg.List()); got != before+1 {
		t.Fatalf("promotion created %d jobs, want exactly one", got-before)
	}

	job, ok := reg.Wait(context.Background(), handle.ID(), 5*time.Second)
	if !ok || job.Status != jobs.StatusDone {
		t.Fatalf("promoted job did not finish successfully: ok=%v job=%+v", ok, job)
	}
	if !strings.Contains(job.Result, "promoted result") {
		t.Fatalf("unexpected promoted result %q", job.Result)
	}
	requests := fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("expected one model request, got %d", len(requests))
	}
	got := requests[0].Messages
	if len(got) != len(transcript)+1 {
		t.Fatalf("expected full transcript plus continuation, got %d messages: %+v", len(got), got)
	}
	for i := range transcript {
		if got[i].Role != transcript[i].Role || got[i].Content[0].Text != transcript[i].Content[0].Text {
			t.Fatalf("transcript message %d was not preserved: got %+v want %+v", i, got[i], transcript[i])
		}
	}
	if got[len(got)-1].Role != "user" || !strings.Contains(got[len(got)-1].Content[0].Text, "worth doing") {
		t.Fatalf("missing promotion continuation instruction: %+v", got[len(got)-1])
	}

	notices := JobNotices.Drain()
	if len(notices) != 0 {
		t.Fatalf("promotion must not enqueue a duplicate notice; tool result is the parent handoff, got %v", notices)
	}
	btwEvaluationsMu.Lock()
	_, retained := btwEvaluations[evaluationID]
	btwEvaluationsMu.Unlock()
	if retained {
		t.Fatal("promoted evaluation retained its full transcript")
	}
	if _, err := (btwPromotionAdapter{}).Promote(context.Background(), evaluationID); err == nil {
		t.Fatal("a consumed evaluation must not be promoted twice")
	}
}

// TestBtwPromotionAdapter_UsesChildRuntimeGate prevents the promoted path from
// accidentally becoming a privileged agent.Run: a hallucinated secondary
// subagent call must be refused, and the registry must still contain only the
// one promoted job.
func TestBtwPromotionAdapter_UsesChildRuntimeGate(t *testing.T) {
	reg := jobs.NewRegistry()
	prevRegistry, prevNotices := JobRegistry, JobNotices
	JobRegistry, JobNotices = reg, jobs.NewNotifier()
	defer func() { JobRegistry, JobNotices = prevRegistry, prevNotices }()

	fake := &connectortest.Fake{
		ProviderName: "test-provider",
		ModelName:    "test-model",
		Script: func(turn int, _ connector.Request) []stream.Event {
			if turn == 0 {
				return []stream.Event{
					stream.ToolCall{ID: "call-secondary", Name: "subagent", Arguments: `{"task":"should not run"}`},
					stream.Finish{Reason: "tool_calls"},
				}
			}
			return []stream.Event{stream.TextDelta{Text: "gate worked"}, stream.Finish{Reason: "stop"}}
		},
	}
	evaluationID := "btw-evaluation-gate-test"
	btwEvaluationsMu.Lock()
	btwEvaluations[evaluationID] = &btwEvaluation{
		msgs: []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "context"}}}},
		mc:   fake, question: "gate test",
	}
	btwEvaluationsMu.Unlock()

	handle, err := (btwPromotionAdapter{}).Promote(context.Background(), evaluationID)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	job, ok := reg.Wait(context.Background(), handle.ID(), 5*time.Second)
	if !ok || job.Status != jobs.StatusDone {
		t.Fatalf("promoted job did not finish: ok=%v job=%+v", ok, job)
	}
	if !strings.Contains(job.Result, "gate worked") {
		t.Fatalf("unexpected result after refused secondary subagent: %q", job.Result)
	}
	if got := len(reg.List()); got != 1 {
		t.Fatalf("secondary subagent escaped the child gate: registry has %d jobs, want 1", got)
	}
	if got := fake.Calls(); got != 2 {
		t.Fatalf("expected one refused tool turn plus final answer, got %d model calls", got)
	}
}
