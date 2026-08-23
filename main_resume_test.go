package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// alwaysHelpTool is a Fake that calls the always-registered "help" tool on
// every turn, so the child agent never finishes on its own and is forced to
// stop at its MaxIterations cap. "help" is chosen because it needs no
// wiring (tools.RunTool has it registered unconditionally — see
// tools/tool.go) and always succeeds with empty args.
func alwaysHelpTool() *connectortest.Fake {
	return &connectortest.Fake{
		ProviderName: "always-help",
		ModelName:    "always-help-1",
		OnExhausted: []stream.Event{
			stream.ToolCall{ID: "tc", Name: "help", Arguments: "{}"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
}

// TestSubagentCutoffMessage_ZeroOutput_StaysResumable pins the item 28(B)
// fix: subagentCutoffMessage (the decision run() delegates to once a child
// is cut off) must NOT tell the model to "narrow the task or split it" when
// jobID is non-empty — that phrasing claims the conversation is a dead end,
// but a non-empty jobID means main.go's run() just stashed a resumable
// entry for it (see resumable[jobID] at run()'s stash site). Exercised
// directly against the pure decision function — rather than via a real
// agent.Run call — because agent.Run's own "possible infinite loop"
// diagnostic (see agent.go, right before it returns ErrMaxIterations) means
// collected text is never actually empty on the real iteration-cap path in
// practice; the function must still get the logic right for whenever text
// truly is empty.
func TestSubagentCutoffMessage_ZeroOutput_StaysResumable(t *testing.T) {
	for _, deadlineWasHit := range []bool{false, true} {
		jobID := "job-zero-output-1"
		_, err := subagentCutoffMessage("", deadlineWasHit, jobID, 5, context.DeadlineExceeded)
		if err == nil {
			t.Fatalf("deadlineWasHit=%v: expected an error, got nil", deadlineWasHit)
		}
		msg := err.Error()
		if strings.Contains(msg, "narrow the task or split it") {
			t.Errorf("deadlineWasHit=%v: error claims the task must be narrowed/split even though a resumable entry exists: %q", deadlineWasHit, msg)
		}
		if !strings.Contains(msg, "resumable") && !strings.Contains(msg, "resume(") {
			t.Errorf("deadlineWasHit=%v: error does not mention that the conversation is resumable: %q", deadlineWasHit, msg)
		}
		if !strings.Contains(msg, jobID) {
			t.Errorf("deadlineWasHit=%v: error does not carry the job id needed to actually call resume(): %q", deadlineWasHit, msg)
		}
	}

	// With no job id at all (nothing was stashed for it), the honest
	// message IS "narrow the task or split it" — this must still be said
	// when it's true.
	_, err := subagentCutoffMessage("", false, "", 5, nil)
	if err == nil || !strings.Contains(err.Error(), "narrow the task or split it") {
		t.Errorf("with no job id, expected the narrow/split message, got: %v", err)
	}
}

// TestAgentRunnerRun_ZeroOutputTruncated_ResumableEntryExists checks the
// other half of the same fix end-to-end through agentRunner.run: whenever a
// jobID is present, run() must actually have stashed resumable[jobID] by
// the time it returns its cutoff error — otherwise the message
// subagentCutoffMessage produces above would itself be a dead end.
func TestAgentRunnerRun_ZeroOutputTruncated_ResumableEntryExists(t *testing.T) {
	fake := alwaysHelpTool()
	ctx := connector.WithModelClient(context.Background(), fake)
	jobID := "job-zero-output-2"
	ctx = context.WithValue(ctx, tools.JobIDCtxKey{}, jobID)

	one := 2
	opts := tools.SubagentOptions{MaxIterations: &one}

	r := &agentRunner{}
	_, err := r.run(ctx, "do the thing", "", "", opts)
	if err == nil {
		t.Fatalf("expected an error (hit the iteration cap), got nil")
	}

	resumableMu.Lock()
	_, ok := resumable[jobID]
	resumableMu.Unlock()
	if !ok {
		t.Errorf("resumable[%q] was not stashed for a truncated child", jobID)
	}
}

// TestAgentRunnerRun_TruncatedWithText_NoteCarriesUsableJobID pins item
// 28(B)'s other half: when a truncated child DID produce partial text, the
// "[note: ...]" appended to it must carry a job id the model can actually
// call resume(job_id=...) with — not just a generic "use resume" hint. This
// also protects tools/resume.go's contract: ResumeTool.Run requires job_id
// as input, so a note without one is unactionable.
func TestAgentRunnerRun_TruncatedWithText_NoteCarriesUsableJobID(t *testing.T) {
	fake := &connectortest.Fake{
		ProviderName: "partial",
		ModelName:    "partial-1",
		Turns: [][]stream.Event{
			{
				stream.TextDelta{Text: "partial progress"},
				stream.ToolCall{ID: "tc", Name: "help", Arguments: "{}"},
				stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
			},
		},
		OnExhausted: []stream.Event{
			stream.ToolCall{ID: "tc", Name: "help", Arguments: "{}"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
	ctx := connector.WithModelClient(context.Background(), fake)
	jobID := "job-partial-1"
	ctx = context.WithValue(ctx, tools.JobIDCtxKey{}, jobID)

	two := 2
	opts := tools.SubagentOptions{MaxIterations: &two}

	r := &agentRunner{}
	text, err := r.run(ctx, "do the thing", "", "", opts)
	if !errors.Is(err, tools.ErrSubagentTruncated) {
		t.Fatalf("expected ErrSubagentTruncated, got %v", err)
	}
	if !strings.Contains(text, "partial progress") {
		t.Fatalf("returned text lost the child's partial output: %q", text)
	}
	if !strings.Contains(text, "resume(job_id=\""+jobID+"\"") && !strings.Contains(text, jobID) {
		t.Errorf("note text does not carry a usable job id (%q): %q", jobID, text)
	}

	resumableMu.Lock()
	_, ok := resumable[jobID]
	resumableMu.Unlock()
	if !ok {
		t.Errorf("resumable[%q] was not stashed for the truncated-with-text case", jobID)
	}
}

// TestAgentRunnerRun_DeadlineExceeded_ResumableWithPartialText pins item
// 28(C): the wall-clock deadline path must be treated the same way the
// iteration-cap path already is — a resumable entry stashed, and whatever
// partial text the child produced carried through — instead of a bare
// failure with the content thrown away and nothing to resume.
func TestAgentRunnerRun_DeadlineExceeded_ResumableWithPartialText(t *testing.T) {
	// BlockUntilCancel makes Stream hang and, once ctx is cancelled (here,
	// by the deadline below), report the cancellation as a
	// stream.StreamError carrying ctx.Err() — the same shape a real
	// connector reports a hit deadline with.
	fake := &connectortest.Fake{
		ProviderName:     "slow",
		ModelName:        "slow-1",
		BlockUntilCancel: true,
	}
	baseCtx := connector.WithModelClient(context.Background(), fake)
	jobID := "job-deadline-1"
	ctx, cancel := context.WithTimeout(baseCtx, 30*time.Millisecond)
	defer cancel()
	ctx = context.WithValue(ctx, tools.JobIDCtxKey{}, jobID)

	r := &agentRunner{}
	_, err := r.run(ctx, "do the thing", "", "", tools.SubagentOptions{})
	if err == nil {
		t.Fatalf("expected an error from a deadline that fired mid-stream, got nil")
	}
	if !strings.Contains(err.Error(), "resumable") && !strings.Contains(err.Error(), "resume(") {
		t.Errorf("deadline-exceeded error does not mention that the conversation is resumable: %q", err.Error())
	}

	// The key fix under test: before item 28(C), main.go:316 required
	// err == nil || truncated, and a deadline error is neither — so nothing
	// was ever stashed and a timed-out child was a dead end.
	resumableMu.Lock()
	_, ok := resumable[jobID]
	resumableMu.Unlock()
	if !ok {
		t.Errorf("resumable[%q] was not stashed on deadline exceeded — a timed-out child must be resumable, not a dead end", jobID)
	}
}

// ─── item 16: normal-completion resume hint ────────────────────────────────

// TestAgentRunnerRun_NormalCompletion_CarriesResumeHint is the core of item
// 16: a child that finishes cleanly (no cutoff at all — the case item 28
// already covers via subagentCutoffMessage) must still tell the parent it
// can continue this exact conversation later, with a real, usable job id.
func TestAgentRunnerRun_NormalCompletion_CarriesResumeHint(t *testing.T) {
	fake := connectortest.Text("the final answer")
	ctx := connector.WithModelClient(context.Background(), fake)
	jobID := "job-normal-1"
	ctx = context.WithValue(ctx, tools.JobIDCtxKey{}, jobID)

	r := &agentRunner{}
	text, err := r.run(ctx, "do the thing", "", "", tools.SubagentOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(text, "the final answer") {
		t.Fatalf("returned text lost the child's own answer: %q", text)
	}
	if !strings.Contains(text, "resume(job_id=\""+jobID+"\"") {
		t.Errorf("normal-completion text does not carry a usable resume hint with this job's id (%q): %q", jobID, text)
	}

	// Keep it short and on one line — every child's output carries it and
	// the parent pays for it in tokens (the item's explicit constraint).
	hint := text[len("the final answer"):]
	if strings.Contains(hint, "\n") {
		t.Errorf("resume hint is not a single line: %q", hint)
	}
	if len(hint) > 200 {
		t.Errorf("resume hint is longer than expected for a short, token-conscious note (%d bytes): %q", len(hint), hint)
	}
}

// TestAgentRunnerRun_NormalCompletion_NoJobID_NoHint is the "must not lie"
// half: with no job id at all (the only remaining gap — no job registry
// wired, real invocations always have one per tools/subagent.go's
// spawn/jobStarter wiring), the model must not be told to call
// resume(job_id="") — that would be actionable-looking advice pointing
// nowhere.
func TestAgentRunnerRun_NormalCompletion_NoJobID_NoHint(t *testing.T) {
	fake := connectortest.Text("the final answer")
	ctx := connector.WithModelClient(context.Background(), fake)

	r := &agentRunner{}
	text, err := r.run(ctx, "do the thing", "", "", tools.SubagentOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if text != "the final answer" {
		t.Fatalf("expected no hint appended when no job id is available, got %q", text)
	}
	if strings.Contains(text, "resume(") {
		t.Errorf("text mentions resume() without a job id to back it: %q", text)
	}
}

// TestAgentRunnerRun_CutoffAndNormalHints_AreMutuallyExclusive pins that the
// two resume-hint sites (subagentCutoffMessage for the cutoff case, run()'s
// own success return for the normal-completion case) never both fire for the
// same completion — they are gated on disjoint conditions (truncated ||
// deadlineExceeded vs. plain err == nil), so exercising each directly must
// show exactly one hint, never a duplicate.
func TestAgentRunnerRun_CutoffAndNormalHints_AreMutuallyExclusive(t *testing.T) {
	jobID := "job-exclusive-1"

	// Normal completion: exactly one hint.
	normalFake := connectortest.Text("done")
	normalCtx := context.WithValue(connector.WithModelClient(context.Background(), normalFake), tools.JobIDCtxKey{}, jobID)
	r := &agentRunner{}
	normalText, err := r.run(normalCtx, "task", "", "", tools.SubagentOptions{})
	if err != nil {
		t.Fatalf("normal run: %v", err)
	}
	if n := strings.Count(normalText, "resume(job_id="); n != 1 {
		t.Fatalf("normal completion: expected exactly 1 resume hint, got %d: %q", n, normalText)
	}

	// Cutoff (iteration cap): exactly one hint, and it must be the cutoff
	// wording (subagentCutoffMessage's "[note: ...]"), not the plain
	// success one — the two call sites are gated on disjoint conditions so
	// only one of them ever runs per completion.
	cutoffText, cutoffErr := subagentCutoffMessage("partial", false, jobID, 5, nil)
	if cutoffErr == nil {
		t.Fatalf("expected the cutoff path to return an error (Truncated sentinel)")
	}
	if n := strings.Count(cutoffText, "resume(job_id="); n != 1 {
		t.Fatalf("cutoff completion: expected exactly 1 resume hint, got %d: %q", n, cutoffText)
	}
	if !strings.Contains(cutoffText, "[note:") {
		t.Errorf("cutoff completion lost its cutoff-specific note wrapper: %q", cutoffText)
	}
}
