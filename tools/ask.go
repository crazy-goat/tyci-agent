package tools

import (
	"context"
)

// JobAsker is the local contract the "ask_parent" tool needs — set once
// from main() via SetJobAsker, over the app's shared jobs.Registry. Same
// layering rule as JobWaiter/JobStarter (see wait.go/subagent.go's doc
// comments): this package never imports "jobs" directly.
type JobAsker interface {
	// Ask blocks until answer arrives (via Answer) or ctx is done, returning
	// ok=false in the latter case. id is unknown -> ok=false immediately.
	// fromUser reports whether the answer is a genuine human reply as
	// opposed to another agent's (the "answer_job" tool is the only caller
	// today, and it always passes fromUser=false — see AskTool.Run, which
	// uses it to mark the answer's provenance for the child that asked).
	Ask(ctx context.Context, id, question string) (answer string, fromUser bool, ok bool)
}

// jobAsker is nil until SetJobAsker is called; the "ask_parent" tool fails
// loudly (not silently blocking or panicking) until then.
var jobAsker JobAsker

// SetJobAsker wires the "ask_parent" tool to a JobAsker. Called once from
// main() with an adapter over the app's shared jobs.Registry.
func SetJobAsker(a JobAsker) { jobAsker = a }

// AskUnroutableCtxKey marks a job's context to make "ask_parent" fail
// immediately when the caller cannot return control to anyone able to answer.
//
// Every subagent call gets a job id now (see subagent.go's runWithHandoff),
// including a blocking child under `tyci run` / `--print`. But a job id
// alone does not mean a question asked from inside it can ever be answered:
// answering requires the tool call that spawned the job to first return
// control to its own caller's agent loop (so a reminder about the pending
// question — or a person at a REPL — has a turn in which to call "answer_job").
// That happens for an async spawn (the call returns immediately) and for a
// blocking spawn that CAN hand its children to the background
// (runWithHandoff with handoff=true, gated on backgroundAllowed) — but not
// for a blocking spawn with no handoff available: that tool call does not
// return until the child itself finishes, so a child blocked in "ask_parent" there
// can never be unblocked no matter how long it waits. runWithHandoff sets
// this key to true for exactly that case (handoff=false); every other
// job-bearing context leaves it unset (the default, meaning ask_parent
// behaves as it always has: it blocks for a real answer).
type AskUnroutableCtxKey struct{}

// AskTool implements the "ask_parent" tool: lets a running job (any
// subagent call — blocking or async — or a /btw side-conversation) pose a
// blocking question to its parent — whoever spawned this job, which may be
// a human at the keyboard, another agent, or (if no handoff back to either
// is available) nobody reachable at all — and block until answered.
type AskTool struct{}

func (t *AskTool) Name() string { return "ask_parent" }

func (t *AskTool) Run(ctx context.Context, input map[string]any) ToolResult {
	question, _ := input["question"].(string)
	if question == "" {
		return validationResult("question is required")
	}

	jobID, ok := ctx.Value(JobIDCtxKey{}).(string)
	if !ok || jobID == "" {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   "ask_parent only works inside a job (a subagent call, or a /btw side-conversation) — this call has no job id",
		}
	}

	if unroutable, _ := ctx.Value(AskUnroutableCtxKey{}).(bool); unroutable {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   "ask_parent cannot get an answer here: the call that started this job cannot return control to anyone able to answer it (no background handoff is available). Proceed with your best judgement or state your assumption and continue",
		}
	}

	if jobAsker == nil {
		return ToolResult{Type: "result", Success: false, Error: "ask_parent unavailable: job registry not configured"}
	}

	answer, fromUser, ok := jobAsker.Ask(ctx, jobID, question)
	if !ok {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   "no answer arrived before this job was cancelled; proceed with your best judgement or state your assumption and continue",
		}
	}
	content := answer
	if !fromUser {
		// The parent agent (or another agent) answered this directly via
		// the "answer_job" tool, not the human — the child must not report
		// this back as if the user had said it.
		content = "[another agent answered this, NOT the user]: " + answer
	}
	return ToolResult{Type: "result", Success: true, Content: content}
}

// JobAnswerer is the local contract the "answer_job" tool needs — set once
// from main() via SetJobAnswerer, over the app's shared jobs.Registry.
type JobAnswerer interface {
	// Answer delivers text to a job currently blocked in Ask, unblocking it.
	// fromUser must be true for a human reply, false for another agent's —
	// see JobAsker.Ask's doc comment. The "answer_job" tool below always
	// passes false: it is only ever invoked by a model, never by a person
	// directly.
	// Returns false if id is unknown or the job isn't currently waiting.
	Answer(id, text string, fromUser bool) bool
}

// jobAnswerer is nil until SetJobAnswerer is called.
var jobAnswerer JobAnswerer

// SetJobAnswerer wires the "answer_job" tool to a JobAnswerer.
func SetJobAnswerer(a JobAnswerer) { jobAnswerer = a }

// AnswerTool implements the "answer_job" tool: relays a real answer to a
// job currently shown as waiting_answer (via "wait") and unblocks it —
// either something the calling agent genuinely already knows, or something
// the human actually said elsewhere, never a stand-in invented for a human
// who has not actually replied. Unlike "ask_parent", this tool has NO
// job-ctx gating — anyone (the main thread or another agent) can answer any
// job that's currently waiting; that's the point, it's how
// main-thread-mediated or agent-to-agent communication both work.
type AnswerTool struct{}

func (t *AnswerTool) Name() string { return "answer_job" }

func (t *AnswerTool) Run(ctx context.Context, input map[string]any) ToolResult {
	jobID, _ := input["job_id"].(string)
	if jobID == "" {
		return validationResult("job_id is required")
	}

	if action, _ := input["action"].(string); action == "extension" {
		requestID, _ := input["request_id"].(string)
		if requestID == "" {
			return validationResult("request_id is required for extension answers")
		}
		approve, ok := input["approve"].(bool)
		if !ok {
			return validationResult("approve is required for extension answers and must be a boolean")
		}
		if jobExtensionRequester == nil {
			return ToolResult{Type: "result", Success: false, Error: "answer_job unavailable: job extension requester not configured"}
		}
		if !jobExtensionRequester.ResolveExtension(jobID, requestID, approve) {
			return ToolResult{Type: "result", Success: false, Error: "job_id/request_id not found or extension request already resolved"}
		}
		if approve {
			return ToolResult{Type: "result", Success: true, Content: "timeout extension approved; job may continue"}
		}
		return ToolResult{Type: "result", Success: true, Content: "timeout extension rejected; job will stop at its current deadline"}
	}

	text, _ := input["text"].(string)
	if text == "" {
		return validationResult("text is required")
	}

	if jobAnswerer == nil {
		return ToolResult{Type: "result", Success: false, Error: "answer_job unavailable: job registry not configured"}
	}

	if !jobAnswerer.Answer(jobID, text, false) {
		return ToolResult{Type: "result", Success: false, Error: "job_id not found or not currently waiting for an answer"}
	}
	return ToolResult{Type: "result", Success: true, Content: "answer delivered; job resumed"}
}
