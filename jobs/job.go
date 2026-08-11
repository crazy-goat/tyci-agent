package jobs

import "time"

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
	answerCh chan string
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
