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

	// tombstones and tombstoneOrder hold pruned SUBAGENT jobs' final
	// snapshots (see tombstoneCap's doc comment) — a separate, independently
	// bounded pool from jobs itself, so a burst of pruned BASH jobs (which
	// are never tombstoned) can never evict a subagent's tombstoned result,
	// and vice versa. tombstoneOrder is the FIFO insertion order used to
	// evict the oldest tombstone once tombstoneCap is exceeded.
	tombstones     map[string]Job
	tombstoneOrder []string
}

func NewRegistry() *Registry {
	return &Registry{jobs: make(map[string]*Job), tombstones: make(map[string]Job)}
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

// panicError turns a recovered panic value into a normal job error. Formatting
// arbitrary panic values can call user-defined Formatter, String, or Error
// methods, so keep the formatting itself behind a recovery boundary too.
func panicError(value any) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("job function panicked: <unprintable panic value>")
		}
	}()
	description := fmt.Sprintf("%v", value)
	// The fmt package normally catches formatter panics and puts a diagnostic
	// marker in its output. Do not expose that implementation detail as the
	// job's error; it is the same unreadable case as a panic escaping here.
	if strings.Contains(description, "%!") {
		description = "<unprintable panic value>"
	}
	return fmt.Errorf("job function panicked: %s", description)
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
		ID:            nextID(),
		Description:   description,
		Kind:          kind,
		ParentID:      parentID,
		Status:        StatusRunning,
		StartedAt:     now,
		done:          make(chan struct{}),
		cancel:        cancel,
		extensionCtx:  extensionCtx,
		statusChanged: make(chan struct{}),
	}
	// Seed lastActivity to the same instant as StartedAt, so a job that
	// hasn't done anything yet reads as "idle since start" (a small, correct
	// duration) rather than a zero time.Time producing a nonsense one. See
	// Job.lastActivity's doc comment.
	job.lastActivity = now.UnixNano()
	// Same seeding for LastProgressAt — see its doc comment. Without this a
	// freshly started job would read as having gone quiet since the Unix
	// epoch, immediately eligible for a progress-heartbeat nudge.
	job.LastProgressAt = now

	r.mu.Lock()
	r.jobs[job.ID] = job
	onEvent := r.onEvent
	r.mu.Unlock()

	if onEvent != nil {
		onEvent(job.Snapshot())
	}

	go func() {
		var result string
		var truncated bool
		var err error
		returned := false

		// F4: this bookkeeping MUST run no matter how fn's frame unwinds —
		// normal return, a recovered panic (both already flow through the
		// inner recover below into err/result/truncated), or fn calling
		// runtime.Goexit() (directly, or via something like a testing.T
		// FailNow deep in its call chain). Goexit runs every deferred call
		// on its way up the goroutine's stack but never resumes ordinary
		// control flow in any of those frames — code that merely comes
		// AFTER the inner func() call below is skipped entirely on that
		// path. Before this was a defer, that is exactly what happened: the
		// recover-based defer inside the inner func() would fire and set
		// err, but cancel()/the registry bookkeeping/close(job.done) that
		// followed it as plain sequential code never ran, leaving the job
		// stuck in StatusRunning forever and Wait with no completion event
		// to ever wake it. Making this whole block a defer on the OUTER
		// frame fixes that: Goexit's unwind reaches it just like a panic or
		// a normal return would.
		defer func() {
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
			// C3 (batch-2 review): sweep whatever is still sitting in mailbox
			// into the snapshot BEFORE anything else can happen to it — this
			// job's own agent loop just stopped for good, so nothing will ever
			// drain it through the normal path again, and once pruneTerminalLocked
			// below (or a later prune) removes this job from r.jobs, even
			// DrainMessages could no longer reach it. See ResidualMailbox's doc
			// comment for why leaving this silently in place instead was a bug.
			job.ResidualMailbox = job.mailbox
			job.mailbox = nil
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

		func() {
			defer func() {
				if recovered := recover(); !returned {
					// A panic in a job must follow the same terminal path as an
					// ordinary function error. In particular, keep the completion
					// event and done signal intact so Wait and the interactive
					// process do not get stranded by a bad job function.
					//
					// recovered == nil here does NOT mean "someone panicked with
					// a nil value": since Go 1.21, panic(nil) is converted to a
					// non-nil *runtime.PanicNilError by default, so recover()
					// only comes back nil when this defer fired without a panic
					// at all — i.e. fn called runtime.Goexit() (directly, or via
					// something like testing.T.FailNow buried in its call
					// chain), which still runs deferred calls on its way up the
					// goroutine's stack but never lets control return normally.
					if recovered == nil {
						err = errors.New("job function exited via runtime.Goexit() without returning")
					} else {
						err = panicError(recovered)
					}
					result = ""
					truncated = false
				}
			}()
			result, truncated, err = fn(jobCtx, job.ID)
			returned = true
		}()
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
// finished (done/failed/truncated); the caller should report "not a running
// job" rather than claim success. A waiting_answer job is cancellable: its
// Ask call is blocked on the job context, so cancelling that context wakes it
// and lets the job terminate instead of leaving it stuck indefinitely.
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
	return job.Status == StatusRunning || job.Status == StatusWaitingAnswer
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

// WaiterCount reports how many callers are currently blocked inside Wait
// (not WaitObserve — see its doc comment) for id, 0 for an unknown id.
// Production code has no use for this; it exists so a test can synchronize
// on "a Wait call has actually registered" instead of guessing with a
// sleep before triggering the transition it wants Wait to catch.
func (r *Registry) WaiterCount(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return 0
	}
	return job.waiters
}

// signalStatusChangeLocked wakes every Wait call currently blocked on this
// job's statusChanged (a select reading from a channel that is closed right
// under it fires immediately, with no ordering requirement against when the
// select was entered) and arms a fresh channel for the next transition. Ask
// is the only caller today, and only for the transition INTO
// StatusWaitingAnswer — see Wait's doc comment on why the reverse
// transition must not also signal. Caller must hold r.mu.
func (r *Registry) signalStatusChangeLocked(job *Job) {
	close(job.statusChanged)
	job.statusChanged = make(chan struct{})
}

// Wait blocks until id finishes, enters StatusWaitingAnswer, timeout
// elapses, or ctx is done — see waitInternal. It counts as a waiter for
// Job.QuestionHasWaiter's purposes: this is the shape that hands its
// result back to whoever called it (the "wait" tool, in production), which
// is what makes it a genuine second delivery path for a pending question.
// A caller that blocks on the same signal WITHOUT reporting the question
// to anyone must use WaitObserve instead (see its doc comment for why —
// batch-2 review finding C1).
func (r *Registry) Wait(ctx context.Context, id string, timeout time.Duration) (*Job, bool) {
	return r.waitInternal(ctx, id, timeout, true)
}

// WaitObserve behaves exactly like Wait — blocking until id finishes,
// enters StatusWaitingAnswer, timeout elapses, or ctx is done — but does
// NOT count toward Job.waiters, so it has no effect on QuestionHasWaiter.
//
// It exists for a caller that watches a job for its OWN purposes without
// itself becoming a delivery path for the question it might see — see
// tools/subagent.go's runWithHandoff, whose watcher only wakes an
// unrelated select; it never hands the question back to anyone. Before
// this method existed, that watcher used Wait, so Ask saw waiters>0 for
// essentially every blocking subagent call and suppressed the onEvent
// notice — the ONLY delivery the question had, since the watcher itself
// delivers nothing. That was batch-2 review finding C1: coupling "is
// blocked in Wait" with "will report the question" through one counter is
// wrong whenever a caller does the former without the latter.
func (r *Registry) WaitObserve(ctx context.Context, id string, timeout time.Duration) (*Job, bool) {
	return r.waitInternal(ctx, id, timeout, false)
}

// waitInternal is Wait and WaitObserve's shared implementation. countAsWaiter
// distinguishes them — see Wait's and WaitObserve's doc comments.
func (r *Registry) waitInternal(ctx context.Context, id string, timeout time.Duration, countAsWaiter bool) (*Job, bool) {
	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok {
		// id may have been pruned from r.jobs (pruneTerminalLocked) after
		// finishing — if it was a subagent job, its final snapshot survives
		// in tombstones (see tombstoneCap's doc comment), so degrade
		// gracefully to the real, already-terminal result instead of
		// reporting "unknown job_id" for something that actually finished.
		if snap, tombstoned := r.tombstones[id]; tombstoned {
			r.mu.Unlock()
			return &snap, true
		}
		r.mu.Unlock()
		return nil, false
	}
	// Registered while still holding r.mu so Ask (which also takes r.mu to
	// flip Status) can never see a stale count — either this call is
	// counted before Ask reads waiters, or Ask's whole status flip (and
	// QuestionHasWaiter decision) happens before this one is registered.
	// See jobs.Job's waiters doc comment and Ask's QuestionHasWaiter use.
	if countAsWaiter {
		job.waiters++
	} else {
		job.observers++
	}
	statusCh := job.statusChanged
	r.mu.Unlock()

	// FRAGILE (batch-2 review round 2 finding D6): everything between here
	// and the matching job.waiters--/job.observers-- below runs with NO
	// defer to guarantee the decrement happens — that is deliberate (see
	// the decrement's own doc comment for why: it must share a single
	// critical section with the snapshot, which a defer running after a
	// second Lock cannot do), but it means a future early return added
	// anywhere in this stretch would leak the increment permanently. A
	// permanently-leaked waiters count permanently sets QuestionHasWaiter
	// for that job, which permanently suppresses its ask notices —
	// silently reintroducing the exact bug (C1) this counter exists to
	// avoid. Do not add a return between this point and the decrement; if
	// a new exit path is ever needed here, thread it through so the
	// decrement still runs on every path, in the same critical section as
	// whatever snapshot it takes.
	//
	// statusCh is closed the moment Ask flips this job INTO
	// StatusWaitingAnswer (see signalStatusChangeLocked) — never on the way
	// back out, which would wake an unrelated, non-looping call the
	// instant some other question got answered and hand it a mid-flight
	// "running" snapshot instead of the terminal one it actually wants. A
	// caller that does need to notice the return-to-running transition too
	// (there isn't one today) would have to poll for it, same as before.
	//
	// This gives the one transition that matters the same real-time wake
	// job.done already gives a normal completion, instead of waiting out
	// the rest of timeout and finding out on the next poll. Deliberately
	// NOT special-cased for "already waiting when called": a caller in
	// that state still wants an ordinary bounded wait for whatever happens
	// NEXT (an answer arriving, the job being cancelled, ...), not an
	// instant return of a status it may already know about.
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-job.done:
	case <-statusCh:
	case <-timer.C:
	case <-ctx.Done():
	}

	// The decrement happens in the SAME critical section as the snapshot
	// below — not deferred to run after a second, separate Lock/Unlock.
	// Deferring it (as an earlier version of this code did) left a window
	// where this call had already taken its snapshot — possibly "running",
	// with an empty Question, because its own timeout or ctx was what
	// ended the select — while still counted as a waiter a fraction longer.
	// Ask could observe waiters>0 in exactly that window and suppress the
	// notice for a caller that had, in fact, already returned without ever
	// seeing the question (batch-2 review finding C2). Folding the
	// decrement into this lock hold means Ask can only ever see this
	// call either fully registered (strictly before its snapshot) or
	// fully gone (strictly after) — never a state the two disagree about.
	r.mu.Lock()
	if countAsWaiter {
		job.waiters--
	} else {
		job.observers--
	}
	snapshot := job.Snapshot()
	r.mu.Unlock()
	return &snapshot, true
}

