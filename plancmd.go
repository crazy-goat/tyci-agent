package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

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

// planCloseWait bounds the wait for a planner job that is still running when
// tyci exits, before its session is closed.
const planCloseWait = 3 * time.Second

// planRepoInfo finds the repository of /plan. Tests replace it.
var planRepoInfo = func() (flow.RepoInfo, error) { return workflowManager.Info() }

// planNew starts a planner session. Tests replace it with a stub.
var planNew = planner.New

// planSpawn runs the planner agent of a /plan job. Tests replace it with a stub.
var planSpawn = tools.RunSubagentTask

// planNotify shows a notice in the TUI. Tests replace it.
var planNotify = func(text string) { publishNotice(appBus, "", text, false) }

// plannerEntry is an open planner session and the policy of its role. The
// session stays open until tyci exits, so the planner can be resumed.
type plannerEntry struct {
	session *planner.Session
	policy  tools.Policy
}

// plannerSessions holds the open planner sessions. The key is the id of the
// planner job that started the session. A resumed job is found through its
// conversation chain (see plannerEntryOf).
var (
	plannerSessionsMu sync.Mutex
	plannerSessions   = map[string]plannerEntry{}
)

// registerPlannerSession keeps the session of the planner job jobID open.
func registerPlannerSession(jobID string, e plannerEntry) {
	plannerSessionsMu.Lock()
	defer plannerSessionsMu.Unlock()
	plannerSessions[jobID] = e
}

// plannerEntryOf returns the open planner session of jobID or of a job in its
// conversation chain, so a resumed planner gets the worktree of its session.
func plannerEntryOf(jobID string) (plannerEntry, bool) {
	resumableMu.Lock()
	ids := chainIDs(jobID, resumable[jobID])
	resumableMu.Unlock()
	plannerSessionsMu.Lock()
	defer plannerSessionsMu.Unlock()
	for _, id := range ids {
		if e, ok := plannerSessions[id]; ok {
			return e, true
		}
	}
	return plannerEntry{}, false
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
// starts. The job ends with its answer. The session stays open until tyci
// exits (see closePlannerSessions).
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
		jobCtx = context.WithValue(jobCtx, tools.JobIDCtxKey{}, jobID)
		res, _, runErr := planSpawn(jobCtx, spec)
		return res, false, runErr
	})
	registerPlannerSession(job.ID, plannerEntry{session: s, policy: policy})
	planNotify(fmt.Sprintf("planner %s started in %s", job.ID, s.Worktree))
	return nil
}

// closePlannerSessions ends every open planner session. It runs when tyci
// exits. A planner job that still runs is cancelled first, and its session is
// closed after it stops or after planCloseWait. A clean worktree is removed.
// A kept worktree gets a notice with its path on w. A close error is printed
// on w too.
func closePlannerSessions(w io.Writer) {
	plannerSessionsMu.Lock()
	open := plannerSessions
	plannerSessions = map[string]plannerEntry{}
	plannerSessionsMu.Unlock()

	ids := make([]string, 0, len(open))
	for id := range open {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if JobRegistry.IsLive(id) {
			JobRegistry.Cancel(id)
			JobRegistry.Wait(context.Background(), id, planCloseWait)
		}
		e := open[id]
		kept, err := e.session.Close()
		switch {
		case err != nil:
			fmt.Fprintf(w, "/plan: close planner session %s: %v\n", id, err)
		case kept:
			fmt.Fprintf(w, "planner session %s kept: its worktree is %s\n", id, e.session.Worktree)
		}
	}
}
