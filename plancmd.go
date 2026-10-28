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
	// plannerTurns maps the id of a resumed planner turn that runs now to the
	// id of its planner job. A running turn has no resumable entry yet (that
	// is written when its run ends), so this map is what finds it. Guarded by
	// plannerSessionsMu.
	plannerTurns = map[string]string{}
)

// registerPlannerTurn records that the running job jobID continues the planner
// job root.
func registerPlannerTurn(jobID, root string) {
	plannerSessionsMu.Lock()
	defer plannerSessionsMu.Unlock()
	plannerTurns[jobID] = root
}

// unregisterPlannerTurn forgets a turn that ended.
func unregisterPlannerTurn(jobID string) {
	plannerSessionsMu.Lock()
	defer plannerSessionsMu.Unlock()
	delete(plannerTurns, jobID)
}

// registerPlannerSession keeps the session of the planner job jobID open.
func registerPlannerSession(jobID string, e plannerEntry) {
	plannerSessionsMu.Lock()
	defer plannerSessionsMu.Unlock()
	plannerSessions[jobID] = e
}

// plannerOwnerOf returns the open planner session of jobID, the id of its
// planner job, and true. The job is the planner job itself, a running resumed
// turn, or a finished job of the conversation chain of a planner.
func plannerOwnerOf(jobID string) (plannerEntry, string, bool) {
	ids := chainOf(jobID)
	plannerSessionsMu.Lock()
	defer plannerSessionsMu.Unlock()
	if root, ok := plannerTurns[jobID]; ok {
		if e, ok := plannerSessions[root]; ok {
			return e, root, true
		}
	}
	root, ok := ownerOf(ids, plannerSessions)
	if !ok {
		return plannerEntry{}, "", false
	}
	return plannerSessions[root], root, true
}

// plannerEntryOf is plannerOwnerOf without the id of the planner job.
func plannerEntryOf(jobID string) (plannerEntry, bool) {
	e, _, ok := plannerOwnerOf(jobID)
	return e, ok
}

// chainOf returns the job ids of the conversation chain of jobID.
func chainOf(jobID string) []string {
	resumableMu.Lock()
	defer resumableMu.Unlock()
	return chainIDs(jobID, resumable[jobID])
}

// ownerOf returns the key of open that is one of ids: the planner job that
// started the session of a chain.
func ownerOf(ids []string, open map[string]plannerEntry) (string, bool) {
	for _, id := range ids {
		if _, ok := open[id]; ok {
			return id, true
		}
	}
	return "", false
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
// exits. Before a session is closed, every live job that belongs to it is
// cancelled and waited for: its planner job, a running resumed turn, and a
// job of its conversation chain. So a turn that still writes stops first. A
// clean worktree is removed. A kept worktree gets a notice with its path on w.
// A close error is printed on w too.
func closePlannerSessions(w io.Writer) {
	plannerSessionsMu.Lock()
	open := plannerSessions
	turns := make(map[string]string, len(plannerTurns))
	for id, root := range plannerTurns {
		turns[id] = root
	}
	plannerSessions = map[string]plannerEntry{}
	plannerSessionsMu.Unlock()

	live := map[string][]string{} // planner job id -> live jobs of its sessions
	for _, job := range JobRegistry.List() {
		if !JobRegistry.IsLive(job.ID) {
			continue
		}
		root, ok := turns[job.ID]
		if !ok {
			root, ok = ownerOf(chainOf(job.ID), open)
		}
		if ok {
			live[root] = append(live[root], job.ID)
		}
	}

	ids := make([]string, 0, len(open))
	for id := range open {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, jobID := range live[id] {
			JobRegistry.Cancel(jobID)
		}
		for _, jobID := range live[id] {
			JobRegistry.Wait(context.Background(), jobID, planCloseWait)
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
