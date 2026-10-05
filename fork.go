package main

import (
	"fmt"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/session"
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
// session.SanitizeMessageSequence — see its doc comment) that the fork path
// consumes identically:
//
//   - ForkNewSession: fork-as-new-session. The forked history (no extra
//     turn appended — the point is to keep talking as the user, not to hand
//     off a task) is written into a brand-new, independently persisted
//     session file, so the caller can resume it exactly like any other
//     `tyci session list`/`/resume` entry.

// ForkNewSession creates a brand-new, independently persisted session file
// under cwd, seeded with base — already the fork point's history (see
// session.ForkAtIndex / session.ForkAtEventID) — and returns the opened
// Session (still open; caller decides when to close it, exactly like any
// other freshly-Open'd session) plus the exact []connector.Message it wrote,
// ready to hand to agent.Config.
//
// No extra user turn is appended: fork-as-new-session
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
			_ = sess.Close()
			return nil, nil, fmt.Errorf("fork-as-new-session: writing forked history: %w", err)
		}
	}

	return sess, forked, nil
}
