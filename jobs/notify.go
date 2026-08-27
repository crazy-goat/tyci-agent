package jobs

import "sync"

// maxPendingNotices bounds the queue. A notice is a single short line, so 64
// is far above any realistic backlog; the cap only exists so a runaway
// producer (a script spawning background commands in a loop) can't grow the
// slice without bound while nobody drains it. Oldest notices are dropped
// first — the newest are the ones the model still needs to act on.
const maxPendingNotices = 64

// maxShownKeys bounds the "already shown elsewhere" record MarkQuestionShown
// writes into when it races ahead of the notice it is meant to suppress (see
// its doc comment). Same rationale and same size as maxPendingNotices: a
// question notice is a rare event compared to background-bash traffic, so
// this is generous headroom, not a tight budget. It also bounds the damage
// from a mark that is NEVER consumed — see MarkQuestionShown's doc comment
// on the two production paths (a mailbox-routed question, or one a REPORTING
// Wait call is about to deliver) that leave exactly that kind of orphaned
// key behind.
const maxShownKeys = 64

// notice is one queued entry. jobID and seq, when both set (seq != 0),
// identify a "child jobID is blocked on question" notice specifically — the
// only kind that can ALSO be delivered another way: a blocking subagent
// call's handoff message (spawnedJobsMessage's "question" field) carries the
// very same question when it happens to still be pending at the moment the
// handoff message is built (see tools/subagent.go's handOff/pendingQuestions).
//
// Every other notice (a background command finishing, a residual-mailbox
// sweep, ...) carries jobID == "" and is never subject to suppression.
type notice struct {
	jobID string
	seq   int
	text  string
}

// shownKey identifies one "jobID's question was already delivered via a
// handoff message" fact recorded by MarkQuestionShown ahead of the matching
// NotifyQuestion call — see MarkQuestionShown's doc comment for why that
// ordering is possible and has to be handled, not assumed away.
type shownKey struct {
	jobID string
	seq   int
}

// Notifier is a queue of short, model-facing notices produced by background
// work that finished while the agent was busy elsewhere or idle.
//
// It has two consumers, and both are needed:
//
//   - Drain is wired into agent.Config.NextMessages, so a notice queued
//     while a turn is in flight reaches the model at the next safe point
//     (see agent.Run's drain site) without waiting for the user.
//   - Signal wakes an idle REPL loop (see runTUI) so a notice queued while
//     nobody is running can start a fresh turn on its own, instead of
//     sitting in the queue until the user happens to type something.
//
// Notifier deliberately knows nothing about jobs.Registry beyond the id/seq
// pair NotifyQuestion and MarkQuestionShown key on: the producer formats its
// own one-line notice and hands over a string, and job.go's own notion of a
// job shape never leaks in here. That keeps it usable by anything
// backgrounded in future without teaching it new job shapes. seq (mirroring
// jobs.Job.QuestionSeq — see its doc comment) is the one piece of job
// identity that does leak in, as a bare int with no meaning of its own here:
// it exists purely so two different asks are never confused with each other.
type Notifier struct {
	mu      sync.Mutex
	pending []notice
	shown   []shownKey
	signal  chan struct{}
}

func NewNotifier() *Notifier {
	return &Notifier{signal: make(chan struct{}, 1)}
}

// Notify queues text and wakes anyone selecting on Signal. Never blocks:
// the signal channel has capacity 1 and is a level-triggered "something is
// pending" edge, not a per-notice stream — a consumer that wakes once and
// drains everything is the intended pattern.
//
// The signature matches the tools package's JobNotifier contract, so
// *Notifier satisfies it structurally with no adapter (same layering rule as
// the JobWaiter/JobStarter adapters in btw.go).
func (n *Notifier) Notify(text string) {
	if text == "" {
		return
	}
	n.mu.Lock()
	n.pending = append(n.pending, notice{text: text})
	if len(n.pending) > maxPendingNotices {
		n.pending = n.pending[len(n.pending)-maxPendingNotices:]
	}
	n.mu.Unlock()
	n.wake()
}

