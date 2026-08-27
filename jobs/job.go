package jobs

import (
	"context"
	"strings"
	"sync/atomic"
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

// Kind distinguishes what kind of work a job represents. The registry is
// otherwise flat and holds every background job — a subagent, a backgrounded
// shell command, a /btw side-conversation, a scheduled cron run — so this is
// the one field the Subagents and Bash sidebar tabs (TODO item 1) filter on.
// KindOther (the zero value) covers a job created before this field existed
// in any test fixture, and anything not worth a dedicated tab.
type Kind string

const (
	KindOther    Kind = ""
	KindSubagent Kind = "subagent"
	KindBash     Kind = "bash"
	KindCron     Kind = "cron"
)

// ErrStoppedByUser is what a job's recorded error reads when jobs.Registry's
// Cancel stopped it, instead of the bare "context canceled" a cancelled
// context would otherwise produce. A sentinel rather than a formatted string
// so callers (tests, display) can match it without pinning exact wording.
var ErrStoppedByUser = contextStoppedByUser("stopped by user (kill_job)")

type contextStoppedByUser string

func (e contextStoppedByUser) Error() string { return string(e) }

type Job struct {
	ID          string
	Description string

	// Kind says what spawned this job — see Kind's doc comment.
	Kind Kind
	// ParentID is the ID of the job that spawned this one, empty when the
	// spawning context was the top-level conversation (not itself a job).
	// Set once at Start from the spawn context's own job id (see
	// tools.JobIDCtxKey) and never mutated afterward — it is what lets the
	// Subagents tab reconstruct a tree via a parent-link walk instead of
	// the registry ever holding a live child list, and what lets Cancel
	// stop a whole subtree (a subagent plus every background command it
	// started) in one call.
	ParentID string

	Status     Status
	Result     string
	Err        string
	StartedAt  time.Time
	FinishedAt time.Time
	done       chan struct{}

	// Question holds the pending question text while Status ==
	// StatusWaitingAnswer (set by Ask, cleared once answered or unblocked).
	Question string

	// QuestionSeq increments every time Ask poses a new question on this
	// job (set under Registry.mu, right next to Question — see Ask). It
	// exists so a question can be keyed unforgeably: Question is free text,
	// and a child that asks the exact same words twice across its lifetime
	// (retrying after a timeout, asking again after being answered once)
	// would otherwise produce two notices with an identical jobID+question
	// key. jobs.Notifier's NotifyQuestion/MarkQuestionShown key on
	// jobID+QuestionSeq instead, specifically so the first ask's "already
	// shown" mark can never be mistaken for covering the second ask (item
	// 54 review finding 1). Never reset — always increasing for the life of
	// the job.
	QuestionSeq int

	// QuestionHasWaiter is true when, at the moment Ask set Status to
	// StatusWaitingAnswer, at least one caller was already blocked inside
	// Registry.Wait for this specific job (see waiters below). It exists so
	// a single question is delivered exactly once: whoever wires the
	// onEvent hook into a notice queue (see main.go's wireTools) can skip
	// queuing that notice when this is true, because the waiting Wait call
	// is about to report the very same question back to its own caller
	// synchronously — the two paths would otherwise both deliver it. Reset
	// to false when the job leaves StatusWaitingAnswer.
	QuestionHasWaiter bool

	// Progress holds the last status note reported via SetProgress
	// (report_progress tool). Unlike Question, it is NOT cleared when the
	// job finishes — it persists as the last thing the job said about its
	// own progress before completing. Kept as its own field (rather than
	// derived from ProgressHistory below) because it predates the history
	// and several existing call sites read Job.Progress directly for "the
	// latest note" without wanting to index into a slice for it.
	Progress string

	// ProgressHistory holds every status note SetProgress has recorded for
	// this job, oldest first, capped at progressHistoryCap entries with the
	// oldest evicted once full — see SetProgress and
	// ProgressHistoryTruncated. Before this field existed, a child that
	// reported three times left only the third note behind: SetProgress
	// overwrote Progress in place, so the parent lost the sequence, which
	// is the entire point of progress reporting for a long-running child.
	ProgressHistory []string

	// ProgressHistoryTruncated is true once at least one entry has been
	// evicted from ProgressHistory to keep it within progressHistoryCap. A
	// bounded history that silently drops its oldest entries is
	// indistinguishable from a complete one unless something says so — this
	// is that something.
	ProgressHistoryTruncated bool

	// lastProgressAt is when this job last reported real progress via
	// SetProgress (report_progress tool), seeded to StartedAt at Start so a
	// job that has said nothing yet reads as "quiet since it started" rather
	// than a zero time.Time producing a nonsense duration — the same seeding
	// idiom lastActivity below uses, and for the same reason. Used by
	// Registry.NeedsProgressHeartbeat (item 15) to decide when a running
	// subagent has gone quiet long enough to deserve a harness nudge asking
	// it to call report_progress.
	//
	// Unexported and guarded by Registry.mu, same as lastActivity/cancelled/
	// lastHeartbeatNudgeAt below: review of item 15 caught this exported
	// with no Snapshot() copy, which would have handed back the zero time to
	// any caller reading Job.LastProgressAt off a snapshot, and an outright
	// data race to one reading it directly off a live *Job.
	lastProgressAt time.Time

	// LastActivity is materialized by Snapshot from lastActivity below — it
	// only ever holds a meaningful value on a Snapshot()-returned copy, not
	// on the live *Job (which tracks the same information in lastActivity
	// instead). Display code reads this field, never lastActivity directly.
	LastActivity time.Time

	// lastActivity holds unix nanoseconds of the last sign of life seen from
	// this job: a streamed Text/Thinking/ToolCallStart/ToolCallDelta/
	// ToolCallEnd call, or a backgrounded bash output line — see
	// jobs.Registry.TouchActivity and its callers in tools/subagent.go and
	// tools/bash.go. Deliberately NOT derived from Progress: report_progress
	// is voluntary and rare, so a job that never calls it would otherwise
	// read as permanently idle even while actively streaming output.
	//
	// This is a plain int64 rather than atomic.Int64 on purpose: Snapshot
	// copies the whole Job by value, and go vet's copylocks check rejects
	// copying a struct containing an atomic.Int64 (it carries a noCopy-style
	// guard). A plain int64 read/written via atomic.LoadInt64/StoreInt64 on
	// its address has no such restriction, and needs no registry-wide lock
	// to update — see touchActivity.
	//
	// Set once at Start (so a job that hasn't done anything yet reads as
	// "idle since start", not a zero time producing a nonsense duration) and
	// never cleared on completion — like Progress, it persists past terminal
	// state so a completed job's last known activity is still visible.
	lastActivity int64

	// answerCh is lazily created by Ask and delivered to by Answer. It stays
	// internal to the registry: unexported and channel-typed, so Snapshot
	// must never copy it.
	answerCh chan jobAnswer

	// statusChanged is closed and replaced every time Ask flips Status
	// between StatusRunning and StatusWaitingAnswer (see
	// Registry.signalStatusChangeLocked). Wait selects on whatever value it
	// held at the moment Wait was called, so a transition that happens
	// either while Wait is blocked OR in the narrow window just before —
	// closing a channel a select is about to read from still wakes it —
	// is never missed. It is not used for the terminal transition; job.done
	// already covers that. Unexported and channel-typed, so Snapshot never
	// copies it; guarded by Registry.mu like everything else here.
	statusChanged chan struct{}

	// waiters counts callers currently blocked inside Registry.Wait (NOT
	// WaitObserve — see its doc comment) for this job. Ask reads it (while
	// already holding Registry.mu) to decide QuestionHasWaiter above. Only
	// a Wait call counts: it is the shape that hands its result back to
	// whoever called it, which is what makes it a genuine second delivery
	// path for the question. WaitObserve exists precisely for a caller
	// that blocks on the same signal for its own purposes (waking an
	// unrelated select) without itself reporting the question to anyone —
	// counting that too was batch-2 review finding C1: it made Ask
	// suppress the only delivery a question had. Guarded by Registry.mu.
	waiters int

	// observers counts callers currently blocked inside Registry.WaitObserve
	// for this job — the mirror of waiters above, kept as a separate
	// counter rather than folded into it so the two can never be confused
	// (that confusion is exactly what C1 was). Ask does not read this: an
	// observer never suppresses a notice. It exists purely so a test can
	// synchronize on "a WaitObserve call has actually registered" instead
	// of a settle sleep (see Registry.ObserverCount). Guarded by
	// Registry.mu.
	observers int

	// cancel ends this job's own context (see Registry.Start, which derives
	// it). Unexported and func-typed so Snapshot keeps copying the struct by
	// value; guarded by Registry.mu like everything else written after
	// construction. Set once at Start, never replaced.
	cancel context.CancelFunc

	// cancelled records that Cancel — not the job's own timeout backstop,
	// not its caller's context — stopped this job, so Start's completion
	// path can rewrite a bare context.Canceled into ErrStoppedByUser ("who
	// stopped this?" is the question the panel has to answer). Plain bool
	// under Registry.mu for the same copylocks reason as lastActivity
	// above; set only before the matching cancel fires.
	cancelled bool

	// Extension fields describe the one pending or accepted timeout extension.
	// They are ordinary snapshot data; the decision channel and resettable
	// context below remain private so Snapshot never copies synchronization
	// primitives.
	ExtensionRequestID string
	ExtensionSeconds   time.Duration
	ExtensionReason    string
	ExtensionPending   bool
	ExtensionAccepted  bool

	// mailbox queues messages posted via Registry.Post (the "message" tool,
	// or the "/msg" slash command), awaiting delivery to this job's own
	// agent loop at its next iteration boundary — see Registry.DrainMessages
	// and tools.JobMailboxNextMessages, which wires it into a background
	// subagent's agent.Config.NextMessages the same way the main agent's
	// NextMessages queue works today. Guarded by Registry.mu, like Progress:
	// unlike lastActivity there is no hot-path pressure here (a message is a
	// rare, deliberate act, not something fired on every streamed token), so
	// a plain slice under the registry lock is simplest.
	mailbox []string

	// lastHeartbeatNudgeAt is when Registry.NeedsProgressHeartbeat last
	// returned true for this job — i.e. when the harness last injected a
	// "post a report_progress note" reminder into this job's own loop.
	// Deliberately separate from lastProgressAt: a nudge is not a real
	// progress note (the child may ignore it), but it still has to count as
	// "quiet time reset" for nagging purposes, or a child that never calls
	// report_progress would get nagged on every single iteration once the
	// threshold is first crossed — exactly the trap item 15 calls out.
	// Unexported and guarded by Registry.mu, same as lastActivity/cancelled
	// above; Snapshot never needs to expose it.
	lastHeartbeatNudgeAt time.Time

	// ResidualMailbox is set ONCE, at the moment this job goes terminal (see
	// Registry.Start's completion path), to whatever was still sitting in
	// mailbox and never got drained by this job's own (now-stopped) agent
	// loop. Batch-2 review finding C3: Registry.Post reports success for
	// any live job, but "live" only means the loop MIGHT still drain it at
	// its next iteration boundary — a job whose final iteration has
	// already happened (agent.Run does not drain after its last turn) will
	// never read another posted message again, and the mailbox is gone the
	// moment this job is pruned. Before this field existed, that content —
	// notices routed here by notifyToParent among them — simply vanished
	// with no trace once the job finished. Whoever consumes onEvent's
	// terminal snapshot is expected to forward this to somewhere still
	// reachable (main.go's onEvent hook forwards it to the main notice
	// queue, tagged) instead of letting it disappear. Copied by Snapshot
	// like any other exported field; nil on every NON-terminal snapshot.
	ResidualMailbox []string

	extensionCtx      *resettableDeadlineContext
	extensionDecision chan bool
	extensionResolved bool
	extensionApproved bool
}

// touchActivity atomically records "now" as this job's last sign of life.
// Safe for concurrent use from multiple goroutines (parallel tool calls
// within the same job), and cheap enough to call on every streamed token:
// no registry lock, no snapshot, no event. The CAS loop guards against a
// stale call (racing with a newer one) ever moving the timestamp backward.
func (j *Job) touchActivity() {
	now := time.Now().UnixNano()
	for {
		old := atomic.LoadInt64(&j.lastActivity)
		if now <= old {
			return
		}
		if atomic.CompareAndSwapInt64(&j.lastActivity, old, now) {
			return
		}
	}
}

// jobAnswer is what Answer sends and Ask receives over answerCh. fromUser
// distinguishes a genuine human reply from an agent's own reply (delivered
// via the "answer_job" tool, which only the model can invoke, and which
// always passes fromUser=false — there is no dedicated command a human
// types directly any more) — see AskTool's doc comment in tools/ask.go for
// why the distinction has to survive the round trip.
type jobAnswer struct {
	text     string
	fromUser bool
}

// ShortID trims the "job-<unixnano>-<n>" ID (see nextID) down to its
// trailing counter — the stable, human-scannable form the TUI jobs panel
// displays.
func ShortID(id string) string {
	if idx := strings.LastIndexByte(id, '-'); idx >= 0 && idx+1 < len(id) {
		return id[idx+1:]
	}
	return id
}

func (j *Job) Snapshot() Job {
	return Job{
		ID:                j.ID,
		Description:       j.Description,
		Kind:              j.Kind,
		ParentID:          j.ParentID,
		Status:            j.Status,
		Result:            j.Result,
		Err:               j.Err,
		StartedAt:         j.StartedAt,
		FinishedAt:        j.FinishedAt,
		Question:          j.Question,
		QuestionSeq:       j.QuestionSeq,
		QuestionHasWaiter: j.QuestionHasWaiter,
		Progress:          j.Progress,
		// Deep-copied for the exact reason ResidualMailbox is below: a
		// plain slice-header copy would leave every snapshot aliasing the
		// SAME backing array the live Job keeps appending/evicting from
		// in SetProgress, which is a data race the moment a caller reads a
		// snapshot's ProgressHistory while another goroutine calls
		// SetProgress on the live job.
		ProgressHistory:          append([]string(nil), j.ProgressHistory...),
		ProgressHistoryTruncated: j.ProgressHistoryTruncated,
		// append([]string(nil), ...) copies the backing array — a plain
		// slice-header copy would leave every snapshot (including every
		// element List() returns) aliasing the SAME array the live Job
		// still points at, breaking the copy-by-value contract every
		// unexported/func-typed field's doc comment in this file justifies
		// leaving out of Snapshot (batch-2 review round 2 finding D4). Read-only
		// everywhere today, so no live bug yet — but nothing enforces that.
		ResidualMailbox:    append([]string(nil), j.ResidualMailbox...),
		ExtensionRequestID: j.ExtensionRequestID,
		ExtensionSeconds:   j.ExtensionSeconds,
		ExtensionReason:    j.ExtensionReason,
		ExtensionPending:   j.ExtensionPending,
		ExtensionAccepted:  j.ExtensionAccepted,
		LastActivity:       time.Unix(0, atomic.LoadInt64(&j.lastActivity)),
	}
}
