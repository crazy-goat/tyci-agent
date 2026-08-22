package tools

import (
	"context"
	"strings"
	"testing"
)

// fakeJobAsker is a minimal JobAsker whose Ask always returns a fixed
// canned answer/fromUser/ok triple, for exercising AskTool.Run's handling
// of the provenance flag without a real jobs.Registry.
type fakeJobAsker struct {
	answer   string
	fromUser bool
	ok       bool
}

func (f fakeJobAsker) Ask(ctx context.Context, id, question string) (string, bool, bool) {
	return f.answer, f.fromUser, f.ok
}

// TestAskTool_AgentAnswerIsMarkedNotTheUser guards the fix for the bug
// where a parent agent could answer a child's "ask_parent" (via the
// "answer_job" tool) and the child had no way to tell that from a real human reply — it
// would report back "the user said X" when no user had said anything.
// Revert AskTool.Run's fromUser handling and this fails because the
// content comes back as the bare answer, with no marker at all.
func TestAskTool_AgentAnswerIsMarkedNotTheUser(t *testing.T) {
	old := jobAsker
	defer func() { jobAsker = old }()
	jobAsker = fakeJobAsker{answer: "yes, go ahead", fromUser: false, ok: true}

	tool := &AskTool{}
	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, "job-1-1")
	res := tool.Run(ctx, map[string]any{"question": "should I proceed?"})

	if !res.Success {
		t.Fatalf("expected success, got error %q", res.Error)
	}
	content := res.Content
	if !strings.Contains(content, "NOT the user") {
		t.Fatalf("expected the content to mark this as not from the user, got %q", content)
	}
	if !strings.Contains(content, "yes, go ahead") {
		t.Fatalf("expected the actual answer to still be present, got %q", content)
	}
}

// TestAskTool_UserAnswerIsPlain guards the other half: a genuine human
// answer (fromUser=true) must reach the child as the bare answer text,
// with no "not the user" marker attached to it.
func TestAskTool_UserAnswerIsPlain(t *testing.T) {
	old := jobAsker
	defer func() { jobAsker = old }()
	jobAsker = fakeJobAsker{answer: "yes, go ahead", fromUser: true, ok: true}

	tool := &AskTool{}
	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, "job-1-1")
	res := tool.Run(ctx, map[string]any{"question": "should I proceed?"})

	if !res.Success {
		t.Fatalf("expected success, got error %q", res.Error)
	}
	content := res.Content
	if content != "yes, go ahead" {
		t.Fatalf("expected the bare answer with no provenance marker, got %q", content)
	}
}

// fakeJobAnswerer records the fromUser flag it was called with, so
// AnswerTool.Run's contract (it always represents the model, never a
// person, so it must always pass fromUser=false) can be checked directly.
type fakeJobAnswerer struct {
	gotID       string
	gotText     string
	gotFromUser bool
	ret         bool
}

func (f *fakeJobAnswerer) Answer(id, text string, fromUser bool) bool {
	f.gotID, f.gotText, f.gotFromUser = id, text, fromUser
	return f.ret
}

// TestAnswerTool_AlwaysMarksFromUserFalse guards against the "answer_job" tool
// — reachable only by a model, never directly by a person — ever being
// wired to claim its answer came from the user. Revert AnswerTool.Run's
// hardcoded false and this fails once JobAnswerer's signature is restored
// to accept it as a variable (or once it's flipped to true).
func TestAnswerTool_AlwaysMarksFromUserFalse(t *testing.T) {
	old := jobAnswerer
	defer func() { jobAnswerer = old }()
	fake := &fakeJobAnswerer{ret: true}
	jobAnswerer = fake

	tool := &AnswerTool{}
	res := tool.Run(context.Background(), map[string]any{"job_id": "job-1-1", "text": "blue"})

	if !res.Success {
		t.Fatalf("expected success, got error %q", res.Error)
	}
	if fake.gotFromUser {
		t.Fatal("expected the answer tool to always pass fromUser=false — it is only ever invoked by a model")
	}
	if fake.gotID != "job-1-1" || fake.gotText != "blue" {
		t.Fatalf("expected the call to pass through id/text unchanged, got (%q, %q)", fake.gotID, fake.gotText)
	}
}
