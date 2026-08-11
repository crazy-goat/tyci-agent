package jobs

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type Registry struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

func NewRegistry() *Registry {
	return &Registry{jobs: make(map[string]*Job)}
}

var idCounter uint64

// nextID uses a timestamp prefix plus an atomic counter: unique within a
// single process is all that's required here, so pulling in a uuid
// dependency would add nothing.
func nextID() string {
	n := atomic.AddUint64(&idCounter, 1)
	return fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), n)
}

func (r *Registry) Start(ctx context.Context, description string, fn func(ctx context.Context) (result string, truncated bool, err error)) *Job {
	job := &Job{
		ID:          nextID(),
		Description: description,
		Status:      StatusRunning,
		StartedAt:   time.Now(),
		done:        make(chan struct{}),
	}

	r.mu.Lock()
	r.jobs[job.ID] = job
	r.mu.Unlock()

	go func() {
		result, truncated, err := fn(ctx)

		r.mu.Lock()
		job.FinishedAt = time.Now()
		job.Result = result
		switch {
		case err != nil:
			job.Status = StatusFailed
			job.Err = err.Error()
		case truncated:
			job.Status = StatusTruncated
		default:
			job.Status = StatusDone
		}
		r.mu.Unlock()

		close(job.done)
	}()

	return job
}

func (r *Registry) Get(id string) (*Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	return job, ok
}

func (r *Registry) List() []Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := make([]Job, 0, len(r.jobs))
	for _, job := range r.jobs {
		list = append(list, job.Snapshot())
	}
	return list
}

func (r *Registry) Wait(ctx context.Context, id string, timeout time.Duration) (*Job, bool) {
	r.mu.Lock()
	job, ok := r.jobs[id]
	r.mu.Unlock()
	if !ok {
		return nil, false
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-job.done:
	case <-timer.C:
	case <-ctx.Done():
	}

	r.mu.Lock()
	snapshot := job.Snapshot()
	r.mu.Unlock()
	return &snapshot, true
}
