package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/crazy-goat/tyci-agent/stream"
)

// TaskSpec describes one subagent run started from Go (the workflow engine),
// not by a model. None of these fields are visible to a model.
type TaskSpec struct {
	Task          string
	Model         string // provider URI
	SystemPrompt  string
	Dir           string // existing directory; becomes the child's working directory
	MaxIterations int
	// SoftLimit and HardLimit are context limits in tokens; 0 = global defaults.
	SoftLimit int
	HardLimit int
	// Name, when set, registers the run as a job with this description, so
	// the jobs list, "message" and "resume" can reach it by this name.
	Name string
	// Resume, when set, is the session id of an earlier named run. Task is
	// then sent as a new user turn in that conversation; the other fields
	// except Dir are ignored.
	Resume string
	// OnDone, when set, receives the usage of the run after it ends (also
	// after an error). The workflow runner stores it in the run history.
	OnDone func(TaskStats)
}

// TaskStats is what a finished subagent run reports: the model it used and
// its usage summed over all turns.
type TaskStats struct {
	Model     string
	Usage     stream.Usage
	Turns     int
	ToolCalls int
}

// RunSubagentTask runs spec through the registered subagent runner in
// spec.Dir, without creating a worktree. It returns the final answer and a
// session id. For a named run the session id is its job id, which
// TaskSpec.Resume accepts; otherwise it is a fresh unique label.
func RunSubagentTask(ctx context.Context, s TaskSpec) (result, sessionID string, err error) {
	if s.Resume != "" {
		return resumeSubagentTask(ctx, s)
	}
	if subagentToolInstance == nil || subagentToolInstance.Runner == nil {
		return "", "", errors.New("no subagent runner is set")
	}
	runner := subagentToolInstance.Runner
	starter := getJobStarter()
	if s.Name == "" || starter == nil {
		return runSubagentTask(ctx, runner, s)
	}
	var res, id string
	runErr := errors.New("role agent ended without a result")
	done := make(chan struct{})
	starter.Start(ctx, s.Name, JobKindSubagent, "", func(jobCtx context.Context, jobID string) (string, bool, error) {
		defer close(done)
		// Nobody answers ask_parent for a role agent (parentID is empty), so
		// mark the job as unroutable: ask_parent then fails at once.
		jobCtx = context.WithValue(jobCtx, JobIDCtxKey{}, jobID)
		jobCtx = context.WithValue(jobCtx, AskUnroutableCtxKey{}, true)
		res, _, runErr = runSubagentTask(jobCtx, runner, s)
		id = jobID
		return res, false, runErr
	})
	<-done
	return res, id, runErr
}

// resumeSubagentTask sends s.Task to the finished conversation s.Resume and
// waits for the answer. The new job id is the new session id.
func resumeSubagentTask(ctx context.Context, s TaskSpec) (string, string, error) {
	resumer := getJobResumer()
	var waiter JobWaiter
	if tool, ok := lookupTool("wait"); ok {
		if wt, ok := tool.(*WaitTool); ok {
			waiter = wt.waiter()
		}
	}
	if resumer == nil || waiter == nil {
		return "", "", errors.New("resume unavailable: job registry not configured")
	}
	// Same rule as a named run: nobody answers ask_parent for a role agent.
	jobCtx := context.WithValue(WithWorkdir(ctx, s.Dir), AskUnroutableCtxKey{}, true)
	h, err := resumer.Resume(jobCtx, s.Resume, s.Task)
	if err != nil {
		return "", "", err
	}
	id := h.ID()
	for {
		st, ok := waiter.Wait(ctx, id, time.Minute)
		if !ok {
			return "", id, fmt.Errorf("resumed job %s is unknown", id)
		}
		if st.Done {
			if !st.Success {
				return st.Content, id, errors.New(st.Error)
			}
			return st.Content, id, nil
		}
		if ctx.Err() != nil {
			if c := getJobCanceler(); c != nil {
				c.Cancel(id)
			}
			return "", id, ctx.Err()
		}
	}
}

func runSubagentTask(ctx context.Context, runner SubAgentRunner, s TaskSpec) (string, string, error) {
	t := subagentTask{Task: s.Task, Model: s.Model, systemPrompt: s.SystemPrompt, softLimit: s.SoftLimit, hardLimit: s.HardLimit}
	if s.MaxIterations > 0 {
		t.MaxIterations = &s.MaxIterations
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	id := hex.EncodeToString(b)
	res := runSingleTask(WithWorkdir(ctx, s.Dir), runner, t, 0, false)
	if s.OnDone != nil {
		s.OnDone(TaskStats{Model: res.Model, Usage: res.Usage, Turns: res.Turns, ToolCalls: res.ToolCalls})
	}
	if !res.Success {
		return res.Content, id, errors.New(res.Error)
	}
	return res.Content, id, nil
}
