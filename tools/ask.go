package tools

import (
	"context"
)

// JobAsker is the local contract the "ask" tool needs — set once from
// main() via SetJobAsker, over the app's shared jobs.Registry. Same
// layering rule as JobWaiter/JobStarter (see wait.go/subagent.go's doc
// comments): this package never imports "jobs" directly.
type JobAsker interface {
	// Ask blocks until answer arrives (via Answer) or ctx is done, returning
	// ok=false in the latter case. id is unknown -> ok=false immediately.
	Ask(ctx context.Context, id, question string) (answer string, ok bool)
}

// jobAsker is nil until SetJobAsker is called; the "ask" tool fails loudly
// (not silently blocking or panicking) until then.
var jobAsker JobAsker

// SetJobAsker wires the "ask" tool to a JobAsker. Called once from main()
// with an adapter over the app's shared jobs.Registry.
func SetJobAsker(a JobAsker) { jobAsker = a }

// AskTool implements the "ask" tool: lets a running background job (an
// async subagent or a /btw side-conversation) pose a blocking question to
// whoever is watching it, and block until answered.
type AskTool struct{}

func (t *AskTool) Name() string { return "ask" }

func (t *AskTool) Run(ctx context.Context, input map[string]any) ToolResult {
	question, _ := input["question"].(string)
	if question == "" {
		return ToolResult{Type: "result", Success: false, Error: "question is required"}
	}

	jobID, ok := ctx.Value(JobIDCtxKey{}).(string)
	if !ok || jobID == "" {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   "ask only works inside an async background job (started via subagent(...,async:true) or a /btw side-conversation)",
		}
	}

	if jobAsker == nil {
		return ToolResult{Type: "result", Success: false, Error: "ask unavailable: job registry not configured"}
	}

	answer, ok := jobAsker.Ask(ctx, jobID, question)
	if !ok {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   "no answer arrived before this job's own time limit; proceed with your best judgement or state your assumption and continue",
		}
	}
	return ToolResult{Type: "result", Success: true, Content: answer}
}

// JobAnswerer is the local contract the "answer" tool needs — set once from
// main() via SetJobAnswerer, over the app's shared jobs.Registry.
type JobAnswerer interface {
	// Answer delivers text to a job currently blocked in Ask, unblocking it.
	// Returns false if id is unknown or the job isn't currently waiting.
	Answer(id, text string) bool
}

// jobAnswerer is nil until SetJobAnswerer is called.
var jobAnswerer JobAnswerer

// SetJobAnswerer wires the "answer" tool to a JobAnswerer.
func SetJobAnswerer(a JobAnswerer) { jobAnswerer = a }

// AnswerTool implements the "answer" tool: replies to a job currently shown
// as waiting_answer (via "wait") and unblocks it. Unlike "ask", this tool has
// NO job-ctx gating — anyone (the main thread or another agent) can answer
// any job that's currently waiting; that's the point, it's how
// main-thread-mediated or agent-to-agent communication both work.
type AnswerTool struct{}

func (t *AnswerTool) Name() string { return "answer" }

func (t *AnswerTool) Run(ctx context.Context, input map[string]any) ToolResult {
	jobID, _ := input["job_id"].(string)
	if jobID == "" {
		return ToolResult{Type: "result", Success: false, Error: "job_id is required"}
	}
	text, _ := input["text"].(string)
	if text == "" {
		return ToolResult{Type: "result", Success: false, Error: "text is required"}
	}

	if jobAnswerer == nil {
		return ToolResult{Type: "result", Success: false, Error: "answer unavailable: job registry not configured"}
	}

	if !jobAnswerer.Answer(jobID, text) {
		return ToolResult{Type: "result", Success: false, Error: "job_id not found or not currently waiting for an answer"}
	}
	return ToolResult{Type: "result", Success: true, Content: "answer delivered; job resumed"}
}
