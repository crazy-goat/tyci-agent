package tools

import (
	"sync"
	"time"
)

// JobProgressHeartbeat is the local contract behind the periodic
// report_progress nudge (TODO item 15): a running subagent job that has
// gone quiet — no report_progress note, and no earlier nudge — for longer
// than the given duration gets one harness-authored reminder injected into
// its own loop at its next iteration boundary. Same layering rule as the
// other job-facing contracts in this package (JobAsker, JobMailbox, ...):
// this package never imports "jobs" directly, so jobs.Registry satisfies
// this structurally. Wired once from main() via SetJobProgressHeartbeat
// over the app's shared jobs.Registry.
type JobProgressHeartbeat interface {
	// NeedsProgressHeartbeat reports whether the running job identified by
	// id should be nudged right now, and if so records that the nudge
	// happened so the very next call does not immediately fire again — see
	// jobs.Registry.NeedsProgressHeartbeat's doc comment for why this is a
	// single check-and-set rather than two separate calls.
	NeedsProgressHeartbeat(id string, after time.Duration) bool
}

// jobProgressHeartbeat is nil until SetJobProgressHeartbeat is called; until
// then JobProgressHeartbeatCheck returns nil, so wiring it into a child's
// agent.Config is a harmless no-op. Guarded by jobProgressHeartbeatMu for
// the same reason jobNotifier is (see bgbash.go's jobNotifierMu doc
// comment): the closure it backs runs from a job's own long-lived agent
// loop goroutine, which outlives the tool call that started it, while
// SetJobProgressHeartbeat is called from the setup path.
var (
	jobProgressHeartbeatMu sync.RWMutex
	jobProgressHeartbeat   JobProgressHeartbeat
)

// SetJobProgressHeartbeat wires the periodic report_progress nudge to a
// JobProgressHeartbeat. Called once from main() with an adapter over the
// app's shared jobs.Registry.
func SetJobProgressHeartbeat(h JobProgressHeartbeat) {
	jobProgressHeartbeatMu.Lock()
	jobProgressHeartbeat = h
	jobProgressHeartbeatMu.Unlock()
}

// getJobProgressHeartbeat copies the current JobProgressHeartbeat out under
// RLock — see getJobAsker's doc comment (ask.go) for why callers never hold
// the lock while calling into the interface.
func getJobProgressHeartbeat() JobProgressHeartbeat {
	jobProgressHeartbeatMu.RLock()
	defer jobProgressHeartbeatMu.RUnlock()
	return jobProgressHeartbeat
}

// JobProgressHeartbeatCheck returns a ProgressHeartbeat-shaped callback
// (agent.Config.ProgressHeartbeat) bound to jobID: calling it asks whether
// jobID has gone quiet for more than SubagentBackgroundAfterSec — reused
// here as this reminder's threshold too, per item 15's decided design
// ("time-based, not step-based"): iterations vary from one second to ten
// minutes and MaxIterations defaults to unlimited, so a flat iteration count
// would be meaningless, for exactly the reason SubagentBackgroundAfterSec
// itself already exists as a time-based handoff rather than a step count.
//
// Returns nil when jobID is empty or no JobProgressHeartbeat is wired, so a
// caller can assign the result to cfg.ProgressHeartbeat unconditionally —
// agent.Run already treats a nil ProgressHeartbeat as "never nudge".
func JobProgressHeartbeatCheck(jobID string) func() bool {
	if getJobProgressHeartbeat() == nil || jobID == "" {
		return nil
	}
	// Re-reads the wiring via getJobProgressHeartbeat on every call rather
	// than capturing the pointer above — this runs from a job's own
	// long-lived agent loop, so it must never hold a stale copy across
	// however many checks happen over that job's lifetime (same reasoning
	// as JobMailboxNextMessages in message.go).
	return func() bool {
		heartbeat := getJobProgressHeartbeat()
		if heartbeat == nil {
			return false
		}
		return heartbeat.NeedsProgressHeartbeat(jobID, SubagentBackgroundAfterSec())
	}
}
