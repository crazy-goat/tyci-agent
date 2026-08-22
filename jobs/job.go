package jobs

import (
	"strings"
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

	// answerCh is lazily created by Ask and delivered to by Answer. It stays
	// internal to the registry: unexported and channel-typed, so Snapshot
	// must never copy it.
	answerCh chan jobAnswer
}

// jobAnswer is what Answer sends and Ask receives over answerCh. fromUser
// distinguishes a human reply (delivered via the TUI/REPL "/answer" command)
// from an agent's own reply (delivered via the "answer" tool, which only the
// model can invoke) — see AskTool's doc comment in tools/ask.go for why the
// distinction has to survive the round trip.
type jobAnswer struct {
	text     string
	fromUser bool
}

// ShortID trims the "job-<unixnano>-<n>" ID (see nextID) down to its
// trailing counter — the stable, human-scannable form the TUI jobs panel
// displays and that a person types back into "/answer".
func ShortID(id string) string {
	if idx := strings.LastIndexByte(id, '-'); idx >= 0 && idx+1 < len(id) {
		return id[idx+1:]
	}
	return id
}

func (j *Job) Snapshot() Job {
	return Job{
		ID:          j.ID,
		Description: j.Description,
		Status:      j.Status,
		Result:      j.Result,
		Err:         j.Err,
		StartedAt:   j.StartedAt,
		FinishedAt:  j.FinishedAt,
		Question:    j.Question,
		Progress:    j.Progress,
	}
}
