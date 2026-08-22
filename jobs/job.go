package jobs

import (
	"strings"
	"sync/atomic"
	"time"
)

type Status string

const (
	StatusRunning       Status = "running"
	StatusDone          Status = "done"
	StatusFailed        Status = "failed"
	StatusTruncated     Status = "truncated"
	StatusWaitingAnswer Status = "waiting_answer"
)

type Job struct {
	ID          string
	Description string

	Status     Status
	Result     string
	Err        string
	StartedAt  time.Time
	FinishedAt time.Time
	done       chan struct{}

	// Question holds the pending question text while Status ==
	// StatusWaitingAnswer (set by Ask, cleared once answered or unblocked).
	Question string

	// Progress holds the last status note reported via SetProgress
	// (report_progress tool). Unlike Question, it is NOT cleared when the
	// job finishes — it persists as the last thing the job said about its
	// own progress before completing.
	Progress string

	// LastActivity is materialized by Snapshot from lastActivity below — it
	// only ever holds a meaningful value on a Snapshot()-returned copy, not
	// on the live *Job (which tracks the same information in lastActivity
	// instead). Display code reads this field, never lastActivity directly.
	LastActivity time.Time

	// lastActivity holds unix nanoseconds of the last sign of life seen from
	// this job: a streamed Text/Thinking/ToolCallStart/ToolCallDelta/
	// ToolCallEnd call, or a backgrounded bash output line — see
	// jobs.Registry.TouchActivity and its callers in tools/subagent.go and
	// tools/bash.go. Deliberately NOT derived from Progress: report_progress
	// is voluntary and rare, so a job that never calls it would otherwise
	// read as permanently idle even while actively streaming output.
	//
	// This is a plain int64 rather than atomic.Int64 on purpose: Snapshot
	// copies the whole Job by value, and go vet's copylocks check rejects
	// copying a struct containing an atomic.Int64 (it carries a noCopy-style
	// guard). A plain int64 read/written via atomic.LoadInt64/StoreInt64 on
	// its address has no such restriction, and needs no registry-wide lock
	// to update — see touchActivity.
	//
	// Set once at Start (so a job that hasn't done anything yet reads as
	// "idle since start", not a zero time producing a nonsense duration) and
	// never cleared on completion — like Progress, it persists past terminal
	// state so a completed job's last known activity is still visible.
	lastActivity int64

	// answerCh is lazily created by Ask and delivered to by Answer. It stays
	// internal to the registry: unexported and channel-typed, so Snapshot
	// must never copy it.
	answerCh chan jobAnswer
}

// touchActivity atomically records "now" as this job's last sign of life.
// Safe for concurrent use from multiple goroutines (parallel tool calls
// within the same job), and cheap enough to call on every streamed token:
// no registry lock, no snapshot, no event. The CAS loop guards against a
// stale call (racing with a newer one) ever moving the timestamp backward.
func (j *Job) touchActivity() {
	now := time.Now().UnixNano()
	for {
		old := atomic.LoadInt64(&j.lastActivity)
		if now <= old {
			return
		}
		if atomic.CompareAndSwapInt64(&j.lastActivity, old, now) {
			return
		}
	}
}

// jobAnswer is what Answer sends and Ask receives over answerCh. fromUser
// distinguishes a genuine human reply from an agent's own reply (delivered
// via the "answer_job" tool, which only the model can invoke, and which
// always passes fromUser=false — there is no dedicated command a human
// types directly any more) — see AskTool's doc comment in tools/ask.go for
// why the distinction has to survive the round trip.
type jobAnswer struct {
	text     string
	fromUser bool
}

// ShortID trims the "job-<unixnano>-<n>" ID (see nextID) down to its
// trailing counter — the stable, human-scannable form the TUI jobs panel
// displays.
func ShortID(id string) string {
	if idx := strings.LastIndexByte(id, '-'); idx >= 0 && idx+1 < len(id) {
		return id[idx+1:]
	}
	return id
}

func (j *Job) Snapshot() Job {
	return Job{
		ID:           j.ID,
		Description:  j.Description,
		Status:       j.Status,
		Result:       j.Result,
		Err:          j.Err,
		StartedAt:    j.StartedAt,
		FinishedAt:   j.FinishedAt,
		Question:     j.Question,
		Progress:     j.Progress,
		LastActivity: time.Unix(0, atomic.LoadInt64(&j.lastActivity)),
	}
}
