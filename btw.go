package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/tools"
)

// JobRegistry is the app's one shared background-job registry. /btw side
// conversations (below) and the "subagent" tool's async spawn path both run
// on it, and the "wait" tool (tools/wait.go) can poll any job started here
// via SetJobWaiter's/SetJobStarter's adapters in main(). It lives here, not
// in tools/tool.go, because tools deliberately does not import "jobs" (see
// tools.JobWaiter's doc comment) — main is the one layer allowed to depend
// on both.
var JobRegistry = jobs.NewRegistry()

// jobWaiterAdapter satisfies tools.JobWaiter over JobRegistry, translating
// jobs.Job's richer status into the tools package's minimal JobStatus shape.
type jobWaiterAdapter struct{ reg *jobs.Registry }

func (a jobWaiterAdapter) Wait(ctx context.Context, id string, timeout time.Duration) (tools.JobStatus, bool) {
	job, ok := a.reg.Wait(ctx, id, timeout)
	if !ok {
		return tools.JobStatus{}, false
	}
	return tools.JobStatus{
		ID:       job.ID,
		Done:     job.Status != jobs.StatusRunning && job.Status != jobs.StatusWaitingAnswer,
		Success:  job.Status == jobs.StatusDone || job.Status == jobs.StatusTruncated,
		Content:  job.Result,
		Error:    job.Err,
		Waiting:  job.Status == jobs.StatusWaitingAnswer,
		Question: job.Question,
		Progress: job.Progress,
	}, true
}

// jobHandleAdapter satisfies tools.JobHandle by exposing *jobs.Job's ID
// field as a method — the tools package's contract is method-shaped so it
// never needs to know jobs.Job's concrete field layout.
type jobHandleAdapter struct{ *jobs.Job }

func (j jobHandleAdapter) ID() string { return j.Job.ID }

// jobStarterAdapter satisfies tools.JobStarter over JobRegistry, the same
// registry jobWaiterAdapter and /btw run on — so an async subagent job is
// pollable via the wait tool and shows up wherever JobRegistry is inspected.
type jobStarterAdapter struct{ reg *jobs.Registry }

func (a jobStarterAdapter) Start(ctx context.Context, description, kind, parentID string, fn func(context.Context, string) (string, bool, error)) tools.JobHandle {
	return jobHandleAdapter{a.reg.Start(ctx, description, jobs.Kind(kind), parentID, fn)}
}

// jobAskerAdapter satisfies tools.JobAsker over JobRegistry.
type jobAskerAdapter struct{ reg *jobs.Registry }

func (a jobAskerAdapter) Ask(ctx context.Context, id, question string) (string, bool, bool) {
	return a.reg.Ask(ctx, id, question)
}

// jobAnswererAdapter satisfies tools.JobAnswerer over JobRegistry.
type jobAnswererAdapter struct{ reg *jobs.Registry }

func (a jobAnswererAdapter) Answer(id, text string, fromUser bool) bool {
	return a.reg.Answer(id, text, fromUser)
}

// jobProgressAdapter satisfies tools.JobProgressReporter over JobRegistry.
type jobProgressAdapter struct{ reg *jobs.Registry }

func (a jobProgressAdapter) SetProgress(id, text string) bool {
	return a.reg.SetProgress(id, text)
}

// jobActivityToucherAdapter satisfies tools.JobActivityToucher over
// JobRegistry.
type jobActivityToucherAdapter struct{ reg *jobs.Registry }

func (a jobActivityToucherAdapter) TouchActivity(id string) {
	a.reg.TouchActivity(id)
}

// jobMailboxAdapter satisfies tools.JobMailbox over JobRegistry: backs the
// "message" tool and the "/msg" slash command (Resolve/Post), and the
// per-job NextMessages drain wired into a background subagent's own
// agent.Config (Drain, via tools.JobMailboxNextMessages).
type jobMailboxAdapter struct{ reg *jobs.Registry }

func (a jobMailboxAdapter) Resolve(id string) (string, bool) { return a.reg.Resolve(id) }
func (a jobMailboxAdapter) Post(id, text string) bool        { return a.reg.Post(id, text) }
func (a jobMailboxAdapter) Drain(id string) []string         { return a.reg.DrainMessages(id) }

// jobResumerAdapter satisfies tools.JobResumer over JobRegistry and the
// package-level resumable map (main.go): it forks a previously-recorded
// conversation, appends task as a new user turn, and runs the fork as a
// brand-new job — same shape as runAsync's spawn path (subagent.go), just
// seeded from a stored transcript instead of a fresh one.
type jobResumerAdapter struct{ reg *jobs.Registry }

