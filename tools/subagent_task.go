package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
}

// RunSubagentTask runs spec through the registered subagent runner in
// spec.Dir, without creating a worktree. It returns the final answer and a
// fresh id for this run. Child runs that run as jobs write a session file
// under the "agents" directory; the id is only a unique label for the run history.
func RunSubagentTask(ctx context.Context, s TaskSpec) (result, sessionID string, err error) {
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
		res, id, runErr = runSubagentTask(jobCtx, runner, s)
		return res, false, runErr
	})
	<-done
	return res, id, runErr
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
	if !res.Success {
		return res.Content, id, errors.New(res.Error)
	}
	return res.Content, id, nil
}
