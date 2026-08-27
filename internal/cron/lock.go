package cron

import "path/filepath"

// LockFileName is the cross-process advisory lock tick dispatch takes, so
// that a live session's minute ticker (StartCronTicker) and a `tyci cron
// tick` invoked by the OS's own scheduler (or two overlapping tick
// invocations) cannot both see the same job as due and both dispatch it.
// Job.LastRun in cron.json is only written back after a run finishes
// (MarkRun), so without this lock two processes racing a tick both read the
// same "not run yet" state and both start the job.
const LockFileName = "tick.lock"

// LockPath returns the tick lock file inside dir (the ~/.tyci directory).
func LockPath(configDir string) string {
	return filepath.Join(configDir, LockFileName)
}
