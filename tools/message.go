package tools

import (
	"context"
	"fmt"
)

// JobMailbox is the local contract behind the "message" tool and the
// "/msg" slash command: Resolve maps a job's full id or its jobs-panel
// short form ("#N") to the full id; Post enqueues a message for delivery to
// that job's own agent loop at its next iteration boundary; Drain pops
// everything queued for a job, called from that job's own loop. Same
// layering rule as JobAsker/JobActivityToucher/etc above: this package
// never imports "jobs" directly, so a future jobs.Registry satisfies this
// structurally. Wired once from main() via SetJobMailbox over the app's
// shared jobs.Registry.
type JobMailbox interface {
	// Resolve maps id (a full job id, or its short "#N" form) to the job's
	// full id. ok is false when nothing matches.
	Resolve(id string) (full string, ok bool)
	// Post enqueues text for delivery to job id's next iteration boundary.
	// Returns false when id is unknown.
	Post(id, text string) bool
	// Drain pops and returns everything queued for job id via Post, FIFO,
	// clearing the mailbox. nil for an unknown id or an empty mailbox.
	Drain(id string) []string
}

// jobMailbox is nil until SetJobMailbox is called; the "message" tool fails
// loudly (not silently) until then, and JobMailboxNextMessages returns nil
// so wiring it into a child's agent.Config is a harmless no-op.
var jobMailbox JobMailbox

// SetJobMailbox wires the "message" tool and the per-job NextMessages drain
// to a JobMailbox. Called once from main() with an adapter over the app's
// shared jobs.Registry.
func SetJobMailbox(m JobMailbox) { jobMailbox = m }

// JobMailboxNextMessages returns a NextMessages-shaped callback bound to
// jobID: calling it drains exactly the messages posted to jobID's mailbox
// since the last call, FIFO. Meant to be assigned to a background
// subagent's own agent.Config.NextMessages, so its agent loop picks up a
// posted message at its next iteration boundary — the same mechanism the
// main agent already uses to drain its pending-input queue (see
// agent.Config.NextMessages's doc comment and agent.Run's drain after every
// runOnce).
//
// Returns nil when jobID is empty or no mailbox is wired, so a caller can
// assign the result to cfg.NextMessages unconditionally without a nil
// check: agent.Run already treats a nil NextMessages as "nothing to drain".
func JobMailboxNextMessages(jobID string) func() []string {
	if jobMailbox == nil || jobID == "" {
		return nil
	}
	return func() []string { return jobMailbox.Drain(jobID) }
}

// MessageTool implements the "message" tool: lets the parent model post a
// message to one of its own running children (a background subagent job,
// or a /btw side-conversation), delivered at that job's next iteration
// boundary — mirroring "/msg" (the human-facing equivalent) and the
// mechanism the main agent's own pending-input queue already uses.
//
// Denied to a plain subagent child (see subagentDeniedTools in
// toolgate.go): a plain child can never spawn further children of its own
// (the "subagent" tool is denied to it), so it can never have a job to
// message in the first place. Only something that still holds "subagent" —
// the main agent, or a /btw side-conversation, which gets the full
// unrestricted schema — has any use for this tool.
type MessageTool struct{}

func (t *MessageTool) Name() string { return "message" }

func (t *MessageTool) Run(ctx context.Context, input map[string]any) ToolResult {
	rawID, _ := input["job_id"].(string)
	if rawID == "" {
		return ToolResult{Type: "result", Success: false, Error: "job_id is required"}
	}
	text, _ := input["text"].(string)
	if text == "" {
		return ToolResult{Type: "result", Success: false, Error: "text is required"}
	}

	if jobMailbox == nil {
		return ToolResult{Type: "result", Success: false, Error: "message unavailable: job registry not configured"}
	}

	jobID, ok := jobMailbox.Resolve(rawID)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("unknown job_id %q — use the exact id a subagent/wait/resume call gave you, or its short #N form from the jobs panel", rawID)}
	}

	if !jobMailbox.Post(jobID, text) {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("job %q not found", jobID)}
	}
	return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("message queued for job %s; it will see it at its next iteration", jobID)}
}
