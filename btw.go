package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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
		ID:                       job.ID,
		Done:                     job.Status != jobs.StatusRunning && job.Status != jobs.StatusWaitingAnswer,
		Success:                  job.Status == jobs.StatusDone || job.Status == jobs.StatusTruncated,
		Content:                  job.Result,
		Error:                    job.Err,
		Waiting:                  job.Status == jobs.StatusWaitingAnswer,
		Question:                 job.Question,
		Progress:                 job.Progress,
		ProgressHistory:          job.ProgressHistory,
		ProgressHistoryTruncated: job.ProgressHistoryTruncated,
	}, true
}

// jobObserverAdapter satisfies tools.JobObserver over JobRegistry — same
// translation as jobWaiterAdapter above, but calling WaitObserve instead of
// Wait, and via a distinctly-named Observe method (batch-2 review round 2
// finding D1) so this adapter can never be handed to SetJobObserver by
// mistake in place of jobWaiterAdapter — the two used to share the exact
// same method signature, which is what let the original C1 mistake compile
// silently. This is the one that matters for C1: runWithHandoff's watcher
// (and its handoff-message peek) must not count as a "waiter" for
// jobs.Job.QuestionHasWaiter's purposes, or Ask suppresses the onEvent
// notice for a caller that was never going to report the question to
// anyone. See jobs.Registry.WaitObserve's doc comment.
type jobObserverAdapter struct{ reg *jobs.Registry }