// ObserverCount reports how many callers are currently blocked inside
// WaitObserve (not Wait — see WaiterCount) for id, 0 for an unknown id.
// Production code has no use for this; it exists so a test can synchronize
// on "a WaitObserve call has actually registered" instead of a settle
// sleep before triggering the transition it wants to catch (batch-2 review
// round 2's "optional if cheap" suggestion).
func (r *Registry) ObserverCount(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return 0
	}
	return job.observers
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
	// See jobs.Job's QuestionHasWaiter doc comment: recorded now, while
	// still holding r.mu, so it reflects exactly the set of Wait calls that
	// were registered before this question existed — not a count that
	// could still change underneath it.
	job.QuestionHasWaiter = job.waiters > 0
	r.signalStatusChangeLocked(job)
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
	job.QuestionHasWaiter = false
	// Deliberately NOT signalStatusChangeLocked here — see its call above
	// this select and its doc comment: only the transition INTO
	// StatusWaitingAnswer wakes a blocked Wait early. Signaling this one
	// too would wake an ordinary, non-looping Wait call (e.g. a plain
	// "wait until this job finishes") the instant a completely unrelated
	// question got answered, handing back a mid-flight "running" snapshot
	// instead of the terminal one that call actually wants.
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

// progressHistoryCap bounds the number of notes kept per job
// (Job.ProgressHistory), oldest evicted first once full — see
// SetProgress. This is NOT an overflow guard for a rare event: an explicit
// "report_progress" call is voluntary and rare (tools/progress.go's
// ReportProgressTool doc comment), but that tool is not this cap's
// dominant caller. A backgrounded shell command posts one of these on
// every output line it produces, throttled to at most one per second (see
// tools/bash.go's bashRun.setProgress and bgProgressInterval) — for any
// build or test run that keeps printing for more than ~20 seconds, which
// is the ordinary case, not the unusual one. So this cap is a SLIDING
// TAIL over a steady, roughly 1/s stream, not a rare-overflow backstop:
// its job is to say which ~20 seconds of that stream a caller still sees,
// not to catch something that almost never happens.
const progressHistoryCap = 20

