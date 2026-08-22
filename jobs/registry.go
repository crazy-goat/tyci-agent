package jobs

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type Registry struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	onEvent func(Job)
}

func NewRegistry() *Registry {
	return &Registry{jobs: make(map[string]*Job)}
}

// SetOnEvent registers fn to be called (outside any internal lock, so fn
// may safely call back into the registry) whenever a job's status changes
// (on Start → running, and on completion → done/failed/truncated). nil is
// valid and is the default (no-op).
func (r *Registry) SetOnEvent(fn func(Job)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onEvent = fn
}

var idCounter uint64

// nextID uses a timestamp prefix plus an atomic counter: unique within a
// single process is all that's required here, so pulling in a uuid
// dependency would add nothing.
func nextID() string {
	n := atomic.AddUint64(&idCounter, 1)
	return fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), n)
}

// Start creates a job, registers it, and runs fn in its own goroutine. fn
// receives the job's own ctx plus its assigned job.ID — several tools (ask,
// report_progress) need to know "which job am I running inside", and the ID
// must be available with zero race window right as the goroutine starts;
// passing it as a plain argument is simplest and race-free since the ID is
// already assigned before the goroutine is launched.
func (r *Registry) Start(ctx context.Context, description string, fn func(ctx context.Context, jobID string) (result string, truncated bool, err error)) *Job {
	job := &Job{
		ID:          nextID(),
		Description: description,
		Status:      StatusRunning,
		StartedAt:   time.Now(),
		done:        make(chan struct{}),
	}

	r.mu.Lock()
	r.jobs[job.ID] = job
	onEvent := r.onEvent
	r.mu.Unlock()

	if onEvent != nil {
		onEvent(job.Snapshot())
	}

	go func() {
		result, truncated, err := fn(ctx, job.ID)

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
		snapshot := job.Snapshot()
		onEvent := r.onEvent
		// This job just became terminal, so this is exactly the moment the
		// retained-history bound can be exceeded. Prune before releasing the
		// lock; the snapshot above is already taken, so this job's own
		// completion event still reaches subscribers even in the (impossible
		// with a 50-job floor, but harmless) case where it were pruned here.
		r.pruneTerminalLocked()
		r.mu.Unlock()

		close(job.done)

		if onEvent != nil {
			onEvent(snapshot)
		}
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

// Ask lets the running job identified by id pose a blocking question to
// whoever is watching it (the main thread, or another agent), and blocks
// until Answer delivers a reply, ctx is cancelled, or the job's own ctx
// (passed to Start) is done — the latter is what naturally unblocks a
// forgotten ask once the job's own wall-clock timeout fires, since ctx here
// is expected to be that same job ctx (or a descendant of it).
//
// ok is false when id is unknown, or when ctx is done before an answer
// arrives; in both cases answer is "".
func (r *Registry) Ask(ctx context.Context, id, question string) (answer string, ok bool) {
	r.mu.Lock()
	job, found := r.jobs[id]
	if !found {
		r.mu.Unlock()
		return "", false
	}
	job.Status = StatusWaitingAnswer
	job.Question = question
	if job.answerCh == nil {
		job.answerCh = make(chan string, 1)
	}
	answerCh := job.answerCh
	onEvent := r.onEvent
	snapshot := job.Snapshot()
	r.mu.Unlock()

	if onEvent != nil {
		onEvent(snapshot)
	}

	var result string
	var got bool
	select {
	case ans := <-answerCh:
		result, got = ans, true
	case <-ctx.Done():
		result, got = "", false
	}

	r.mu.Lock()
	job.Status = StatusRunning
	job.Question = ""
	snapshot = job.Snapshot()
	onEvent = r.onEvent
	r.mu.Unlock()

	if onEvent != nil {
		onEvent(snapshot)
	}

	return result, got
}

// Answer delivers text to a job currently waiting on Ask, unblocking it.
// Returns false when id is unknown, the job is not currently
// StatusWaitingAnswer, or no answerCh exists yet — including the race where
// two answers arrive for the same question or the asker already gave up
// (ctx done), in which case the non-blocking send below simply does nothing.
func (r *Registry) Answer(id, text string) bool {
	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok || job.Status != StatusWaitingAnswer || job.answerCh == nil {
		r.mu.Unlock()
		return false
	}
	answerCh := job.answerCh
	r.mu.Unlock()

	select {
	case answerCh <- text:
		return true
	default:
		return false
	}
}

// SetProgress records a short status note for the job identified by id,
// visible via Snapshot().Progress without the job finishing. No restriction
// on the job's current status — allowed even if called oddly (e.g. after the
// job is already terminal), it's harmless. Returns false only when id is
// unknown.
func (r *Registry) SetProgress(id, text string) bool {
	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return false
	}
	job.Progress = text
	onEvent := r.onEvent
	snapshot := job.Snapshot()
	r.mu.Unlock()

	if onEvent != nil {
		onEvent(snapshot)
	}
	return true
}

// maxRetainedTerminalJobs bounds how many finished (done/failed/truncated)
// jobs the registry keeps. Running and waiting_answer jobs are never pruned.
//
// Without a bound the map grows for the whole session, and each entry holds
// its full Result string — for a backgrounded shell command that is up to
// the bash output cap (256 KiB, see tools/bash_output.go). A long session
// that backgrounds a few hundred commands would therefore retain tens of MB
// of output nobody can reach any more. 50 is well past the point where a
// model would still poll an old job_id with "wait".
const maxRetainedTerminalJobs = 50

// pruneTerminalLocked drops the oldest finished jobs beyond
// maxRetainedTerminalJobs. Caller must hold r.mu.
func (r *Registry) pruneTerminalLocked() {
	var terminal []*Job
	for _, job := range r.jobs {
		switch job.Status {
		case StatusRunning, StatusWaitingAnswer:
			// Still live — pruning it would break an in-flight wait/answer.
		default:
			terminal = append(terminal, job)
		}
	}
	if len(terminal) <= maxRetainedTerminalJobs {
		return
	}
	// Oldest first by completion time, so the survivors are the ones most
	// likely to still be polled.
	sort.Slice(terminal, func(i, j int) bool {
		return terminal[i].FinishedAt.Before(terminal[j].FinishedAt)
	})
	for _, job := range terminal[:len(terminal)-maxRetainedTerminalJobs] {
		delete(r.jobs, job.ID)
	}
}

// PendingLines describes the jobs that are still outstanding, one line each,
// blocked ones first.
//
// It exists for the agent loop's end-of-turn check (agent.Config.PendingJobs),
// and the ordering is the point: a running job is something to wait for or
// leave alone, while a job blocked on a question is a dead end that only the
// current turn can open — it makes no progress and its work is discarded when
// it times out.
func (r *Registry) PendingLines() []string {
	var blocked, running []string
	for _, j := range r.List() {
		switch j.Status {
		case StatusWaitingAnswer:
			blocked = append(blocked, fmt.Sprintf("WAITING FOR ANSWER: %s (job_id=%s) asks: %q", j.Description, j.ID, j.Question))
		case StatusRunning:
			line := fmt.Sprintf("running: %s (job_id=%s)", j.Description, j.ID)
			if j.Progress != "" {
				line += fmt.Sprintf(" — last reported: %s", j.Progress)
			}
			running = append(running, line)
		}
	}
	return append(blocked, running...)
}
