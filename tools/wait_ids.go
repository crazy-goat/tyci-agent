package tools

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// waitIDs returns the job ids that a wait call names, in order and without
// repeats. "ids" is the list, and "job_id" is the older single-id alias; both
// may be given. multi is true when the call uses the list form ("ids" given,
// or more than one id), which the multi-job path serves. A lone job_id keeps
// the single-job path and its result text. A malformed entry is errText.
func waitIDs(input map[string]any) (ids []string, multi bool, errText string) {
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if raw, ok := input["ids"]; ok {
		multi = true
		list, ok := raw.([]any)
		if !ok {
			if sl, isStrings := raw.([]string); isStrings {
				list = make([]any, len(sl))
				for i, s := range sl {
					list[i] = s
				}
			} else {
				return nil, false, "ids must be an array of job_id strings"
			}
		}
		for _, v := range list {
			s, ok := v.(string)
			if !ok || s == "" {
				return nil, false, "ids must contain only non-empty job_id strings"
			}
			add(s)
		}
	}
	if alias, _ := input["job_id"].(string); alias != "" {
		add(alias)
	}
	if len(ids) > 1 {
		multi = true
	}
	return ids, multi, ""
}

// runIDs waits on several jobs at once. mode "any" ends when the first job
// finishes or blocks on a question; mode "all" ends when every job does. It
// asks each job with a zero timeout and sleeps one poll slice between rounds,
// so it ends early for the same reasons as a single-job wait: someone typed,
// or a new notice arrived. The per-job access rules match a single-job wait.
func (t *WaitTool) runIDs(ctx context.Context, callerJobID string, ids []string, mode string, total time.Duration, clampedNote string, arrived func() bool) ToolResult {
	if len(ids) == 0 {
		return ToolResult{Type: "result", Success: false, Error: "ids is empty; pass at least one job id"}
	}
	waiter := t.waiter()
	if waiter == nil {
		return ToolResult{Type: "result", Success: false, Error: "job registry unavailable; omit job_id to just wait N seconds"}
	}
	lister := getJobLister()
	inChild := ctx.Value(SubagentSinkCtxKey{}) != nil
	for _, id := range ids {
		fullID, known := resolveListedJob(lister, id)
		if inChild && !known {
			return ToolResult{Type: "result", Success: false, Error: unknownJobIDError}
		}
		if !inOwnSubtree(ctx, callerJobID, fullID, lister) {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("refused: job %q is not within your own subtree — inside a subagent you may wait only on jobs you started (your job id is %q)", id, callerJobID)}
		}
	}

	deadline := time.Now().Add(total)
	for {
		statuses := make([]JobStatus, len(ids))
		settled := 0
		for i, id := range ids {
			st, ok := waiter.Wait(ctx, id, 0)
			if !ok {
				return ToolResult{Type: "result", Success: false, Error: unknownJobIDError}
			}
			statuses[i] = st
			if st.Done {
				settled++
			}
		}
		if (mode == "any" && settled > 0) || settled == len(ids) {
			return waitIDsResult(ids, statuses, clampedNote, false)
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return waitIDsResult(ids, statuses, clampedNote, true)
		}
		if UserPending() {
			return ToolResult{Type: "result", Success: true, Content: "stopped waiting on jobs because someone typed — read what they said and answer them. The jobs were not touched and are still running."}
		}
		if arrived() {
			return ToolResult{Type: "result", Success: true, Content: "stopped waiting on jobs because a new notice arrived; read it. The jobs were not touched."}
		}
		select {
		case <-ctx.Done():
		case <-time.After(jobPollInterval):
		}
	}
}

// waitIDsResult lists the state of every job, one line each. timedOut is set
// when the wait ended before the mode was satisfied.
func waitIDsResult(ids []string, statuses []JobStatus, clampedNote string, timedOut bool) ToolResult {
	lines := make([]string, 0, len(ids))
	for i, id := range ids {
		st := statuses[i]
		switch {
		case st.Done && st.Success:
			lines = append(lines, fmt.Sprintf("job_id=%s: finished: %s", id, st.Content))
		case st.Done:
			lines = append(lines, fmt.Sprintf("job_id=%s: failed: %s", id, st.Error))
		default:
			lines = append(lines, fmt.Sprintf("job_id=%s: still running", id))
		}
	}
	content := strings.Join(lines, "\n")
	if timedOut {
		content += "\nThe wait ended before the mode was satisfied; the jobs above are not touched and you will be notified when they finish."
	}
	return ToolResult{Type: "result", Success: true, Content: content + clampedNote}
}

// MaxParallel keeps wait calls of one batch in order. A wait does not change
// state, but two long waits in one batch would only block each other.
func (t *WaitTool) MaxParallel() int { return 1 }