// progressEntryRuneCap bounds a single note passed to SetProgress in
// RUNES — never bytes, per this repo's documented history of byte-slicing
// truncation bugs (see truncateTombstoneField's doc comment). An explicit
// "report_progress" call is model-supplied text with no length limit
// enforced anywhere upstream of here; a backgrounded shell command's own
// output line is already cut to 120 runes by tools/bash.go's truncateLine
// before it ever reaches here, well inside this cap, so this backstop
// exists for the caller that has no cap of its own — without it, a single
// report_progress call could make one job's history dominate memory on
// its own, independent of progressHistoryCap.
const progressEntryRuneCap = 2000

// SetProgress records a short status note for the job identified by id,
// visible via Snapshot().Progress (the latest note) and
// Snapshot().ProgressHistory (the full retained sequence) without the job
// finishing. No restriction on the job's current status — allowed even if
// called oddly (e.g. after the job is already terminal), it's harmless.
// Returns false only when id is unknown.
func (r *Registry) SetProgress(id, text string) bool {
	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return false
	}
	entry := truncateProgressEntry(text)
	job.Progress = entry
	job.LastProgressAt = time.Now()
	job.ProgressHistory = append(job.ProgressHistory, entry)
	if len(job.ProgressHistory) > progressHistoryCap {
		// Drop the oldest and record that we did — see
		// Job.ProgressHistoryTruncated's doc comment for why this bit has
		// to exist: a bounded history that silently drops entries is
		// indistinguishable from a complete one otherwise.
		job.ProgressHistory = job.ProgressHistory[len(job.ProgressHistory)-progressHistoryCap:]
		job.ProgressHistoryTruncated = true
	}
	onEvent := r.onEvent
	snapshot := job.Snapshot()
	r.mu.Unlock()

	if onEvent != nil {
		onEvent(snapshot)
	}
	return true
}

