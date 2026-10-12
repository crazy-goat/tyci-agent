package tools

import (
	"context"
	"sync"
)

// JobAsker is the local contract the "ask_parent" tool needs — set once
// from main() via SetJobAsker, over the app's shared jobs.Registry. Same
// layering rule as JobWaiter/JobStarter (see wait.go/subagent.go's doc
// comments): this package never imports "jobs" directly.
type JobAsker interface {
	// Ask publishes the question of job id to its parent and returns at once.
	// It returns the question id, "q<Seq>", that answer_job and wait use.
	// ok is false when the question could not be published.
	Ask(ctx context.Context, id, question string) (qid string, ok bool)
}

// jobAsker is nil until SetJobAsker is called; the "ask_parent" tool fails
// loudly (not silently blocking or panicking) until then. Guarded by
// jobAskerMu for the same reason jobNotifier is (see bgbash.go's
// jobNotifierMu doc comment): it is read from job goroutines that outlive
// the tool call that started them, while SetJobAsker is called from the
// setup path.
var (
	jobAskerMu sync.RWMutex
	jobAsker   JobAsker
)

// SetJobAsker wires the "ask_parent" tool to a JobAsker. Called once from
// main() with an adapter over the app's shared jobs.Registry.
func SetJobAsker(a JobAsker) {
	jobAskerMu.Lock()
	jobAsker = a
	jobAskerMu.Unlock()
}

// getJobAsker copies the current JobAsker out under RLock, so a caller never
// holds the lock while calling into the interface (see bgbash.go's
// jobNotifierMu doc comment for why that matters).
func getJobAsker() JobAsker {
	jobAskerMu.RLock()
	defer jobAskerMu.RUnlock()
	return jobAsker
}

// AskTool implements the "ask_parent" tool: lets a running job (any subagent
// call, blocking or async, or a /btw side-conversation) pose a question to its
// parent. The question is published on the bus and the tool returns at once
// with the question id. The answer arrives later as a message (ask.reply).
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

	asker := getJobAsker()
	if asker == nil {
		return ToolResult{Type: "result", Success: false, Error: "ask_parent unavailable: job registry not configured"}
	}

	qid, ok := asker.Ask(ctx, jobID, question)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "ask_parent could not send the question; proceed with your best judgement or state your assumption and continue"}
	}
	return ToolResult{
		Type:    "result",
		Success: true,
		Content: "question sent, id=" + qid + "; the answer arrives as a message. Call wait(kinds=[\"ask.reply\"]) only if the answer blocks your work.",
	}
}

// JobAnswerer is the local contract the "answer_job" tool needs — set once
// from main() via SetJobAnswerer.
type JobAnswerer interface {
	// Answer sends text as the answer to question id ("q<Seq>"). It returns
	// an error when id is unknown or already answered.
	Answer(ctx context.Context, id, text string) error
}

// jobAnswerer is nil until SetJobAnswerer is called. Guarded the same way
// jobAsker is above.
var (
	jobAnswererMu sync.RWMutex
	jobAnswerer   JobAnswerer
)

// SetJobAnswerer wires the "answer_job" tool to a JobAnswerer.
func SetJobAnswerer(a JobAnswerer) {
	jobAnswererMu.Lock()
	jobAnswerer = a
	jobAnswererMu.Unlock()
}

// getJobAnswerer copies the current JobAnswerer out under RLock — see
// getJobAsker's doc comment above for why.
func getJobAnswerer() JobAnswerer {
	jobAnswererMu.RLock()
	defer jobAnswererMu.RUnlock()
	return jobAnswerer
}

// AnswerTool implements the "answer_job" tool: sends a real answer to a
// question (id q<N>, as returned by ask_parent). The answer is sent to the
// child that asked, as an ask.reply message. Either something the calling
// agent genuinely already knows, or something the human actually said
// elsewhere, never a stand-in invented for a human who has not replied.
// This tool has NO job-ctx gating: anyone (the main thread or another agent)
// can answer a question.
type AnswerTool struct{}

func (t *AnswerTool) Name() string { return "answer_job" }

func (t *AnswerTool) Run(ctx context.Context, input map[string]any) ToolResult {
	jobID, _ := input["job_id"].(string)
	if jobID == "" {
		return validationResult("job_id is required (the question id, q<N>)")
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
		requester := getJobExtensionRequester()
		if requester == nil {
			return ToolResult{Type: "result", Success: false, Error: "answer_job unavailable: job extension requester not configured"}
		}
		if !requester.ResolveExtension(jobID, requestID, approve) {
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

	answerer := getJobAnswerer()
	if answerer == nil {
		return ToolResult{Type: "result", Success: false, Error: "answer_job unavailable: job registry not configured"}
	}

	if err := answerer.Answer(ctx, jobID, text); err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	return ToolResult{Type: "result", Success: true, Content: "answer delivered; the job does not wait for it"}
}
