package tools

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// JobExtensionRequester is the local contract the
// "request_timeout_extension" and "answer_job" tools need for timeout
// extension requests. The tools package deliberately does not import jobs;
// the caller supplies an implementation over its shared job registry.
type JobExtensionRequester interface {
	// RequestExtension registers a request to extend id by seconds, with the
	// supplied reason. It returns a request id and ok=false when id is unknown
	// or cannot accept another request.
	RequestExtension(id string, seconds time.Duration, reason string) (requestID string, ok bool)

	// WaitExtension blocks until the request is approved or rejected, or ctx is
	// done. ok=false means no answer was available before the wait ended.
	WaitExtension(ctx context.Context, id, requestID string) (approved bool, ok bool)

	// ResolveExtension approves or rejects a pending request. It returns false
	// when the request is unknown or has already been resolved.
	ResolveExtension(id, requestID string, approve bool) bool
}

// jobExtensionRequester is nil until SetJobExtensionRequester is called.
var jobExtensionRequester JobExtensionRequester

// SetJobExtensionRequester wires timeout-extension tools to the shared job
// registry. Called once from the composition root.
func SetJobExtensionRequester(r JobExtensionRequester) {
	jobExtensionRequester = r
}

const maxTimeoutExtensionSeconds = 600

// RequestTimeoutExtensionTool lets a child ask for a bounded extension of its
// own timeout. The request is routed by the child job id in ctx and blocks
// until its parent approves or rejects it.
type RequestTimeoutExtensionTool struct{}

func (t *RequestTimeoutExtensionTool) Name() string { return "request_timeout_extension" }

func (t *RequestTimeoutExtensionTool) Run(ctx context.Context, input map[string]any) ToolResult {
	seconds, ok := extensionSeconds(input["seconds"])
	if !ok || seconds <= 0 || seconds > maxTimeoutExtensionSeconds {
		return validationResultf("seconds must be a positive integer no greater than %d", maxTimeoutExtensionSeconds)
	}

	reason, _ := input["reason"].(string)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return validationResult("reason is required and must be nonempty")
	}

	jobID, ok := ctx.Value(JobIDCtxKey{}).(string)
	if !ok || jobID == "" {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   "request_timeout_extension only works inside a job (a subagent call, or a /btw side-conversation) — this call has no job id",
		}
	}

	if jobExtensionRequester == nil {
		return ToolResult{Type: "result", Success: false, Error: "request_timeout_extension unavailable: job registry not configured"}
	}

	requestID, ok := jobExtensionRequester.RequestExtension(jobID, time.Duration(seconds)*time.Second, reason)
	if !ok || requestID == "" {
		return ToolResult{Type: "result", Success: false, Error: "could not register a timeout extension request for this job"}
	}

	// The request is FOR jobID's own parent — the one who can call
	// answer_job/resolve_extension on it — not for jobID's own mailbox
	// (which nothing but jobID's own agent loop ever reads).
	notifyToParent(parentIDOf(jobID), fmt.Sprintf("[timeout extension] request pending: job_id=%q request_id=%q seconds=%d reason=%q", jobID, requestID, seconds, reason))

	approved, answered := jobExtensionRequester.WaitExtension(ctx, jobID, requestID)
	if !answered {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("timeout extension request %q was rejected or no answer arrived", requestID)}
	}
	if !approved {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("timeout extension request %q was rejected", requestID)}
	}
	return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("timeout extension approved: %d seconds", seconds)}
}

func extensionSeconds(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		return int(v), int64(int(v)) == v
	case uint:
		return int(v), uint(int(v)) == v
	case uint8:
		return int(v), true
	case uint16:
		return int(v), true
	case uint32:
		return int(v), uint32(int(v)) == v
	case uint64:
		return int(v), uint64(int(v)) == v
	case float32:
		i := int(v)
		return i, float32(i) == v
	case float64:
		i := int(v)
		return i, float64(i) == v
	default:
		return 0, false
	}
}