// truncateProgressEntry truncates a single report_progress note to at most
// progressEntryRuneCap runes, appending an ellipsis when it actually cut
// something. Rune-based, never byte-based — see truncateTombstoneField's
// doc comment for why byte slicing is not an option in this codebase.
func truncateProgressEntry(s string) string {
	runes := []rune(s)
	if len(runes) <= progressEntryRuneCap {
		return s
	}
	return string(runes[:progressEntryRuneCap]) + "…"
}

// NeedsProgressHeartbeat reports whether the RUNNING job identified by id
// should be nudged, right now, to post a report_progress note — because more
// than `after` has elapsed since the later of: this job's start, its last
// real progress note (LastProgressAt), or the last time this same method
// already returned true for it (lastHeartbeatNudgeAt). That last clause is
// the trap item 15's spec calls out explicitly: without it, a child that
// never calls report_progress would get nagged on every single iteration
// after first crossing the threshold, instead of at most once per `after`
// interval.
//
// This is a single check-AND-set, not two separate calls — a true result
// stamps lastHeartbeatNudgeAt to now in the same critical section, so a
// caller polling this once per loop iteration can never observe a stale
// "yes, nudge" for longer than one call. Same discipline as Ask's
// QuestionHasWaiter above.
//
// Returns false for a non-running job (StatusWaitingAnswer is already a dead
// end the parent has to unblock, not something a self-nudge fixes; a
// terminal job has nothing left to report) or an unknown id, and for
// after <= 0 (nothing would ever elapse).
func (r *Registry) NeedsProgressHeartbeat(id string, after time.Duration) bool {
	if after <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok || job.Status != StatusRunning {
		return false
	}
	reference := job.LastProgressAt
	if job.lastHeartbeatNudgeAt.After(reference) {
		reference = job.lastHeartbeatNudgeAt
	}
	if time.Since(reference) < after {
		return false
	}
	job.lastHeartbeatNudgeAt = time.Now()
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

// tombstoneCap bounds how many pruned SUBAGENT jobs' final snapshots stay
// retrievable through Wait after eviction from the main jobs map (see
// pruneTerminalLocked). Only KindSubagent jobs are tombstoned — never
// KindBash — for two reasons: a bash job's completion notice already
// includes its output inline (unlike a subagent's, which is truncated to
// an 800-rune preview — see tools/subagent.go's subagentCompletionNotice),
// so there is nothing extra worth keeping around for it; and a bash job's
// Result can be up to the 256 KiB bash output cap, so tombstoning it too
// would let a handful of huge shell outputs crowd out every subagent
// result. Keeping this pool subagent-only, and separate from
// maxRetainedTerminalJobs's own jobs map, means a burst of pruned bash jobs
// can never evict a tombstoned subagent result (or the reverse).
//
// 200 — 4x maxRetainedTerminalJobs — because a tombstone is exactly the
// safety net for the case maxRetainedTerminalJobs already treats as "well
// past the point where a model would still poll": a long session that runs
// many subagents in the background and only gets back to polling one much
// later.
//
// A count alone is NOT actually a bound on memory: a subagent's Result is
// its child's entire final answer, with nothing capping its length
// anywhere upstream (unlike bash's 256 KiB output cap), and Description/
// Err/Progress/ExtensionReason are also caller/model-supplied strings with
// no size limit enforced here (ExtensionReason happens to be capped at
// maxExtensionReason — 1024 bytes — by RequestExtension today, but that is
// a decision the extension feature owns, not one this tombstone should
// depend on staying true). 200 x unbounded is not a small, fixed cost — it
// is a 4x retention increase (50 live + 200 tombstoned) dressed up as one.
// So tombstoneLocked below truncates each of those five string fields (via
// truncateTombstoneField/truncateTombstoneResult, UTF-8-safe — never slice
// a string by byte offset mid-rune) to tombstoneFieldRuneCap runes before
// storing the snapshot. That makes the actual worst case honest and
// computable: at most tombstoneCap * 5 * tombstoneFieldRuneCap runes, i.e.
// at most 200 * 5 * 4000 = 4,000,000 runes, or ~16 MB assuming every rune
// costs the UTF-8 max of 4 bytes (ASCII text, the common case, costs a
// quarter of that). ProgressHistory adds up to a further
// tombstoneCap * progressHistoryCap * tombstoneFieldRuneCap runes on top of
// that (200 * 20 * 4000 = 16,000,000 runes, ~64 MB worst case) — see
// truncateTombstoneProgressHistory, which bounds the whole slice
// independently of SetProgress's own live caps rather than trusting they
// stay in force. In practice SetProgress already caps each entry to the
// smaller progressEntryRuneCap (2000), so the realistic number today is
// about half that worst case — but the tombstone's OWN bound is what makes
// this section's arithmetic true regardless of what SetProgress does later.
// Truncating Result specifically means "the full
// result", the premise the tombstone exists to satisfy, is honored only up
// to tombstoneFieldRuneCap runes — far beyond the 800-rune completion-notice
// preview it replaces, but not literally unbounded, which an in-memory
// process-lifetime cache cannot honestly promise anyway. Result's
// truncation is marked with an explicit "[note: ...]" (see
// truncateTombstoneResult) rather than a bare ellipsis, because Result is
// the one field wait()/resume() hand back as the child's actual answer — a
// silent trailing "…" there reads as the child's own punctuation, not as
// "the harness cut this", and a parent polling late could report a cut
// intermediate paragraph as the final finding with no way to know a
// conclusion existed. The other four fields are secondary metadata a
// bare ellipsis is a fine enough signal for.
const tombstoneCap = 200

// tombstoneFieldRuneCap bounds each of a tombstoned Job's string fields
// (Result, Err, Progress, Description, ExtensionReason, and every entry of
// ProgressHistory) in runes — see tombstoneCap's doc comment for the
// reasoning and the resulting worst-case memory bound.
const tombstoneFieldRuneCap = 4000

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
		if job.Kind == KindSubagent {
			r.tombstoneLocked(job.Snapshot())
		}
		delete(r.jobs, job.ID)
	}
}

