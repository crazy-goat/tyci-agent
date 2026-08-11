package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/jobs"
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
		ID:      job.ID,
		Done:    job.Status != jobs.StatusRunning,
		Success: job.Status == jobs.StatusDone || job.Status == jobs.StatusTruncated,
		Content: job.Result,
		Error:   job.Err,
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

func (a jobStarterAdapter) Start(ctx context.Context, description string, fn func(context.Context) (string, bool, error)) tools.JobHandle {
	return jobHandleAdapter{a.reg.Start(ctx, description, fn)}
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
func forkMessagesForBtw(msgs []connector.Message, question string) []connector.Message {
	forked := make([]connector.Message, len(msgs), len(msgs)+1)
	copy(forked, msgs)
	return append(forked, connector.Message{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: question}},
	})
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
	return cfg
}

// startBtw forks cond's current conversation, appends question as a new
// user turn, and runs it as a background job on JobRegistry — so the main
// conversation is never blocked and the result never flows back into it.
// sink streams the child's output live to the /btw modal; MarkDone is
// called once agent.Run returns so the modal (and the /btw list) show it as
// finished.
func startBtw(ctx context.Context, cond *conductor.Conductor, question string, sink *display.BtwSink) *jobs.Job {
	forked := forkMessagesForBtw(cond.Messages(), question)
	cfg := btwConfig(cond.Config())
	client := cond.Client()

	return JobRegistry.Start(ctx, question, func(jobCtx context.Context) (string, bool, error) {
		_, err := agent.Run(jobCtx, client, sink, &forked, cfg)
		truncated := errors.Is(err, agent.ErrMaxIterations)
		if truncated {
			err = nil
		}
		text := sink.CollectedText()
		sink.MarkDone(err)
		return text, truncated, err
	})
}
