package main

import (
	"context"
	"testing"
	"time"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/tools"
)

// ─── inherit_history end-to-end: opts.History reaches the wire request ────

// TestAgentRunnerRun_InheritedHistoryReachesRequest is the end of the
// inherit_history path from the "subagent" tool (tools/subagent_test.go
// pins opts.History getting set from context) to the wire: agentRunner.run
// must seed the child's messages with opts.History plus task as a new user
// turn, in that order, exactly as session.ForkMessagesWithTurn would.
func TestAgentRunnerRun_InheritedHistoryReachesRequest(t *testing.T) {
	fake := connectortest.Text("child answer")
	ctx := connector.WithModelClient(context.Background(), fake)

	history := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "earlier question"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "earlier answer"}}},
	}
	opts := tools.SubagentOptions{History: history}

	r := &agentRunner{}
	if _, err := r.run(ctx, "do the thing", "", "be helpful", opts); err != nil {
		t.Fatalf("run: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	got := reqs[0].Messages
	if len(got) != 3 {
		t.Fatalf("expected 3 messages (2 inherited + 1 new task turn), got %d: %+v", len(got), got)
	}
	if got[0].Content[0].Text != "earlier question" || got[1].Content[0].Text != "earlier answer" {
		t.Fatalf("inherited history not seeded in order: %+v", got[:2])
	}
	if got[2].Role != "user" || got[2].Content[0].Text != "do the thing" {
		t.Fatalf("expected task appended as a new user turn, got %+v", got[2])
	}

	// The source slice handed in via opts.History must be unaffected —
	// same aliasing guarantee ForkMessagesWithTurn gives /btw.
	if len(history) != 2 {
		t.Fatalf("opts.History was mutated: %+v", history)
	}
}

// TestAgentRunnerRun_NoHistoryFallsBackToPlainTaskSeed is the control: the
// ordinary subagent call (no inherit_history) must keep seeding from just
// task, unchanged.
func TestAgentRunnerRun_NoHistoryFallsBackToPlainTaskSeed(t *testing.T) {
	fake := connectortest.Text("child answer")
	ctx := connector.WithModelClient(context.Background(), fake)

	r := &agentRunner{}
	if _, err := r.run(ctx, "do the thing", "", "be helpful", tools.SubagentOptions{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 || len(reqs[0].Messages) != 1 {
		t.Fatalf("expected exactly 1 seed message, got %+v", reqs)
	}
	if reqs[0].Messages[0].Content[0].Text != "do the thing" {
		t.Fatalf("unexpected seed message: %+v", reqs[0].Messages[0])
	}
}

// ─── ForkChildJob: fork-as-background-child ────────────────────────────────

// TestForkChildJob_ProducesWorkingJobSeededWithHistory drives ForkChildJob
// end-to-end: base history in, a real background job out, running on the
// shared JobRegistry, whose single request to the model carries the forked
// history plus the task as a new user turn.
func TestForkChildJob_ProducesWorkingJobSeededWithHistory(t *testing.T) {
	reg := jobs.NewRegistry()
	prevRegistry := JobRegistry
	JobRegistry = reg
	defer func() { JobRegistry = prevRegistry }()

	fake := connectortest.Text("fork child answer")
	cond := conductor.New(conductor.Options{
		Client: fake,
		Sink:   &collector{},
		Config: agent.Config{},
	})

	base := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "earlier question"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "earlier answer"}}},
	}

	job := ForkChildJob(context.Background(), cond, base, "continue from here")
	if job == nil {
		t.Fatal("expected a non-nil job")
	}

	result, ok := JobRegistry.Wait(context.Background(), job.ID, 5*time.Second)
	if !ok {
		t.Fatal("expected to find the job")
	}
	if result.Status != jobs.StatusDone {
		t.Fatalf("expected job to finish done, got status %q (err=%q)", result.Status, result.Err)
	}
	if result.Result != "fork child answer" {
		t.Fatalf("unexpected job result: %q", result.Result)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request to the model, got %d", len(reqs))
	}
	got := reqs[0].Messages
	if len(got) != 3 {
		t.Fatalf("expected 3 messages (2 base + 1 new task turn), got %d: %+v", len(got), got)
	}
	if got[0].Content[0].Text != "earlier question" || got[1].Content[0].Text != "earlier answer" {
		t.Fatalf("base history not seeded in order: %+v", got[:2])
	}
	if got[2].Role != "user" || got[2].Content[0].Text != "continue from here" {
		t.Fatalf("expected task appended as a new user turn, got %+v", got[2])
	}

	// Base must be unaffected by the fork.
	if len(base) != 2 {
		t.Fatalf("base slice was mutated: %+v", base)
	}

	// A finished forked job registers as resumable, same as
	// jobResumerAdapter.Resume, so it can itself be resumed/forked again.
	resumableMu.Lock()
	_, resumableOk := resumable[job.ID]
	resumableMu.Unlock()
	if !resumableOk {
		t.Error("expected the finished forked job to be registered as resumable")
	}
}

