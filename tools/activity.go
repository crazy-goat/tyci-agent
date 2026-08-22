package tools

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
// the jobs panel, not something a caller needs to check.
var jobActivityToucher JobActivityToucher

// SetJobActivityToucher wires the streaming/bash "last activity" touch
// points to a JobActivityToucher.
func SetJobActivityToucher(t JobActivityToucher) { jobActivityToucher = t }

// touchJobActivity is the single shared touch-point both streamingCollector
// (subagent.go) and backgrounded bash (bash.go) call through, so "what
// counts as a sign of life" is decided in exactly one place.
func touchJobActivity(jobID string) {
	if jobActivityToucher == nil || jobID == "" {
		return
	}
	jobActivityToucher.TouchActivity(jobID)
}
