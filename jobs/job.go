package jobs

import "time"

type Status string

const (
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusTruncated Status = "truncated"
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
	}
}
