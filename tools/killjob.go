package tools

import (
	"context"
	"fmt"
	"strings"
)

// Item 26's kill_job plumbing: the contracts satisfied structurally by
// jobs.Registry through main()'s adapters, plus the inside-a-child safety
// gate. Split off bgbash.go so the bash background plumbing stays about
// bash; this file is only about "which job may be stopped by whom".

// JobCanceler is the kill switch for running jobs, satisfied structurally by
// jobs.Registry (main() wires it via SetJobCanceler). A local, minimal
// contract so this package never imports "jobs" — the same layering rule as
// JobWaiter/JobStarter: a false return must mean "unknown id or not running",
// and the caller decides how to phrase that.
type JobCanceler interface {
	Cancel(id string) bool
}

// jobCanceler is nil until SetJobCanceler is called; KillJobTool then stays
// bash-only (the pre-item-26 behaviour) instead of failing, so tests and
// modes without a registry keep working.
var jobCanceler JobCanceler

// SetJobCanceler wires kill_job's subagent path to a JobCanceler over the
// app's shared jobs.Registry — the same registry every other job hook in
// this package runs on.
func SetJobCanceler(c JobCanceler) { jobCanceler = c }

// JobKindSource exposes just enough of one registered job for the safety
// rule below, without importing "jobs": ID is the full id, ParentID the
// spawning job's id ("" at top level). Satisfied by jobs.Job.
type JobKindSource interface {
	ID() string
	ParentID() string
}

// JobLister hands out every registered job. Satisfied structurally by
// jobs.Registry.List — but that returns []jobs.Job rather than an interface
// slice, so main() wraps it in a tiny adapter (listJobsAdapter in main.go).
type JobLister interface {
	ListJobs() []JobKindSource
}

// jobLister is nil until SetJobLister is called. Unset, the inside-a-child
// subtree check cannot run and refuses everything except the child itself —
// fail closed: without parentage data there is no way to prove a target IS
// yours, and one confused child must not take down unrelated work.
var jobLister JobLister

// SetJobLister wires kill_job's parent walk to the app's shared registry.
func SetJobLister(l JobLister) { jobLister = l }

// subtreeRoot walks parent links up from id and returns the chain's root's
// job id. parents maps child id → parent id ("" when the spawning context
// was not itself a job). A cycle terminates at its first repeat — the entry
// already in seen — so malformed links can only ever shorten the walk, never
// hang it.
func subtreeRoot(parents map[string]string, id string) string {
	seen := make(map[string]bool)
	for {
		parent, ok := parents[id]
		if !ok || parent == "" {
			return id
		}
		if seen[id] {
			return id
		}
		seen[id] = true
		id = parent
	}
}

// killAllowedInsideChild enforces item 26's safety rule for a kill_job call
// made from inside a running child agent (SubagentSinkCtxKey present): it
// may stop ONLY jobs rooted in its own subtree — including itself. The main
// agent (no sink key) may stop anything: everything in the registry is its
// own or its descendants' work. callerJobID is this call site's own job
// (JobIDCtxKey); targetID the resolved victim.
func killAllowedInsideChild(ctx context.Context, callerJobID, targetID string, lister JobLister) bool {
	if ctx.Value(SubagentSinkCtxKey{}) == nil {
		return true
	}
	if targetID == callerJobID {
		return true // stopping yourself is always your own subtree
	}
	if callerJobID == "" || lister == nil {
		return false // fail closed: no way to prove the target is yours
	}
	parents := make(map[string]string)
	for _, j := range lister.ListJobs() {
		parents[j.ID()] = j.ParentID()
	}
	return subtreeRoot(parents, targetID) == callerJobID
}

// Run dispatches one kill_job call. Order matters and is deliberate:
//
//  1. the safety gate runs FIRST, before anything acts — a refusal must
//     never leave a half-stopped target behind;
//  2. bgRegistry (live backgrounded commands, process-group kills) is
//     consulted before the registry so a bash job keeps its pre-item-26
//     path and message unchanged; a finished bash job falls through to the
//     registry cancel below, which then honestly reports "not running";
//  3. jobs.Registry.Cancel stops subagents (and, via its own subtree
//     cascade, every background command they started).
func (t *KillJobTool) Run(ctx context.Context, input map[string]any) ToolResult {
	jobID, _ := input["job_id"].(string)
	if jobID == "" {
		return validationResult("job_id is required")
	}

	// Resolve short forms ("#N"/"N", as the jobs panel prints them) AND a
	// full id to its canonical registry entry. The jobs registry is the one
	// source of truth for "is this a known job", so resolve against
	// jobLister (when wired) rather than jobMailbox: kill_job's domain is
	// the registry, not the message mailbox, and the two may not even be
	// wired. If the lister is nil (no registry: tests, --print mode) the raw
	// id passes through as an unknown target — matching how every tool here
	// degrades when its adapter is unset.
	fullID, known := jobID, false
	if jobLister != nil {
		for _, j := range jobLister.ListJobs() {
			if j.ID() == jobID {
				fullID, known = j.ID(), true
				break
			}
			short := strings.TrimPrefix(jobID, "#")
			if shortID(j.ID()) == short {
				fullID, known = j.ID(), true
				break
			}
		}
	}

	callerJobID, _ := ctx.Value(JobIDCtxKey{}).(string)

	// Inside a child agent only its own subtree is fair game. The refusal
	// names the boundary so the model can self-correct instead of retrying
	// blindly.
	if !killAllowedInsideChild(ctx, callerJobID, fullID, jobLister) {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error: fmt.Sprintf("refused: job %q is not within your own subtree — inside a subagent you may kill only jobs you started (your job id is %q); ask the main agent to stop it",
				jobID, callerJobID),
		}
	}

	if killBackgroundBash(fullID) {
		return ToolResult{
			Type:    "result",
			Success: true,
			Content: fmt.Sprintf("sent SIGKILL to the process group of job %s; its recorded result will show it as killed", fullID),
		}
	}

	if known && jobCanceler != nil && jobCanceler.Cancel(fullID) {
		return ToolResult{
			Type:    "result",
			Success: true,
			Content: fmt.Sprintf("stopped job %s; its recorded result will show it as stopped by user (kill_job), along with everything it had started", fullID),
		}
	}

	return killJobNotRunningError(jobID)
}

// killJobNotRunningError builds the failure for an unresolvable or already
// finished target, listing whatever background commands ARE still running
// so a mistyped id can be self-corrected. Keeps the pre-item-26 shape of
// the message ("check it with wait").
func killJobNotRunningError(jobID string) ToolResult {
	msg := "is not a running job (it may have already finished — check it with wait)"
	if running := runningBackgroundBash(); len(running) > 0 {
		msg = fmt.Sprintf("is not a running job (it may have already finished — check it with wait); currently running background commands: %v", running)
	}
	return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("job %q %s", jobID, msg)}
}

// shortID mirrors jobs.ShortID without importing the jobs package (this
// package stays storage-agnostic; see the package comment). It trims a
// "job-<unixnano>-<n>" id down to its trailing counter — the stable,
// human-scannable form the TUI jobs panel displays.
func shortID(id string) string {
	idx := strings.LastIndexByte(id, '-')
	if idx >= 0 && idx+1 < len(id) {
		return id[idx+1:]
	}
	return id
}