// tombstoneLocked records snap under its own id, evicting the oldest
// tombstone (FIFO) once tombstoneCap is exceeded. Truncates snap's string
// fields first (see tombstoneCap's doc comment) so what is actually
// retained matches what the doc comment promises. Caller must hold r.mu.
func (r *Registry) tombstoneLocked(snap Job) {
	snap.Result = truncateTombstoneResult(snap.Result)
	snap.Err = truncateTombstoneField(snap.Err)
	snap.Progress = truncateTombstoneField(snap.Progress)
	snap.Description = truncateTombstoneField(snap.Description)
	snap.ExtensionReason = truncateTombstoneField(snap.ExtensionReason)
	snap.ProgressHistory = truncateTombstoneProgressHistory(snap.ProgressHistory)

	if _, exists := r.tombstones[snap.ID]; !exists {
		r.tombstoneOrder = append(r.tombstoneOrder, snap.ID)
	}
	r.tombstones[snap.ID] = snap
	for len(r.tombstoneOrder) > tombstoneCap {
		oldest := r.tombstoneOrder[0]
		r.tombstoneOrder = r.tombstoneOrder[1:]
		delete(r.tombstones, oldest)
	}
}

// truncateTombstoneField truncates s to at most tombstoneFieldRuneCap
// runes, appending an ellipsis when it actually cut something. Rune-based,
// never byte-based — this repo has had byte-slicing truncation bugs before
// (backspace/search, the TUI, other tools), and slicing a string by byte
// offset mid-rune corrupts UTF-8 multi-byte sequences.
func truncateTombstoneField(s string) string {
	runes := []rune(s)
	if len(runes) <= tombstoneFieldRuneCap {
		return s
	}
	return string(runes[:tombstoneFieldRuneCap]) + "…"
}

