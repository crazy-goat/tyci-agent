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
}

// RunSubagentTask runs spec through the registered subagent runner in
// spec.Dir, without creating a worktree. It returns the final answer and a
// fresh id for this run. Child runs have no persisted session today, so the
// id is only a unique label for the run history.
func RunSubagentTask(ctx context.Context, s TaskSpec) (result, sessionID string, err error) {
	if subagentToolInstance == nil || subagentToolInstance.Runner == nil {
		return "", "", errors.New("no subagent runner is set")
	}
	return runSubagentTask(ctx, subagentToolInstance.Runner, s)
}

func runSubagentTask(ctx context.Context, runner SubAgentRunner, s TaskSpec) (string, string, error) {
	t := subagentTask{Task: s.Task, Model: s.Model, systemPrompt: s.SystemPrompt}
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