// NotifyQuestion queues a "child jobID is blocked waiting for an answer to
// question" notice — unless MarkQuestionShown has already recorded that this
// exact jobID/seq was just delivered another way (a handoff message), in
// which case this call is dropped instead of queuing a duplicate. text is
// the full formatted line to surface (built by the caller, same as Notify).
// seq is the ask's jobs.Job.QuestionSeq — an unforgeable per-ask id, NOT the
// question text: see MarkQuestionShown's doc comment for why keying on text
// would be wrong (item 54 review finding 1).
//
// Use this (not plain Notify) for any notice whose content a handoff message
// could also independently carry — see MarkQuestionShown's doc comment for
// why the two delivery paths need a shared key to de-duplicate against, in
// either call order.
func (n *Notifier) NotifyQuestion(jobID string, seq int, text string) {
	if text == "" {
		return
	}
	n.mu.Lock()
	if n.consumeShownLocked(jobID, seq) {
		// Already delivered via a handoff message before this notice even
		// reached the queue — see MarkQuestionShown's doc comment for the
		// race this closes. Drop it rather than queue a duplicate.
		n.mu.Unlock()
		return
	}
	n.pending = append(n.pending, notice{jobID: jobID, seq: seq, text: text})
	if len(n.pending) > maxPendingNotices {
		n.pending = n.pending[len(n.pending)-maxPendingNotices:]
	}
	n.mu.Unlock()
	n.wake()
}

func (n *Notifier) wake() {
	select {
	case n.signal <- struct{}{}:
	default:
	}
}

// consumeShownLocked reports whether jobID/seq is in n.shown, removing it if
// so (each mark is good for exactly one suppression). Caller holds n.mu.
func (n *Notifier) consumeShownLocked(jobID string, seq int) bool {
	for i, k := range n.shown {
		if k.jobID == jobID && k.seq == seq {
			n.shown = append(n.shown[:i], n.shown[i+1:]...)
			return true
		}
	}
	return false
}