func (a jobResumerAdapter) Resume(ctx context.Context, jobID, task string) (tools.JobHandle, error) {
	resumableMu.Lock()
	entry, ok := resumable[jobID]
	resumableMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("job %q has no resumable conversation (only a finished async subagent or /btw job can be resumed)", jobID)
	}

	// forkMessagesForBtw's copy-then-append-user-turn shape is exactly what
	// a resumed conversation needs too: a new backing array so nothing the
	// new job appends can ever alias or mutate the stored transcript.
	forked := forkMessagesForBtw(entry.msgs, task)

	// Same detach-and-backstop pattern as runAsync (tools/subagent.go): the
	// tool call's own ctx dies with this turn, but the resumed job must keep
	// running after Resume returns.
	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tools.SubagentTimeoutSec*time.Second)

	parentID, _ := ctx.Value(tools.JobIDCtxKey{}).(string)
	job := a.reg.Start(jobCtx, task, jobs.KindSubagent, parentID, func(runCtx context.Context, newJobID string) (string, bool, error) {
		defer cancel()
		// newJobID is also this resumed conversation's todo-agent id (see
		// todoAgentIDFromCtx's JobIDCtxKey fallback in tools/todo.go) —
		// mark it done so its list becomes eligible for eviction once the
		// job finishes, the same as any other /btw or subagent list.
		defer tools.MarkTodoAgentDone(newJobID)
		runCtx = context.WithValue(runCtx, tools.JobIDCtxKey{}, newJobID)

		c := &collector{}
		// See ForkChildJob's identical comment (fork.go): without this the
		// resumed conversation's real spend never reaches internal/ledger,
		// and the Subagents tree would render it as free.
		_, err := agent.Run(runCtx, entry.mc, ledger.Watch(c, ledger.Subagent, entry.mc.Provider(), entry.mc.Model(), newJobID), &forked, entry.cfg)
		truncated := errors.Is(err, agent.ErrMaxIterations)
		if truncated {
			err = nil
		}
		text := strings.TrimSpace(c.text.String())

		// Re-register so a resumed job can itself be resumed again
		// (chaining), same condition as the original run in
		// agentRunner.run: a usable transcript, not a hard failure.
		if err == nil || truncated {
			resumableMu.Lock()
			resumable[newJobID] = resumableEntry{msgs: forked, mc: entry.mc, cfg: entry.cfg}
			resumableMu.Unlock()
		}

		return text, truncated, err
	})

	return jobHandleAdapter{job}, nil
}

// btwIDCounter backs nextBtwID, mirroring jobs' own timestamp+counter ID
// scheme. Kept as a separate ID space from jobs.Registry's IDs on purpose:
// a /btw entry's ID is a display-layer identity used to route streamed
// output to the right modal, assigned before the background job (and its
// own, unrelated job ID) exists.
var btwIDCounter uint64

func nextBtwID() string {
	n := atomic.AddUint64(&btwIDCounter, 1)
	return fmt.Sprintf("btw-%d-%d", time.Now().UnixNano(), n)
}

// forkMessagesForBtw returns a conversation that starts as an independent
// copy of msgs — a new backing array, so nothing the fork appends
// afterwards (its own tool calls, its own turns) can ever alias or mutate
// the main thread's history — with question appended as a new user turn.
//
// A thin wrapper over session.ForkMessagesWithTurn, which generalizes this
// exact shape for session forking (fork.go) and the "subagent" tool's
// inherit_history option (tools/subagent.go) to share instead of each
// reimplementing it.
func forkMessagesForBtw(msgs []connector.Message, question string) []connector.Message {
	return session.ForkMessagesWithTurn(msgs, question)
}

// btwConfig derives the agent.Config for a /btw fork from the main
// conversation's config. It keeps the model behavior (tools, schema,
// fallbacks, retries) but detaches everything that ties the config to the
// main thread's own state: Session (a side conversation writes no session
// log), NextMessages (the main TUI's pending-input queue), and
// PendingTodos/HasTodos (the main thread's todo list). None of those belong
// to an independent fork that must never touch the main conversation.
func btwConfig(base agent.Config) agent.Config {
	cfg := base
	cfg.Session = nil
	cfg.NextMessages = nil
	cfg.PendingTodos = nil
	cfg.HasTodos = nil
	// base.Schema is the top-level, non-job schema (tools.
	// GetTopLevelToolsSchemaJSON, set in commands.go), which excludes
	// ask_parent because the top-level conversation has no job id. A /btw
	// side-conversation IS a job (see startBtw, which stamps jobCtx with
	// tools.JobIDCtxKey before running it) and must get ask_parent back.
	cfg.Schema = tools.GetAllToolsSchemaJSON()
	return cfg
}

