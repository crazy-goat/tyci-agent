package tools

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type lateResumeHandle string

func (h lateResumeHandle) ID() string { return string(h) }

// lateResumer starts one resumed job. The job ends 200 ms after it is cancelled,
// so a late write would still be running after Cancel.
type lateResumer struct {
	job *lateJob
}

func (r lateResumer) Resume(context.Context, string, string) (JobHandle, error) {
	return lateResumeHandle("job-1"), nil
}

type lateJob struct {
	cancelled chan struct{}
	finished  chan struct{}
	ended     atomic.Bool
}

func (j *lateJob) Cancel(string) bool {
	select {
	case <-j.cancelled:
	default:
		close(j.cancelled)
		go func() {
			time.Sleep(200 * time.Millisecond)
			j.ended.Store(true)
			close(j.finished)
		}()
	}
	return true
}

// Wait returns Done only after the late job has ended, like the registry does.
func (j *lateJob) Wait(ctx context.Context, id string, timeout time.Duration) (JobStatus, bool) {
	select {
	case <-j.finished:
		return JobStatus{ID: id, Done: true, Success: false, Error: "cancelled"}, true
	case <-ctx.Done():
	case <-time.After(timeout):
	}
	return JobStatus{ID: id}, true
}

// A cancelled reminder turn returns only after the resumed job has ended.
func TestResumeCancelWaitsForLateJob(t *testing.T) {
	job := &lateJob{cancelled: make(chan struct{}), finished: make(chan struct{})}

	oldResumer := getJobResumer()
	oldCanceler := getJobCanceler()
	oldWaiter := lookupWaiterForTest(t)
	SetJobResumer(lateResumer{job: job})
	SetJobCanceler(job)
	SetJobWaiter(job)
	t.Cleanup(func() {
		SetJobResumer(oldResumer)
		SetJobCanceler(oldCanceler)
		SetJobWaiter(oldWaiter)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := resumeSubagentTask(ctx, TaskSpec{Resume: "old-job", Task: "remind", Dir: t.TempDir()})
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !job.ended.Load() {
		t.Fatal("resumeSubagentTask returned while the resumed job could still write")
	}
}

// lookupWaiterForTest returns the waiter wired on the wait tool, or nil.
func lookupWaiterForTest(t *testing.T) JobWaiter {
	t.Helper()
	tool, ok := lookupTool("wait")
	if !ok {
		return nil
	}
	if wt, ok := tool.(*WaitTool); ok {
		return wt.waiter()
	}
	return nil
}
