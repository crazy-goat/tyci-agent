package tools

import "sync"

// JobActivityToucher is the local contract that lets a running job record a
// fresh "sign of life" without this package importing "jobs" — same
// layering rule as JobProgressReporter/JobStarter/etc above. Wired once from
// main() over the app's shared jobs.Registry via SetJobActivityToucher.
//
// This is intentionally a separate, narrower interface from
// JobProgressReporter: SetProgress (report_progress) is a voluntary, rare
// status note and also snapshots+fires an event; TouchActivity is meant to
// be called on every streamed token from every running job (see
// streamingCollector in subagent.go and bashRun.setProgress in bash.go), so
// it must stay as cheap as a single atomic write with no event/snapshot
// overhead.
type JobActivityToucher interface {
	// TouchActivity records that job id showed a fresh sign of life at
	// time.Now(). Must be cheap enough to call unconditionally on every
	// streamed token. A call for an unknown id is a silent no-op.
	TouchActivity(id string)
}

// jobActivityToucher is nil until SetJobActivityToucher is called; touching
// activity before then (e.g. in a mode that never wires job support) is
// simply a no-op rather than an error, since it is best-effort telemetry for
// the jobs panel, not something a caller needs to check. Guarded by
// jobActivityToucherMu for the same reason jobNotifier is (see bgbash.go's
// jobNotifierMu doc comment): touchJobActivity is called from every running
// job's own goroutine, which outlives the tool call that started it, while
// SetJobActivityToucher is called from the setup path.
var (
	jobActivityToucherMu sync.RWMutex
	jobActivityToucher   JobActivityToucher
)

// SetJobActivityToucher wires the streaming/bash "last activity" touch
// points to a JobActivityToucher.
func SetJobActivityToucher(t JobActivityToucher) {
	jobActivityToucherMu.Lock()
	jobActivityToucher = t
	jobActivityToucherMu.Unlock()
}

// touchJobActivity is the single shared touch-point both streamingCollector
// (subagent.go) and backgrounded bash (bash.go) call through, so "what
// counts as a sign of life" is decided in exactly one place. Copies the
// current JobActivityToucher out under RLock and calls into the local copy
// unlocked, same as every other job hook in this package (see getJobAsker's
// doc comment in ask.go for why holding the lock across the call would be
// wrong).
func touchJobActivity(jobID string) {
	if jobID == "" {
		return
	}
	jobActivityToucherMu.RLock()
	toucher := jobActivityToucher
	jobActivityToucherMu.RUnlock()
	if toucher == nil {
		return
	}
	toucher.TouchActivity(jobID)
}