// startBtw forks cond's current conversation, appends question as a new
// user turn, and runs it as a background job on JobRegistry — so the main
// conversation is never blocked and the result never flows back into it.
// sink streams the child's output live to the /btw modal; MarkDone is
// called once agent.Run returns so the modal (and the /btw list) show it as
// finished.
//
// client/fallbacks are wrapped through withIsolatedPool (the same wrapper
// agentRunner.run uses for subagents) instead of running on cond.Client()
// directly. Without this, /btw's background agent.Run shared the main
// conversation's HTTP client/connection pool: /btw's request and the main
// thread's next request would contend for and block on the same pool,
// which is exactly the "btw hangs, then the next normal prompt hangs too"
// symptom this fixes — a /btw side-conversation is a genuinely independent
// agent run and must share nothing transport-level with the parent, same
// as a subagent.
func startBtw(ctx context.Context, cond *conductor.Conductor, question string, sink *display.BtwSink) *jobs.Job {
	forked := forkMessagesForBtw(cond.Messages(), question)
	cfg := btwConfig(cond.Config())
	client, fallbacks := withIsolatedPool(cond.Client(), cfg.Fallbacks)
	cfg.Fallbacks = fallbacks

	parentID, _ := ctx.Value(tools.JobIDCtxKey{}).(string)
	return JobRegistry.Start(ctx, question, jobs.KindSubagent, parentID, func(jobCtx context.Context, jobID string) (string, bool, error) {
		jobCtx = context.WithValue(jobCtx, tools.JobIDCtxKey{}, jobID)
		// jobID is also this /btw side-conversation's todo-agent id (see
		// todoAgentIDFromCtx's JobIDCtxKey fallback in tools/todo.go) —
		// mark it done so its list becomes eligible for eviction once this
		// job finishes.
		defer tools.MarkTodoAgentDone(jobID)
		// A /btw side-conversation is a job like any other (see the comment
		// on cfg.Schema above), so it gets the same mailbox drain a
		// background subagent gets: /msg or the "message" tool can steer it
		// mid-flight, delivered at its next iteration boundary. cfg here is
		// the local copy this closure captured from startBtw's argument, not
		// shared with any other job, so mutating it right before the one
		// agent.Run call that uses it is safe.
		cfg.NextMessages = tools.JobMailboxNextMessages(jobID)
		// See ForkChildJob's identical comment (fork.go): a /btw
		// side-conversation spends the parent's money like any other child
		// and must record against the same ledger, or it renders as free in
		// the Subagents tree.
		_, err := agent.Run(jobCtx, client, ledger.Watch(sink, ledger.Subagent, client.Provider(), client.Model(), jobID), &forked, cfg)
		truncated := errors.Is(err, agent.ErrMaxIterations)
		if truncated {
			err = nil
		}
		text := sink.CollectedText()
		sink.MarkDone(err)
		return text, truncated, err
	})
}

// jobCancelerAdapter satisfies tools.JobCanceler over JobRegistry: kill_job's
// subagent path. Registry.Cancel refuses already-terminal jobs, so a stale
// id surfaces as "not running" instead of a fake success.
type jobCancelerAdapter struct{ reg *jobs.Registry }

func (a jobCancelerAdapter) Cancel(id string) bool { return a.reg.Cancel(id) }

// jobKindSource adapts one jobs.Job snapshot to tools.JobKindSource (value
// receiver, plain field reads — Job has no methods of its own).
type jobKindSource struct{ j jobs.Job }

func (s jobKindSource) ID() string       { return s.j.ID }
func (s jobKindSource) ParentID() string { return s.j.ParentID }

// listJobsAdapter satisfies tools.JobLister over JobRegistry: kill_job's
// inside-a-child subtree check walks this parentage.
type listJobsAdapter struct{ reg *jobs.Registry }

func (a listJobsAdapter) ListJobs() []tools.JobKindSource {
	list := a.reg.List()
	out := make([]tools.JobKindSource, len(list))
	for i, j := range list {
		out[i] = jobKindSource{j}
	}
	return out
}