func (a jobObserverAdapter) Observe(ctx context.Context, id string, timeout time.Duration) (tools.JobStatus, bool) {
	job, ok := a.reg.WaitObserve(ctx, id, timeout)
	if !ok {
		return tools.JobStatus{}, false
	}
	return tools.JobStatus{
		ID:                       job.ID,
		Done:                     job.Status != jobs.StatusRunning && job.Status != jobs.StatusWaitingAnswer,
		Success:                  job.Status == jobs.StatusDone || job.Status == jobs.StatusTruncated,
		Content:                  job.Result,
		Error:                    job.Err,
		Waiting:                  job.Status == jobs.StatusWaitingAnswer,
		Question:                 job.Question,
		Progress:                 job.Progress,
		ProgressHistory:          job.ProgressHistory,
		ProgressHistoryTruncated: job.ProgressHistoryTruncated,
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

// jobExtensionRequesterAdapter satisfies tools.JobExtensionRequester over
// JobRegistry. The tools package owns only the interface so it can expose the
// child-only request/answer tools without importing the jobs package.
type jobExtensionRequesterAdapter struct{ reg *jobs.Registry }

func (a jobExtensionRequesterAdapter) RequestExtension(id string, seconds time.Duration, reason string) (string, bool) {
	return a.reg.RequestExtension(id, seconds, reason)
}

func (a jobExtensionRequesterAdapter) WaitExtension(ctx context.Context, id, requestID string) (bool, bool) {
	return a.reg.WaitExtension(ctx, id, requestID)
}

func (a jobExtensionRequesterAdapter) ResolveExtension(id, requestID string, approve bool) bool {
	return a.reg.ResolveExtension(id, requestID, approve)
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
func (a jobMailboxAdapter) IsLive(id string) bool            { return a.reg.IsLive(id) }
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

	// Detach from the tool call's context so the resumed job keeps running after
	// Resume returns. The registry still owns cancellation for kill_job; there
	// is deliberately no subagent-specific deadline.
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	parentID, _ := ctx.Value(tools.JobIDCtxKey{}).(string)
	job := a.reg.Start(jobCtx, task, jobs.KindSubagent, parentID, func(runCtx context.Context, newJobID string) (string, bool, error) {
		defer cancel()
		// newJobID is also this resumed conversation's todo-agent id (see
		// todoAgentIDFromCtx's JobIDCtxKey fallback in tools/todo.go) —
		// mark it done so its list becomes eligible for eviction once the
		// job finishes, the same as any other /btw or subagent list.
		defer tools.MarkTodoAgentDone(newJobID)
		runCtx = context.WithValue(runCtx, tools.JobIDCtxKey{}, newJobID)

		// entry.cfg was stashed bound to whatever job id was actually
		// running when it was recorded (agentRunner.run's own stash, or a
		// PREVIOUS Resume call's re-stash below) — in particular its
		// NextMessages closure drains that OLD job's mailbox, via
		// tools.JobMailboxNextMessages(oldJobID). Reusing entry.cfg verbatim
		// here would make this new job forever drain a dead mailbox: the
		// "message" tool would report "queued" against newJobID while the
		// agent loop actually reading its own NextMessages listens on
		// oldJobID's slot, which nothing ever posts to again. Rebinding a
		// fresh local copy (never entry.cfg itself, which stays untouched in
		// case entry is resumed again concurrently — see resumableEntry's
		// doc comment) fixes both a direct resume and a chained one, because
		// the re-stash below always stores THIS rebound copy, not entry.cfg.
		runCfg := entry.cfg
		runCfg.NextMessages = tools.JobMailboxNextMessages(newJobID)

		c := &collector{}
		// See ForkChildJob's identical comment (fork.go): without this the
		// resumed conversation's real spend never reaches internal/ledger,
		// and the Subagents tree would render it as free.
		_, err := agent.Run(runCtx, entry.mc, ledger.Watch(c, ledger.Subagent, entry.mc.Provider(), entry.mc.Model(), newJobID), &forked, runCfg)
		truncated := errors.Is(err, agent.ErrMaxIterations)
		deadlineExceeded := errors.Is(err, context.DeadlineExceeded)
		stopped := errors.Is(err, context.Canceled)
		if truncated {
			err = nil
		}
		text := strings.TrimSpace(c.text.String())

		// Re-register so a resumed job can itself be resumed again
		// (chaining), same condition as the original run in
		// agentRunner.run: a usable transcript, not a hard failure. Store
		// runCfg (bound to newJobID), not entry.cfg (bound to the id this
		// call started from) — otherwise a second resume off of newJobID
		// would inherit the same stale-mailbox bug one level down.
		if err == nil || truncated || deadlineExceeded || stopped {
			stashResumable(newJobID, resumableEntry{msgs: forked, mc: entry.mc, cfg: runCfg})
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
// btwEvaluation is the completed, read-only side conversation waiting for a
// positive decision from the parent. The transcript is kept here rather than
// in the jobs registry so promotion can consume it exactly once and create a
// separate real subagent job without re-running the evaluation.
type btwEvaluation struct {
	msgs      []connector.Message
	mc        connector.ModelClient
	cfg       agent.Config
	question  string
	createdAt time.Time
}

const (
	btwEvaluationTTL  = time.Hour
	maxBtwEvaluations = 32
)

var (
	btwEvaluationsMu sync.Mutex
	btwEvaluations   = make(map[string]*btwEvaluation)
	btwActive        int
)

func evictBtwEvaluationsLocked(now time.Time) {
	for id, eval := range btwEvaluations {
		if !eval.createdAt.IsZero() && now.Sub(eval.createdAt) >= btwEvaluationTTL {
			delete(btwEvaluations, id)
		}
	}
	for len(btwEvaluations) > maxBtwEvaluations {
		var oldestID string
		var oldest time.Time
		for id, eval := range btwEvaluations {
			if oldestID == "" || eval.createdAt.Before(oldest) {
				oldestID, oldest = id, eval.createdAt
			}
		}
		delete(btwEvaluations, oldestID)
	}
}

func retainBtwEvaluation(id string, eval *btwEvaluation) {
	btwEvaluationsMu.Lock()
	if eval.createdAt.IsZero() {
		eval.createdAt = time.Now()
	}
	btwEvaluations[id] = eval
	evictBtwEvaluationsLocked(time.Now())
	btwEvaluationsMu.Unlock()
}

const btwPromotionTask = "The read-only /btw evaluation concluded this is worth doing. Proceed with the work described in the original request now; use the full conversation above as context, make the requested changes, and report the result."

// btwPromotionAdapter is the composition-root implementation of
// tools.JobPromoter. It is wired over the same registry as /btw and async
// subagents, so promotion creates one ordinary, pollable subagent job.
type btwPromotionAdapter struct{}

func (btwPromotionAdapter) Promote(ctx context.Context, evaluationID string) (tools.JobHandle, error) {
	btwEvaluationsMu.Lock()
	evictBtwEvaluationsLocked(time.Now())
	eval, ok := btwEvaluations[evaluationID]
	if !ok {
		btwEvaluationsMu.Unlock()
		return nil, fmt.Errorf("/btw evaluation %q is not ready to promote", evaluationID)
	}
	// Consume the evaluation while holding the mutex. The map is deliberately
	// bounded by one-shot consumption: the full transcript/client/config are
	// not retained after promotion, and a second promotion cannot create a
	// duplicate child job.
	delete(btwEvaluations, evaluationID)
	msgs := session.ForkMessages(eval.msgs)
	client := eval.mc
	cfg := btwConfig(eval.cfg)
	cfg.Tools = &subagentToolRunner{}
	cfg.Schema = tools.GetSubagentToolsSchemaJSON()
	cfg.PendingJobs = nil
	question := eval.question
	btwEvaluationsMu.Unlock()

	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	parentID, _ := ctx.Value(tools.JobIDCtxKey{}).(string)
	job := JobRegistry.Start(jobCtx, question, jobs.KindSubagent, parentID, func(runCtx context.Context, jobID string) (string, bool, error) {
		defer cancel()
		defer tools.MarkTodoAgentDone(jobID)
		c := &collector{}
		runCtx = context.WithValue(runCtx, tools.JobIDCtxKey{}, jobID)
		runCtx = connector.WithModelClient(runCtx, client)
		// Match an ordinary child: background bash and kill_job must see the
		// child sink, while this same collector receives streamed output.
		runCtx = context.WithValue(runCtx, tools.SubagentSinkCtxKey{}, c)
		cfg.NextMessages = tools.JobMailboxNextMessages(jobID)
		runCtx = tools.WithToolGate(runCtx, tools.DenySubagentRecursion())
		msgs = session.ForkMessagesWithTurn(msgs, btwPromotionTask)
		_, err := agent.Run(runCtx, client, ledger.Watch(c, ledger.Subagent, client.Provider(), client.Model(), jobID), &msgs, cfg)
		truncated := errors.Is(err, agent.ErrMaxIterations)
		deadlineExceeded := errors.Is(err, context.DeadlineExceeded)
		stopped := errors.Is(err, context.Canceled)
		if truncated {
			err = nil
		}
		text := strings.TrimSpace(c.text.String())
		// Timeout and kill_job still leave useful partial work. Preserve it so
		// the parent can resume the promoted conversation instead of losing it.
		if err == nil || truncated || deadlineExceeded || stopped {
			stashResumable(jobID, resumableEntry{msgs: msgs, mc: client, cfg: cfg})
		}
		return text, truncated, err
	})
	return jobHandleAdapter{job}, nil
}

func btwConfig(base agent.Config) agent.Config {
	cfg := base
	// A /btw/fork/resume child is unlimited just like an ordinary subagent;
	// preserve the parent's other model settings and cancellation behavior.
	cfg.MaxIterations = 0
	cfg.Session = nil
	cfg.NextMessages = nil
	cfg.PendingTodos = nil
	cfg.HasTodos = nil
	// F10: base (cond.Config()) carries the MAIN conversation's Compactor.
	// GetAllToolsSchemaJSON below puts "compact" back in a /btw/fork/resume
	// child's schema (it has no other owner check — see CompactTool.Run),
	// so leaving this set would let such a child silently compact the
	// user's live main conversation instead of its own throwaway one. Nil
	// it out exactly like the other parent-owned state above; compactorFrom
	// then returns nil and the tool reports its existing, honest "compact
	// is unavailable" error instead of acting on the wrong conversation.
	cfg.Compactor = nil
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
	btwEvaluationsMu.Lock()
	if btwActive >= maxBtwEvaluations {
		btwEvaluationsMu.Unlock()
		return nil
	}
	btwActive++
	btwEvaluationsMu.Unlock()
	// The evaluation is a /btw subagent job too. Keep the caller's cancellation
	// signal, but do not create a special subagent deadline; kill_job and the
	// registry still cancel the job when requested.
	ctx, cancelEvaluation := context.WithCancel(ctx)
	forked := forkMessagesForBtw(cond.Messages(), question)
	cfg := btwConfig(cond.Config())
	cfg.Schema = tools.BtwEvaluationSchemaJSON()
	client, fallbacks := withIsolatedPool(cond.Client(), cfg.Fallbacks)
	cfg.Fallbacks = fallbacks

	parentID, _ := ctx.Value(tools.JobIDCtxKey{}).(string)
	// Captured as locals, not read from the package globals inside the
	// closure below — same reasoning as wireTools's bus/notices capture
	// (main.go): this closure can still be running long after JobRegistry/
	// JobNotices get reassigned elsewhere (test isolation swaps them), and
	// it must always report to the registry/queue it actually started on.
	reg, notices := JobRegistry, JobNotices
	return reg.Start(ctx, question, jobs.KindSubagent, parentID, func(jobCtx context.Context, jobID string) (string, bool, error) {
		defer cancelEvaluation()
		defer func() { btwEvaluationsMu.Lock(); btwActive--; btwEvaluationsMu.Unlock() }()
		jobCtx = context.WithValue(jobCtx, tools.JobIDCtxKey{}, jobID)
		jobCtx = tools.WithToolGate(jobCtx, tools.BtwReadOnlyGate())
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
		if err == nil || truncated {
			retainBtwEvaluation(jobID, &btwEvaluation{msgs: session.ForkMessages(forked), mc: client, cfg: cfg, question: question})
			noticeText := fmt.Sprintf("[btw] evaluation %q finished (job_id=%q): %s\nIf it is worth doing, call promote_btw(job_id=%q). Promotion creates one real subthread; wait for that job instead of doing the work in this thread.", question, jobID, strings.TrimSpace(text), jobID)
			// B4: address this to whoever spawned this /btw job (parentID),
			// not unconditionally to the main queue — see the identical
			// routing/fallback choice in wireTools's onEvent hook (main.go).
			if parentID == "" || !reg.Post(parentID, noticeText) {
				if parentID != "" {
					noticeText = fmt.Sprintf("[for job %s, which has already finished — forwarded here instead] %s", parentID, noticeText)
				}
				notices.Notify(noticeText)
			}
		}
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
