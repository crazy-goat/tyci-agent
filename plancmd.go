package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/planner"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/tools"
)

// plannerRole is the builtin role that a /plan job runs.
const plannerRole = "planner"

// planDefaultTopic is the first message of a /plan that has no topic.
const planDefaultTopic = "The user did not name a topic. Ask the user what to plan."

// planRepoInfo finds the repository of /plan. Tests replace it.
var planRepoInfo = func() (flow.RepoInfo, error) { return workflowManager.Info() }

// planNew starts a planner session. Tests replace it with a stub.
var planNew = planner.New

// planSpawn runs the planner agent of a /plan job. Tests replace it with a stub.
var planSpawn = tools.RunSubagentTask

// planNotify shows a notice in the TUI. Tests replace it.
var planNotify = func(text string) { publishNotice(appBus, "", text, false) }

// plannerJobs holds the ids of the jobs that run a planner. A planner job ends
// after its answer, and its worktree may be removed then. So no job of this
// set can be resumed, see agentRunWorkdir.
var (
	plannerJobsMu sync.Mutex
	plannerJobs   = map[string]bool{}
)

// errPlannerNotResumable is the refusal to resume a planner job.
var errPlannerNotResumable = errors.New("a planner job cannot be resumed: start a new planner with /plan")

func markPlannerJob(jobID string) {
	plannerJobsMu.Lock()
	defer plannerJobsMu.Unlock()
	plannerJobs[jobID] = true
}

func isPlannerJob(jobID string) bool {
	plannerJobsMu.Lock()
	defer plannerJobsMu.Unlock()
	return plannerJobs[jobID]
}

// handlePlanCommand implements "/plan [topic]". A planner is not a run, so
// the command takes no worker slot. Like the other commands that do not run
// the agent turn, it resets the status first.
func handlePlanCommand(disp slashCommandDisplay, arg string, start func(topic string) error) {
	disp.ResetStatus()
	if err := start(strings.TrimSpace(arg)); err != nil {
		disp.Error(fmt.Errorf("/plan: %v", err))
	}
}

// startPlanner creates a planner session in its own worktree and starts its
// job with topic. It returns the error that stops the start, and then no job
// starts. The job ends with its agent. Then the session is closed.
func startPlanner(ctx context.Context, topic string) error {
	info, err := planRepoInfo()
	if err != nil {
		return err
	}
	cfg, err := flowconfig.Load(info.Home, info.Root, info.Trusted)
	if err != nil {
		return err
	}
	rl, err := cfg.Role(plannerRole)
	if err != nil {
		return err
	}
	model, err := cfg.ResolveModel(rl)
	if err != nil {
		return err
	}
	fallbacks, err := cfg.ResolveFallbacks(rl)
	if err != nil {
		return err
	}
	s, err := planNew(info.MainRoot)
	if err != nil {
		return err
	}
	if topic == "" {
		topic = planDefaultTopic
	}
	policy := tools.Policy{Role: plannerRole, Permission: rl.PermissionMode(), Network: rl.NetworkOn(), Tools: rl.Tools, Dir: s.Worktree}
	spec := tools.TaskSpec{
		Task:         topic,
		Model:        model,
		SystemPrompt: rl.Prompt,
		Dir:          s.Worktree,
		SoftLimit:    rl.CompactSoftLimit,
		HardLimit:    rl.CompactHardLimit,
		Effort:       cfg.ResolveEffort(rl),
		Policy:       &policy,
		Fallbacks:    fallbacks,
	}
	job := JobRegistry.Start(ctx, "plan-"+s.ID, jobs.KindSubagent, "", func(jobCtx context.Context, jobID string) (string, bool, error) {
		markPlannerJob(jobID)
		jobCtx = context.WithValue(jobCtx, tools.JobIDCtxKey{}, jobID)
		res, _, runErr := planSpawn(jobCtx, spec)
		closePlannerSession(s)
		return res, false, runErr
	})
	planNotify(fmt.Sprintf("planner %s started in %s", job.ID, s.Worktree))
	return nil
}

// closePlannerSession ends the session of a finished planner job. It shows a
// notice when the worktree is kept, and the error when the close fails.
func closePlannerSession(s *planner.Session) {
	kept, err := s.Close()
	switch {
	case err != nil:
		planNotify(fmt.Sprintf("/plan: close planner session plan-%s: %v", s.ID, err))
	case kept:
		planNotify(fmt.Sprintf("planner session plan-%s kept: its worktree is %s", s.ID, s.Worktree))
	}
}