// MarkQuestionShown records that jobID's question — identified by seq, its
// jobs.Job.QuestionSeq at the moment it was asked — was already surfaced
// some other way, in practice a blocking subagent call's handoff message
// carrying it in the "question" field of the id it just handed to the
// background (see tools/subagent.go's handOff). The effect is that a
// matching "child is blocked on a question" notice — whether it is already
// queued (NotifyQuestion ran first) or arrives later (NotifyQuestion has not
// run yet) — never surfaces from Drain, instead of repeating what the model
// was just told.
//
// Keyed on jobID+seq, NOT jobID+question text (item 54 review finding 1): a
// child can pose the exact same words twice across its lifetime — retried
// after a timeout, asked again once the first answer turned out to be
// wrong — and each ask gets its own seq (see Ask in registry.go). Keying on
// text would let the FIRST ask's mark (left behind unconsumed — see below)
// wrongly swallow the SECOND ask's genuine notice, silently losing a
// question nobody ever saw. seq makes that collision structurally
// impossible: two different asks never share a key, no matter what they say.
//
// Both call orders genuinely happen and this has to handle each: Ask fires
// its onEvent hook (which calls NotifyQuestion — see main.go's wireTools)
// synchronously and before it blocks, so the notice is USUALLY queued well
// before the handoff logic (pendingQuestions -> handOff -> here) ever runs —
// but the two run on different goroutines with nothing that forces that
// order. When onEvent already ran, this removes the matching pending entry
// directly. When it has not, this records the key (bounded by maxShownKeys,
// oldest evicted first — the same "producer might race ahead of a drain"
// concern maxPendingNotices exists for) so the NotifyQuestion call still to
// come finds it and drops the notice instead of queuing it.
//
// A mark this leaves behind is NOT always consumed — that is expected, not a
// bug, precisely because seq makes an unconsumed mark harmless rather than a
// leak that can misfire later: main.go's onEvent hook never calls
// NotifyQuestion at all for a job with a live ParentID (it routes into that
// parent job's own mailbox instead via reg.Post — see the TODO next to that
// call in main.go for item 54 review finding 2, which this dedup does not
// yet cover) or for a job whose question a REPORTING
// Wait call is about to deliver synchronously (QuestionHasWaiter — see
// Job's doc comment). handOff calls this whenever the handoff message
// carries the question, regardless of which of those applies, since it has
// no way to know; the mark for either case just ages out under
// maxShownKeys, never matching anything.
//
// See jobs/notify_test.go and tools/subagent_handoff_test.go for the ways a
// handoff CAN fail to carry the question in the first place, in which case
// this is never called and Drain is the only delivery it ever gets: early
// return when nothing is left running (handOff's len(stillRunning) == 0
// branch), the handoff==false path via cancelRemaining, or no JobObserver
// wired at all (pendingQuestions returns nil with nothing to query — see
// TestWiring_54b's setup).
//
// Deliberately NOT called on the ctx.Done() (Esc) handoff path in
// runWithHandoff: keeping the notice there costs at worst a duplicate (the
// question was already in the handoff message, which this codebase's tool
// pipeline does still append to the conversation even after ctx is
// cancelled), never a silent loss — and a duplicate is the safer default
// than risking suppression of the only delivery in some future code path
// that changes what happens to a cancelled turn's tool result.
func (n *Notifier) MarkQuestionShown(jobID string, seq int) {
	if jobID == "" || seq == 0 {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for i := range n.pending {
		if n.pending[i].jobID == jobID && n.pending[i].seq == seq {
			n.pending = append(n.pending[:i], n.pending[i+1:]...)
			return
		}
	}
	// Not queued yet — remember the key for the NotifyQuestion call still to
	// come. If NotifyQuestion never actually comes for this seq (see the two
	// production paths in the doc comment above), this simply ages out under
	// maxShownKeys — it can never falsely match a different ask, because seq
	// is unique per ask.
	n.shown = append(n.shown, shownKey{jobID: jobID, seq: seq})
	if len(n.shown) > maxShownKeys {
		n.shown = n.shown[len(n.shown)-maxShownKeys:]
	}
}

// Drain returns every queued notice's text in FIFO order and empties the
// queue. Returns nil (not an empty slice) when there is nothing pending, so
// callers can test with len() or nil-ness interchangeably.
func (n *Notifier) Drain() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.pending) == 0 {
		return nil
	}
	out := make([]string, len(n.pending))
	for i, nt := range n.pending {
		out[i] = nt.text
	}
	n.pending = nil
	// Drop a stale wakeup edge: whoever drained here has already seen
	// everything the edge was announcing, so leaving it armed would wake an
	// idle loop for an empty queue.
	select {
	case <-n.signal:
	default:
	}
	return out
}

// Clear drops all queued notices, any already-armed wakeup, and any
// not-yet-consumed MarkQuestionShown keys. /new uses this at the
// conversation boundary so completions from cancelled old work cannot
// become prompts in the fresh conversation — and, without the shown reset,
// a leftover key could otherwise silently swallow a genuinely new question
// notice queued after the boundary (item 54 review finding 3).
func (n *Notifier) Clear() {
	n.mu.Lock()
	n.pending = nil
	n.shown = nil
	n.mu.Unlock()
	select {
	case <-n.signal:
	default:
	}
}

// Signal fires when a notice is queued. Receiving from it is only a hint
// that the queue *was* non-empty: always follow up with Drain and tolerate
// an empty result, since another consumer (NextMessages during an in-flight
// turn) may have taken the notices first.
func (n *Notifier) Signal() <-chan struct{} { return n.signal }