// ─── ForkNewSession: fork-as-new-session ───────────────────────────────────

// TestForkNewSession_ProducesLoadableResumableSession drives ForkNewSession
// end-to-end: base history in, a brand-new session file out, and that file
// must be loadable via the exact same path /resume uses
// (session.LoadForReplay), reproducing the same history.
func TestForkNewSession_ProducesLoadableResumableSession(t *testing.T) {
	cwd := t.TempDir()
	base := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "earlier question"}}},
		{Role: "assistant", Content: []connector.ContentBlock{
			{Type: "text", Text: "let me check"},
			{Type: "toolCall", ID: "call-1", Name: "bash", Arguments: []byte(`{"cmd":"ls"}`)},
		}},
		{Role: "toolResult", Content: []connector.ContentBlock{
			{Type: "text", Text: "file1", ToolCallID: "call-1", ToolName: "bash"},
		}},
	}

	sess, forked, err := ForkNewSession(cwd, "gpt-4", "openai", base)
	if err != nil {
		t.Fatalf("ForkNewSession() error: %v", err)
	}
	if len(forked) != len(base) {
		t.Fatalf("expected %d forked messages, got %d", len(base), len(forked))
	}
	path := sess.Path()
	if err := sess.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	// Independent copy: mutating base afterward must not have touched what
	// was written.
	base[0].Content[0].Text = "mutated"

	_, msgs, _, corrupt, err := session.LoadForReplay(path)
	if err != nil {
		t.Fatalf("LoadForReplay() error: %v", err)
	}
	if len(corrupt) != 0 {
		t.Fatalf("expected no corrupt lines, got %d", len(corrupt))
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Content[0].Text != "earlier question" {
		t.Fatalf("expected the pre-mutation text to survive in the persisted session, got %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[2].Role != "toolResult" {
		t.Fatalf("unexpected roles: %+v", msgs)
	}
}

// TestForkNewSession_IsListableAsARealSession checks the new session file
// shows up exactly like any other recorded session — `tyci session list`'s
// path (session.ListEntries over session.SessionDir).
func TestForkNewSession_IsListableAsARealSession(t *testing.T) {
	cwd := t.TempDir()
	sess, _, err := ForkNewSession(cwd, "gpt-4", "openai", []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}},
	})
	if err != nil {
		t.Fatalf("ForkNewSession() error: %v", err)
	}
	defer sess.Close()

	dir, err := session.SessionDir(cwd)
	if err != nil {
		t.Fatalf("SessionDir() error: %v", err)
	}
	entries, err := session.ListEntries(dir)
	if err != nil {
		t.Fatalf("ListEntries() error: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Path == sess.Path() {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the forked session %q to be listed among %+v", sess.Path(), entries)
	}
}
