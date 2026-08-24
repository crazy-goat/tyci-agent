package jobs

import (
	"context"
	"errors"
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
var extensionIDCounter uint64

const (
	maxExtensionDuration = 10 * time.Minute
	maxExtensionReason   = 1024
)

// nextID uses a timestamp prefix plus an atomic counter: unique within a
// single process is all that's required here, so pulling in a uuid
// dependency would add nothing.
func nextID() string {
	n := atomic.AddUint64(&idCounter, 1)
	return fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), n)
}

func nextExtensionID() string {
	return fmt.Sprintf("extension-%d", atomic.AddUint64(&extensionIDCounter, 1))
}

// Start creates a job, registers it, and runs fn in its own goroutine. fn
// receives the job's own ctx plus its assigned job.ID — several tools (ask,
// report_progress) need to know "which job am I running inside", and the ID
// must be available with zero race window right as the goroutine starts;
// passing it as a plain argument is simplest and race-free since the ID is
// already assigned before the goroutine is launched.
//
// The job's ctx is derived here (WithCancel) rather than passed through:
// the registry keeps the cancel, which is what makes an outside caller able
// to stop a running job at all — see Cancel. Callers that already hand in a
// cancelable ctx keep working unchanged; this only adds one more link on top
// of whatever they supplied.
func (r *Registry) Start(ctx context.Context, description string, kind Kind, parentID string, fn func(ctx context.Context, jobID string) (result string, truncated bool, err error)) *Job {
	now := time.Now()
	jobCtx := ctx
	var cancel context.CancelFunc
	var extensionCtx *resettableDeadlineContext
	if deadline, ok := ctx.Deadline(); ok {
		// Keep values and the explicit parent cancellation signal, but detach
		// the source deadline itself. The resettable timer owns the deadline.
		values := context.WithoutCancel(ctx)
		signal, parentCancel := context.WithCancel(context.Background())
		stopParent := context.AfterFunc(ctx, func() {
			if ctx.Err() == context.Canceled {
				parentCancel()
			}
		})
		extensionCtx = newResettableDeadlineContext(values, signal, deadline)
		cancel = func() {
			parentCancel()
			stopParent()
			extensionCtx.cancel(context.Canceled)
		}
		jobCtx = extensionCtx
	} else {
		jobCtx, cancel = context.WithCancel(ctx)
	}
	job := &Job{
		ID:           nextID(),
		Description:  description,
		Kind:         kind,
		ParentID:     parentID,
		Status:       StatusRunning,
		StartedAt:    now,
		done:         make(chan struct{}),
		cancel:       cancel,
		extensionCtx: extensionCtx,
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
		result, truncated, err := fn(jobCtx, job.ID)
		cancel()

		r.mu.Lock()
		job.FinishedAt = time.Now()
		job.Result = result
		r.clearPendingExtensionLocked(job)
		switch {
		case err != nil:
			job.Status = StatusFailed
			job.Err = err.Error()
			// A bare "context canceled" tells whoever reads the jobs panel
			// nothing about who cancelled. When Cancel was the cause, say so
			// instead — same failed status, an unmistakable message (see
			// Cancel's doc comment for why there is no sixth status).
			if errors.Is(err, context.Canceled) && job.cancelled {
				job.Err = ErrStoppedByUser.Error()
			}
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

// Cancel stops the running job identified by id — resolving short forms
// ("#3"/"3") through Resolve first — by cancelling its context and, because
// a subagent's backgrounded commands are registered with that job as their
// parent, every still-running descendant's too. Descendants are cancelled
// BEFORE the target: the deepest work dies first, so a child's completion
// path never races its parent already being torn down above it.
//
// Returns false when id resolves to nothing or to a job that has already
// finished (done/failed/truncated/waiting_answer counts as finished enough
// not to need stopping); the caller should report "not a running job"
// rather than claim success. A waiting_answer job is refused on purpose: it
// is not making progress, but killing someone's blocked question out from
// under them is not kill_job's call — answering or ignoring it is.
//
// The cancel funcs run OUTSIDE r.mu on purpose: a job's parting code path
// (its fn returning into Start's completion bookkeeping) takes r.mu itself,
// and calling cancel while holding the lock would deadlock against exactly
// those jobs.
//
// A job stopped by Cancel still ends as StatusFailed (no sixth status was
// added), but its recorded error says it was stopped deliberately rather
// than showing a bare "context canceled" — see Job.cancelled.
func (r *Registry) Cancel(id string) bool {
	full, ok := r.Resolve(id)
	if !ok {
		return false
	}

	// Collect everything under r.mu, call nothing under it.
	r.mu.Lock()

	target, ok := r.jobs[full]
	if !ok || !r.cancelableLocked(target) {
		r.mu.Unlock()
		return false
	}

	// Deepest-first subtree walk over ParentID links (see
	// subtreeOrderLocked). List() would snapshot; reading the live map
	// under the lock we already hold is equivalent and cheaper.
	order := r.subtreeOrderLocked(target)

	for _, job := range order {
		job.cancelled = true
	}
	kills := make([]context.CancelFunc, 0, len(order))
	for _, job := range order {
		if job.cancel != nil {
			kills = append(kills, job.cancel)
		}
	}
	r.mu.Unlock()

	for _, kill := range kills {
		kill()
	}
	return true
}

// CancelAll cancels every job that exists at the conversation boundary and
// waits for live jobs to finish. It returns every known ID, including terminal
// jobs, so consumers that mirror registry events can reject late events from
// the old conversation after the reset. A second pass closes the small race in
// which a cancelled job registers one last child before observing cancellation.
func (r *Registry) CancelAll() []string {
	known := make(map[string]bool)
	for {
		r.mu.Lock()
		var selected []*Job
		var kills []context.CancelFunc
		for id, job := range r.jobs {
			known[id] = true
			if job.Status != StatusRunning && job.Status != StatusWaitingAnswer {
				continue
			}
			job.cancelled = true
			selected = append(selected, job)
			if job.cancel != nil {
				kills = append(kills, job.cancel)
			}
		}
		r.mu.Unlock()

		for _, kill := range kills {
			kill()
		}
		for _, job := range selected {
			<-job.done
		}
		if len(selected) == 0 {
			break
		}
	}

	ids := make([]string, 0, len(known))
	for id := range known {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// subtreeOrderLocked returns the target's subtree in kill order: deepest
// descendants first, target last. The visited set makes the walk terminate
// on malformed links — a cycle (only possible if a test or future feature
// fabricates ParentIDs) or two jobs claiming the same child — instead of
// looping forever. Caller must hold r.mu.
func (r *Registry) subtreeOrderLocked(target *Job) []*Job {
	order := make([]*Job, 0, 8)
	visited := make(map[*Job]bool, 8)
	var walk func(job *Job)
	walk = func(job *Job) {
		if visited[job] {
			return
		}
		visited[job] = true
		for _, candidate := range r.jobs {
			if candidate.ParentID == job.ID && !visited[candidate] {
				walk(candidate)
			}
		}
		order = append(order, job)
	}
	walk(target)
	return order
}

// cancelableLocked reports whether a stop request on this job can act at
// all. Caller must hold r.mu.
func (r *Registry) cancelableLocked(job *Job) bool {
	return job.Status == StatusRunning
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

// RequestExtension registers one bounded timeout-extension request for a
// running job. It returns immediately; the caller can notify the parent
// before waiting for ResolveExtension.
func (r *Registry) RequestExtension(id string, seconds time.Duration, reason string) (string, bool) {
	if seconds <= 0 || seconds > maxExtensionDuration || len(reason) == 0 || len(reason) > maxExtensionReason {
		return "", false
	}
	full, ok := r.Resolve(id)
	if !ok {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[full]
	if !ok || job.Status != StatusRunning || job.extensionCtx == nil || job.ExtensionAccepted || job.ExtensionPending {
		return "", false
	}
	deadline, _ := job.extensionCtx.Deadline()
	if !time.Now().Before(deadline) || job.extensionCtx.Err() != nil {
		return "", false
	}
	requestID := nextExtensionID()
	job.ExtensionRequestID = requestID
	job.ExtensionSeconds = seconds
	job.ExtensionReason = reason
	job.ExtensionPending = true
	job.ExtensionAccepted = false
	job.extensionResolved = false
	job.extensionApproved = false
	job.extensionDecision = make(chan bool, 1)
	return requestID, true
}

// clearPendingExtensionLocked invalidates an in-flight request without
// delivering a decision. It is used when the job itself becomes terminal or
// when WaitExtension's caller abandons the request, so a later answer cannot
// be mistaken for a live request. Caller must hold r.mu.
func (r *Registry) clearPendingExtensionLocked(job *Job) {
	if !job.ExtensionPending {
		return
	}
	job.ExtensionPending = false
	job.extensionDecision = nil
	job.extensionResolved = true
	job.extensionApproved = false
	job.ExtensionRequestID = ""
	job.ExtensionSeconds = 0
	job.ExtensionReason = ""
}

// WaitExtension waits for a decision or the job's own context to end.
func (r *Registry) WaitExtension(ctx context.Context, id, requestID string) (bool, bool) {
	full, found := r.Resolve(id)
	if !found {
		return false, false
	}
	r.mu.Lock()
	job, found := r.jobs[full]
	if !found || job.ExtensionRequestID != requestID {
		r.mu.Unlock()
		return false, false
	}
	if !job.ExtensionPending {
		approved := job.extensionApproved
		resolved := job.extensionResolved
		r.mu.Unlock()
		return approved, resolved
	}
	if job.extensionDecision == nil || job.extensionCtx == nil {
		r.mu.Unlock()
		return false, false
	}
	decision := job.extensionDecision
	jobCtx := job.extensionCtx
	r.mu.Unlock()
	select {
	case approved := <-decision:
		return approved, true
	case <-ctx.Done():
		r.mu.Lock()
		if job.ExtensionRequestID == requestID && job.ExtensionPending && job.extensionDecision == decision {
			r.clearPendingExtensionLocked(job)
		}
		r.mu.Unlock()
		return false, false
	case <-jobCtx.Done():
		r.mu.Lock()
		if job.ExtensionRequestID == requestID && job.ExtensionPending && job.extensionDecision == decision {
			r.clearPendingExtensionLocked(job)
		}
		r.mu.Unlock()
		return false, false
	}
}

// ResolveExtension consumes a pending request. Approval extends the current
// deadline by the requested duration. Late, stale, duplicate, and cancelled
// requests return false.
func (r *Registry) ResolveExtension(id, requestID string, approve bool) bool {
	full, found := r.Resolve(id)
	if !found {
		return false
	}
	r.mu.Lock()
	job, found := r.jobs[full]
	if !found || !job.ExtensionPending || job.ExtensionRequestID != requestID || job.extensionDecision == nil || job.Status != StatusRunning || job.extensionResolved {
		r.mu.Unlock()
		return false
	}
	deadline, _ := job.extensionCtx.Deadline()
	if job.extensionCtx.Err() != nil || !time.Now().Before(deadline) {
		r.clearPendingExtensionLocked(job)
		r.mu.Unlock()
		return false
	}
	if approve {
		if !job.extensionCtx.extend(job.ExtensionSeconds) {
			r.clearPendingExtensionLocked(job)
			r.mu.Unlock()
			return false
		}
		job.ExtensionAccepted = true
	}
	job.ExtensionPending = false
	job.extensionResolved = true
	job.extensionApproved = approve
	decision := job.extensionDecision
	job.extensionDecision = nil
	r.mu.Unlock()
	select {
	case decision <- approve:
	default:
	}
	return true
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

// Post enqueues text for delivery to a live job at its own agent loop's next
// iteration boundary (see DrainMessages). Running and waiting-for-answer jobs
// are live; terminal jobs are not and cannot receive messages.
//
// Returns false when id is unknown or terminal. Callers that need to explain
// the distinction should resolve the id first and use IsLive.
func (r *Registry) Post(id, text string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok || !jobLive(job.Status) {
		return false
	}
	job.mailbox = append(job.mailbox, text)
	return true
}

// IsLive reports whether id identifies a job that can still receive a
// message. Waiting-for-answer is live: it can resume its loop after the
// answer arrives, and message delivery remains valid at that boundary.
func (r *Registry) IsLive(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	return ok && jobLive(job.Status)
}

func jobLive(status Status) bool {
	return status == StatusRunning || status == StatusWaitingAnswer
}

// DrainMessages pops and returns everything queued for job id via Post,
// FIFO, clearing the mailbox. Mirrors how the main agent's pending-input
// queue is drained (agent.Config.NextMessages) — the per-job
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
// HasActiveSubagents reports whether at least one subagent is genuinely
// running. A waiting-for-answer child is intentionally excluded: it needs the
// normal job reminder so the model can relay or answer its question.
func (r *Registry) HasActiveSubagents() bool {
	for _, j := range r.List() {
		if j.Kind == KindSubagent && j.Status == StatusRunning {
			return true
		}
	}
	return false
}

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
