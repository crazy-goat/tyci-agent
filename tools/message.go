package tools

import (
	"context"
	"fmt"
	"sync"
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
	// Returns false when id is unknown or terminal; callers should Resolve
	// first when they need to distinguish those cases.
	Post(id, text string) bool
	// IsLive reports whether id identifies a running or waiting job. Terminal
	// jobs remain resolvable for resume, but cannot receive new messages.
	IsLive(id string) bool
	// Drain pops and returns everything queued for job id via Post, FIFO,
	// clearing the mailbox. nil for an unknown id or an empty mailbox.
	Drain(id string) []string
}

// jobMailbox is nil until SetJobMailbox is called; the "message" tool fails
// loudly (not silently) until then, and JobMailboxNextMessages returns nil
// so wiring it into a child's agent.Config is a harmless no-op. Guarded by
// jobMailboxMu for the same reason jobNotifier is (see bgbash.go's
// jobNotifierMu doc comment): the NextMessages closure it backs runs from a
// job's own agent loop goroutine, which outlives the tool call that started
// it, while SetJobMailbox is called from the setup path.
var (
	jobMailboxMu sync.RWMutex
	jobMailbox   JobMailbox
)

// SetJobMailbox wires the "message" tool and the per-job NextMessages drain
// to a JobMailbox. Called once from main() with an adapter over the app's
// shared jobs.Registry.
func SetJobMailbox(m JobMailbox) {
	jobMailboxMu.Lock()
	jobMailbox = m
	jobMailboxMu.Unlock()
}

// getJobMailbox copies the current JobMailbox out under RLock — see
// getJobAsker's doc comment (ask.go) for why callers never hold the lock
// while calling into the interface.
func getJobMailbox() JobMailbox {
	jobMailboxMu.RLock()
	defer jobMailboxMu.RUnlock()
	return jobMailbox
}

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
	if getJobMailbox() == nil || jobID == "" {
		return nil
	}
	// The returned closure re-reads the mailbox via getJobMailbox on every
	// call rather than capturing the pointer above — it runs from a job's
	// own long-lived agent loop, so it must never hold a stale copy across
	// however many drains happen over that job's lifetime.
	return func() []string {
		mailbox := getJobMailbox()
		if mailbox == nil {
			return nil
		}
		return mailbox.Drain(jobID)
	}
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
		return validationResult("job_id is required")
	}
	text, _ := input["text"].(string)
	if text == "" {
		return validationResult("text is required")
	}

	mailbox := getJobMailbox()
	if mailbox == nil {
		return ToolResult{Type: "result", Success: false, Error: "message unavailable: job registry not configured"}
	}

	jobID, ok := mailbox.Resolve(rawID)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("unknown job_id %q; message only targets live jobs, and an unknown job cannot be resumed. Use a job_id from subagent/wait or its short #N form from the jobs panel", rawID)}
	}

	if !mailbox.IsLive(jobID) {
		return ToolResult{Type: "result", Success: false, Error: terminalMessageError(jobID)}
	}
	if !mailbox.Post(jobID, text) {
		return ToolResult{Type: "result", Success: false, Error: terminalMessageError(jobID)}
	}
	return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("message queued for job %s; it will see it at its next iteration", jobID)}
}

func terminalMessageError(jobID string) string {
	return fmt.Sprintf("agent/job %q is no longer running; message only targets live jobs. Use resume(job_id, task) — for example resume(job_id=%q, task=\"...\") — to continue its finished transcript", jobID, jobID)
}