// truncateTombstoneResult is truncateTombstoneField's Result-specific
// sibling — see tombstoneCap's doc comment for why Result alone gets an
// explicit, unmistakable "[note: ...]" marker (the same idiom
// tools/subagent.go's subagentCutoffMessage and subagentCompletionNotice
// already use for a truncated/cut-off child result) instead of a bare
// trailing ellipsis.
func truncateTombstoneResult(s string) string {
	runes := []rune(s)
	if len(runes) <= tombstoneFieldRuneCap {
		return s
	}
	return string(runes[:tombstoneFieldRuneCap]) + fmt.Sprintf("\n\n[note: result truncated to %d runes for long-term retention]", tombstoneFieldRuneCap)
}

// truncateTombstoneProgressHistory bounds a tombstoned job's WHOLE progress
// history, not just each entry: SetProgress already caps a live job's
// history to progressHistoryCap entries of at most progressEntryRuneCap
// runes each, but a tombstone must not depend on that invariant holding
// forever elsewhere in this file — it enforces its own bound independently,
// the same way tombstoneLocked's other fields do, so a future change to how
// history is written can never silently blow up tombstone memory. Re-slices
// to the newest progressHistoryCap entries (oldest first is already the
// slice's order) and re-truncates each one to tombstoneFieldRuneCap runes
// with truncateTombstoneField, same idiom as every other secondary field
// tombstoneLocked truncates with a bare ellipsis.
func truncateTombstoneProgressHistory(history []string) []string {
	if len(history) > progressHistoryCap {
		history = history[len(history)-progressHistoryCap:]
	}
	out := make([]string, len(history))
	for i, entry := range history {
		out[i] = truncateTombstoneField(entry)
	}
	return out
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
