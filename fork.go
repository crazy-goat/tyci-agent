package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/tools"
)

// Package-level notes on session forking (TODO.md item 5):
//
// The fork POINT is addressed one of two ways, matching whichever kind of
// conversation is being forked:
//
//   - A live, in-memory conversation (the one the user is talking to right
//     now) is addressed by transcript index — see session.ForkAtIndex.
//   - A persisted session file is addressed by session event id — the same
//     id scheme session.WriteCompaction already records as tail_start_id
//     and RebuildMessages/`tyci session show` already understand — see
//     session.ForkAtEventID.
//
// Either way, the result is a sanitized []connector.Message (a cut that
// lands inside a tool-call/result pair is repaired by
// session.SanitizeMessageSequence — see its doc comment) that this file's
// two fork PATHS consume identically:
//
//   - ForkChildJob: fork-as-background-child. The forked history plus a new
//     user turn (the task) becomes the seed for a background subagent job,
//     reusing the same JobRegistry/resumable machinery as /btw and
//     jobResumerAdapter.Resume (btw.go) — so the fork is pollable with
//     wait(job_id=...) and itself resumable/forkable again afterward.
//   - ForkNewSession: fork-as-new-session. The forked history (no extra
//     turn appended — the point is to keep talking as the user, not to hand
//     off a task) is written into a brand-new, independently persisted
//     session file, so the caller can resume it exactly like any other
//     `tyci session list`/`/resume` entry.

// ForkChildJob starts a background subagent job seeded with base — already
// the fork point's history (see session.ForkAtIndex / session.ForkAtEventID)
// — with task appended as the child's own new user turn. It registers the
// job as resumable, exactly like jobResumerAdapter.Resume (btw.go), so a
// forked child can itself be resumed or forked again once it finishes.
//
// client is the conductor's already-resolved model client; cfg its config.
// Both get the same treatment startBtw gives a /btw side-conversation:
// wrapped through withIsolatedPool so the fork shares no HTTP connection
// pool with the parent, and passed through btwConfig so it does not touch
// the parent's Session/NextMessages/PendingTodos.
func ForkChildJob(ctx context.Context, cond *conductor.Conductor, base []connector.Message, task string) *jobs.Job {
	forked := session.ForkMessagesWithTurn(base, task)
	cfg := btwConfig(cond.Config())
	client, fallbacks := withIsolatedPool(cond.Client(), cfg.Fallbacks)
	cfg.Fallbacks = fallbacks

	return JobRegistry.Start(ctx, task, func(jobCtx context.Context, jobID string) (string, bool, error) {
		jobCtx = context.WithValue(jobCtx, tools.JobIDCtxKey{}, jobID)
		defer tools.MarkTodoAgentDone(jobID)
		cfg.NextMessages = tools.JobMailboxNextMessages(jobID)

		c := &collector{}
		_, err := agent.Run(jobCtx, client, c, &forked, cfg)
		truncated := errors.Is(err, agent.ErrMaxIterations)
		if truncated {
			err = nil
		}
		text := strings.TrimSpace(c.text.String())

		// Same re-registration as jobResumerAdapter.Resume: a usable
		// transcript (clean finish or truncated-but-produced-text) makes
		// this job resumable/forkable again, chaining exactly like any
		// other async subagent or /btw job.
		if err == nil || truncated {
			resumableMu.Lock()
			resumable[jobID] = resumableEntry{msgs: forked, mc: client, cfg: cfg}
			resumableMu.Unlock()
		}

		return text, truncated, err
	})
}

// ForkNewSession creates a brand-new, independently persisted session file
// under cwd, seeded with base — already the fork point's history (see
// session.ForkAtIndex / session.ForkAtEventID) — and returns the opened
// Session (still open; caller decides when to close it, exactly like any
// other freshly-Open'd session) plus the exact []connector.Message it wrote,
// ready to hand to agent.Config.
//
// Unlike ForkChildJob, no extra user turn is appended: fork-as-new-session
// exists for continuing AS the user rather than handing off a task, so the
// fork is just the history, waiting for whatever the user types next —
// which is also why it is a session.Session, not a jobs.Job: the point is
// that nothing runs until they do.
func ForkNewSession(cwd, model, provider string, base []connector.Message) (*session.Session, []connector.Message, error) {
	path, err := session.DefaultPath(cwd)
	if err != nil {
		return nil, nil, fmt.Errorf("fork-as-new-session: %w", err)
	}
	sess, err := session.Open(path, cwd, model, provider)
	if err != nil {
		return nil, nil, fmt.Errorf("fork-as-new-session: %w", err)
	}

	// Independent copy: nothing written into the new session file, or
	// appended to it afterward, can ever alias base (the source
	// conversation's own backing array).
	forked := session.ForkMessages(base)
	for _, msg := range forked {
		blocks := session.ContentBlocksFromConnector(msg.Content)
		if err := sess.WriteMessage(msg.Role, blocks, nil); err != nil {
			sess.Close()
			return nil, nil, fmt.Errorf("fork-as-new-session: writing forked history: %w", err)
		}
	}

	return sess, forked, nil
}
