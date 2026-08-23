package jobs

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
func (r *Registry) Start(ctx context.Context, description string, kind Kind, parentID string, fn func(ctx context.Context, jobID string) (result string, truncated bool, err error)) *Job {
	now := time.Now()
	job := &Job{
		ID:          nextID(),
		Description: description,
		Kind:        kind,
		ParentID:    parentID,
		Status:      StatusRunning,
		StartedAt:   now,
		done:        make(chan struct{}),
	}
	// Seed lastActivity to the same instant as StartedAt, so a job that
	// hasn't done anything yet reads as "idle since start" (a small, correct
	// duration) rather than a zero time.Time producing a nonsense one. See
	// Job.lastActivity's doc comment.
	job.lastActivity = now.UnixNano()

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
// arrives; in both cases answer is "" and fromUser is false.
//
// fromUser reports who delivered the answer: true for a genuine human
// reply, false for another agent's reply via the "answer_job" tool (see
// tools/ask.go's AskTool, which uses this to mark the answer's provenance
// for the child that receives it). There is no dedicated human-facing
// command any more — a human's reply reaches a blocked job only by the
// model relaying it through "answer_job", which always passes false (see
// AnswerTool.Run) — but the flag itself stays part of the contract in case
// a future caller ever has a genuine human-sourced answer to deliver
// directly.
func (r *Registry) Ask(ctx context.Context, id, question string) (answer string, fromUser bool, ok bool) {
	r.mu.Lock()
	job, found := r.jobs[id]
	if !found {
		r.mu.Unlock()
		return "", false, false
	}
	job.Status = StatusWaitingAnswer
	job.Question = question
	if job.answerCh == nil {
		job.answerCh = make(chan jobAnswer, 1)
	}
	answerCh := job.answerCh
	onEvent := r.onEvent
	snapshot := job.Snapshot()
	r.mu.Unlock()

	if onEvent != nil {
		onEvent(snapshot)
	}

	var result jobAnswer
	var got bool
	select {
	case ans := <-answerCh:
		result, got = ans, true
	case <-ctx.Done():
		result, got = jobAnswer{}, false
	}

	r.mu.Lock()
	job.Status = StatusRunning
	job.Question = ""
	if !got {
		// ctx.Done() won the select before an answer arrived. A racing
		// Answer call reads job.answerCh under this same lock, so it can
		// still land a value into the OLD channel in the narrow window
		// between the select above and this Lock — Answer would report
		// "delivered" for a reply nobody is listening for any more.
		// Replacing the channel (instead of just draining it, which would
		// need this same lock anyway) means that stale value, if one
		// lands, is orphaned on a channel this job will never read from
		// again — not left buffered for the NEXT Ask on this job to
		// mistake for its own answer.
		job.answerCh = nil
	}
	snapshot = job.Snapshot()
	onEvent = r.onEvent
	r.mu.Unlock()

	if onEvent != nil {
		onEvent(snapshot)
	}

	return result.text, result.fromUser, got
}

// Answer delivers text to a job currently waiting on Ask, unblocking it.
// fromUser must be true for a genuine human reply and false for another
// agent's reply (the only caller today, the "answer_job" tool, always
// passes false — see its doc comment) — Ask hands it back to the asker so
// the answer's provenance survives the round trip.
//
// Returns false when id is unknown, the job is not currently
// StatusWaitingAnswer, or no answerCh exists yet — including the race where
// two answers arrive for the same question or the asker already gave up
// (ctx done), in which case the non-blocking send below simply does nothing.
func (r *Registry) Answer(id, text string, fromUser bool) bool {
	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok || job.Status != StatusWaitingAnswer || job.answerCh == nil {
		r.mu.Unlock()
		return false
	}
	answerCh := job.answerCh
	r.mu.Unlock()

	select {
	case answerCh <- jobAnswer{text: text, fromUser: fromUser}:
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

// TouchActivity records that the job identified by id showed a fresh sign of
// life (streamed text/thinking/tool-call event, or a backgrounded bash
// output line) right now. It is the hot path this whole mechanism exists
// for — called on every streamed token from every running job — so unlike
// SetProgress it deliberately does NOT snapshot the job or fire onEvent: it
// only takes r.mu briefly for the map lookup (unavoidable, since the jobs
// map can be mutated concurrently by pruning), then releases it before
// writing the timestamp via the job's own atomic field. Nothing else the
// registry mutex guards is touched here, so this never contends with
// Start/List/Ask/Answer/SetProgress. A call for an unknown id is silently a
// no-op.
func (r *Registry) TouchActivity(id string) {
	r.mu.Lock()
	job, ok := r.jobs[id]
	r.mu.Unlock()
	if !ok {
		return
	}
	job.touchActivity()
}

// Post enqueues text for delivery to the job identified by id at its own
// agent loop's next iteration boundary (see DrainMessages). This is how a
// running background subagent gets steered mid-flight, either by a person
// via the "/msg" slash command or by the parent model via the "message"
// tool — the mechanism that already exists for the main agent
// (agent.Config.NextMessages) applied per-job instead of process-wide.
//
// Returns false when id is unknown; the caller should tell whoever posted
// that the job doesn't exist rather than silently dropping the message. No
// restriction on the job's current status — posting to a job that is about
// to finish (or has just finished) is harmless, same as SetProgress: the
// message simply never gets drained.
func (r *Registry) Post(id, text string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return false
	}
	job.mailbox = append(job.mailbox, text)
	return true
}

// DrainMessages pops and returns everything queued for job id via Post,
// FIFO, clearing the mailbox. Mirrors how the main agent's pending-input
// queue is drained (agent.Config.NextMessages) — this is the per-job
// equivalent, meant to be called from that same job's own agent loop
// between iterations. An unknown id and an empty mailbox both return nil,
// indistinguishably — the caller (a NextMessages-shaped callback) treats
// both the same way: nothing to inject this iteration.
func (r *Registry) DrainMessages(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return nil
	}
	msgs := job.mailbox
	job.mailbox = nil
	return msgs
}

// Resolve maps id — a job's full ID, or the short form the jobs panel shows
// ("#N", with or without the leading "#") — to that job's full ID. Short
// ids never collide: ShortID trims to the trailing counter of the
// "job-<unixnano>-<n>" scheme, and n is a single atomic counter shared by
// every job in the process, so at most one job can ever have a given short
// form. ok is false when neither form matches anything currently in the
// registry (including an id that has since been pruned — see
// pruneTerminalLocked).
func (r *Registry) Resolve(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.jobs[id]; ok {
		return id, true
	}
	short := strings.TrimPrefix(id, "#")
	if short == "" {
		return "", false
	}
	for full, job := range r.jobs {
		if ShortID(job.ID) == short {
			return full, true
		}
	}
	return "", false
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
//
// Exported as MaxRetainedTerminalJobs so any other mirror of this registry's
// contents (e.g. display.TuiModel.backgroundJobs, fed by SetJobEventBus) can
// prune itself to the same bound instead of drifting from — and having its
// footer text lie about — the registry's actual retention.
const maxRetainedTerminalJobs = MaxRetainedTerminalJobs

// MaxRetainedTerminalJobs is maxRetainedTerminalJobs's exported mirror — see
// its doc comment for the rationale. A plain const alias (not a duplicated
// literal) so the two can never drift apart.
const MaxRetainedTerminalJobs = 50

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
