package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Background-bash tuning. These are the three numbers the whole feature
// turns on, kept together so they are easy to find and reason about.
const (
	// BashDefaultTimeoutSec is how long a foreground bash call may run when
	// backgrounding is unavailable (see BackgroundBashEnabled) or disabled
	// for the call. Previously enforced by the dispatcher in
	// agent/tools_exec.go; the tool now owns it, because the dispatcher's
	// context is cancelled the moment the tool returns and that would kill
	// any command we just handed to the background.
	BashDefaultTimeoutSec = 120

	// BashBackgroundAfterSec is how long we wait before deciding a command
	// is "taking longer than expected" and moving it to the background. Set
	// above the runtime of an ordinary build or test run: below that, the
	// handoff would cost the model an extra polling turn for commands that
	// were about to finish anyway.
	BashBackgroundAfterSec = 30

	// BashBackgroundLimitSec is the wall-clock backstop for a command that
	// has been moved to the background. Nothing else bounds it once it is
	// detached from the tool call's context, so without this a wedged
	// process would live until tyci exits.
	BashBackgroundLimitSec = 3600

	// BashFirstProgressNoticeSec is when the first "still running" heads-up
	// goes out, measured from the command's start — so 30s after the handoff
	// at BashBackgroundAfterSec.
	//
	// It exists because the handoff itself is easy to forget: the model is
	// told the command moved to the background and then gets on with
	// something else, and a typo that turns a five-second command into a hang
	// looks exactly like a legitimately slow build. One line at a minute is
	// enough for whoever wrote the command to notice, and the notice is
	// deliberately informational — it does not ask for the current work to be
	// dropped, and it does not tell the model to re-examine its command.
	BashFirstProgressNoticeSec = 60

	// BashProgressNoticeEverySec is the repeat interval after the first
	// notice. Five minutes: often enough that a wedged process is noticed,
	// rare enough that a legitimate half-hour build costs six lines of
	// context rather than thirty.
	BashProgressNoticeEverySec = 300

	// maxBackgroundBash bounds how many backgrounded commands may run at
	// once. Each one keeps writing to the filesystem while the agent does
	// other work, so an unbounded count means an unbounded number of
	// concurrent builds fighting over the same caches and lock files.
	maxBackgroundBash = 4
)

// backgroundBashEnabled gates the whole feature. It is off by default on
// purpose: a backgrounded command is only useful where something will
// actually consume its completion notice and can start a follow-up turn —
// i.e. an interactive session. In a one-shot `tyci run --prompt ...` there is
// no next turn, the process would be killed at exit, and the model would be
// left polling a job it can never see finish. main() turns it on for the
// interactive modes only (see commands.go).
var backgroundBashEnabled atomic.Bool

// SetBackgroundBashEnabled turns automatic and explicit backgrounding of
// bash commands on or off for this process. Off (the default) makes the bash
// tool behave exactly as it did before the feature existed.
func SetBackgroundBashEnabled(v bool) { backgroundBashEnabled.Store(v) }

// BackgroundBashEnabled reports whether background bash is available. The
// bash tool also requires a wired JobStarter (SetJobStarter) — without a job
// registry there would be nowhere to record the result.
func BackgroundBashEnabled() bool { return backgroundBashEnabled.Load() && getJobStarter() != nil }

// backgroundAllowed reports whether the call site behind ctx may move a
// command to the background. Two conditions, both necessary:
//
//   - the mode opted in (BackgroundBashEnabled), i.e. something will consume
//     the completion notice and can act on it;
//   - we are not inside a child agent. A subagent's run ends when it returns
//     its answer, so a command it backgrounded would have nobody left to
//     collect the result, and the completion notice would surface in the
//     PARENT's conversation, which never issued the command. A child that
//     needs a long command should block on it — it has its own wall-clock
//     budget for exactly that.
func backgroundAllowed(ctx context.Context) bool {
	if !BackgroundBashEnabled() {
		return false
	}
	return ctx.Value(SubagentSinkCtxKey{}) == nil
}

// JobNotifier receives one short, model-facing line when a background
// command finishes. Deliberately a plain string rather than a job struct:
// this package must not import "jobs" (same import-cycle rule as JobWaiter
// and JobStarter), and the producer is better placed to phrase the notice
// than the consumer is.
//
// MarkQuestionShown lets this package (specifically handOff in subagent.go)
// tell the same notifier that a "child is blocked on a question" notice it
// may already have queued (via jobs.Registry.Ask's onEvent hook — see
// main.go's wireTools) was just also delivered through a handoff message, so
// a later drain of the queue does not repeat it. Keyed on jobID+seq — seq is
// jobs.Job.QuestionSeq, an unforgeable per-ask id, NOT the question text:
// keying on text would let one ask's "shown" mark wrongly suppress a later,
// identically-worded ask from the same job (item 54 review finding 1). See
// jobs.Notifier's MarkQuestionShown doc comment for the full reasoning,
// including the one path (Esc/ctx.Done) that deliberately never calls this.
//
// jobs.Notifier satisfies this structurally; main() wires it in wireTools.
type JobNotifier interface {
	Notify(text string)
	MarkQuestionShown(jobID string, seq int)
}

// jobNotifier is nil until SetJobNotifier is called. Unlike the other job
// hooks in this package, a nil notifier is NOT an error: the completion
// notice is a convenience on top of the job registry, and "wait" still
// returns the result either way. So notify() simply does nothing.
// Guarded by a mutex because it is read from job goroutines that outlive the
// tool call that started them, while SetJobNotifier is called from the setup
// path. In production the write happens once before any job exists, so the
// lock costs nothing; the reason it is here is that "written once at startup"
// is a convention nothing enforces, and a detached goroutine reading a plain
// package var is a race whether or not it is currently observable.
var (
	jobNotifierMu sync.RWMutex
	jobNotifier   JobNotifier
)

// SetJobNotifier wires background-command completion notices to a
// JobNotifier (in practice the app's shared jobs.Notifier, whose queue is
// drained into the agent loop and also wakes an idle REPL).
func SetJobNotifier(n JobNotifier) {
	jobNotifierMu.Lock()
	jobNotifier = n
	jobNotifierMu.Unlock()
}

// notify sends text to the main queue. Equivalent to notifyToParent("",
// text) — use that instead wherever the notice belongs to a job with a
// known spawner (see notifyToParent's doc comment); this remains for the
// genuinely top-level cases (a scheduled cron tick nobody asked for, a
// command backgrounded straight from the main conversation).
func notify(text string) {
	notifyToParent("", text)
}

// notifyToParent routes text to the queue belonging to parentID — the job
// that spawned whatever produced this notice (see jobs.Job.ParentID) —
// instead of unconditionally to the main, process-wide queue.
//
// This exists because a notice with no addressee always used to land on the
// main queue, even when it was produced by a job spawned from an
// independent fork (a /btw side-conversation, or a subagent nested inside
// one) that must never touch the main conversation — see btwConfig's doc
// comment in btw.go for why that separation matters. Routing through the
// spawning job's own mailbox (the same delivery path "message"/"/msg"
// already uses — see JobMailbox and JobMailboxNextMessages) means the
// notice reaches that job's own agent loop at its next iteration boundary
// instead.
//
// Design choice for when the intended recipient is gone (parentID names a
// job that has already finished — its fork ended before this notice was
// ready): forward the notice to the main queue rather than dropping it.
// Silently discarding a notice would hide it forever, with nothing in the
// transcript to explain what happened to it; forwarding tags it so whoever
// reads it on the main queue knows it was not meant for them originally.
// parentID == "" (spawned directly from the top-level conversation, not
// from within another job) goes straight to main with no tag, since main
// IS the intended recipient in that case.
//
// The two ways parentID != "" can still end up on the main queue are
// tagged differently (batch-2 review finding C4): "recipient gone" means
// mb.Post itself said so (the job is known and terminal), while "no
// mailbox wired at all" (mb == nil — a test, or a mode that never calls
// SetJobMailbox; production always wires one) says nothing about whether
// parentID is even still running. Claiming it had "already finished" in
// that second case would be a flat guess dressed up as a fact.
func notifyToParent(parentID, text string) {
	if text == "" {
		return
	}
	if parentID != "" {
		switch mb := getJobMailbox(); {
		case mb == nil:
			text = fmt.Sprintf("[for job %s, but no mailbox is wired to route it there — delivered here instead] %s", parentID, text)
		case mb.Post(parentID, text):
			return
		default:
			text = fmt.Sprintf("[for job %s, which has already finished — forwarded here instead] %s", parentID, text)
		}
	}
	jobNotifierMu.RLock()
	n := jobNotifier
	jobNotifierMu.RUnlock()
	if n != nil {
		n.Notify(text)
	}
}

// markQuestionsShown tells the wired JobNotifier that each jobID/seq pair in
// questions was just delivered via a handoff message, so a "child is
// blocked on a question" notice already queued for that exact ask is left
// out the next time the queue drains — see handOff (subagent.go), which is
// the only caller, and JobNotifier.MarkQuestionShown's doc comment for why
// this keys on seq (jobs.Job.QuestionSeq), not the question text. A no-op
// with no notifier wired (tests that don't exercise this).
func markQuestionsShown(questions map[string]pendingQuestion) {
	if len(questions) == 0 {
		return
	}
	jobNotifierMu.RLock()
	n := jobNotifier
	jobNotifierMu.RUnlock()
	if n == nil {
		return
	}
	for jobID, q := range questions {
		n.MarkQuestionShown(jobID, q.Seq)
	}
}

// userPending reports whether a person has typed something that the agent has
// not read yet.
//
// It exists so a tool that CAN hand its work to the background does so at
// once, rather than making the person wait out the rest of a 30- or 60-second
// window. The wait is the only reason typing feels blocked: the work itself
// carries on either way, so there is nothing to gain by holding the turn open.
//
// A function rather than a channel because the only thing this package needs
// to ask is "is someone waiting", and the answer lives in the frontend's
// pending-message queue, which this package must not import.
var (
	userPendingMu sync.RWMutex
	userPendingFn func() bool
)

// SetUserPending wires the frontend's "a line is queued" check. nil (the
// default, and the case in one-shot runs where nobody can type) means nobody
// is ever waiting.
func SetUserPending(fn func() bool) {
	userPendingMu.Lock()
	userPendingFn = fn
	userPendingMu.Unlock()
}

// UserPending reports whether someone is waiting for the agent's attention.
func UserPending() bool {
	userPendingMu.RLock()
	fn := userPendingFn
	userPendingMu.RUnlock()
	return fn != nil && fn()
}

// userPendingPoll is how often a blocking tool checks. Short enough that
// typing feels answered, long enough to be free.
const userPendingPoll = 250 * time.Millisecond

// bgRegistry tracks the backgrounded commands that are still running, so
// they can be killed individually ("kill_job") or all at once on shutdown
// (KillAllBackgroundBash). The stored func is the job context's cancel: the
// job goroutine watches that context and kills the process group, so one
// cancel path covers both the timeout backstop and an explicit kill.
var bgRegistry = struct {
	mu    sync.Mutex
	kill  map[string]context.CancelFunc
	order []string // insertion order, for a stable KillAll and listing
	slots int      // currently occupied background slots
}{kill: make(map[string]context.CancelFunc)}

// acquireBackgroundSlot reserves one of the maxBackgroundBash slots.
// Returns false when they are all taken; the caller must then keep running
// the command in the foreground rather than silently exceeding the cap.
func acquireBackgroundSlot() bool {
	bgRegistry.mu.Lock()
	defer bgRegistry.mu.Unlock()
	if bgRegistry.slots >= maxBackgroundBash {
		return false
	}
	bgRegistry.slots++
	return true
}

func releaseBackgroundSlot() {
	bgRegistry.mu.Lock()
	defer bgRegistry.mu.Unlock()
	if bgRegistry.slots > 0 {
		bgRegistry.slots--
	}
}

func registerBackgroundBash(jobID string, kill context.CancelFunc) {
	bgRegistry.mu.Lock()
	defer bgRegistry.mu.Unlock()
	bgRegistry.kill[jobID] = kill
	bgRegistry.order = append(bgRegistry.order, jobID)
}

func unregisterBackgroundBash(jobID string) {
	bgRegistry.mu.Lock()
	defer bgRegistry.mu.Unlock()
	delete(bgRegistry.kill, jobID)
	for i, id := range bgRegistry.order {
		if id == jobID {
			bgRegistry.order = append(bgRegistry.order[:i], bgRegistry.order[i+1:]...)
			break
		}
	}
}

// killBackgroundBash cancels one backgrounded command's context, which makes
// its job goroutine kill the process group and record a killed result.
// Returns false when the id is not a currently-running background command
// (unknown, already finished, or a job of another kind).
func killBackgroundBash(jobID string) bool {
	bgRegistry.mu.Lock()
	kill, ok := bgRegistry.kill[jobID]
	bgRegistry.mu.Unlock()
	if !ok {
		return false
	}
	kill()
	return true
}

// runningBackgroundBash lists the ids of backgrounded commands still
// running, in the order they were started.
func runningBackgroundBash() []string {
	bgRegistry.mu.Lock()
	defer bgRegistry.mu.Unlock()
	out := make([]string, len(bgRegistry.order))
	copy(out, bgRegistry.order)
	return out
}

// KillAllBackgroundBash kills every still-running backgrounded command and
// returns how many it signalled. Call it on shutdown: these processes are
// deliberately detached from the tool call and the session context, so
// nothing else would reap them, and a half-finished build outliving the
// session that started it is a surprise, not a feature.
func KillAllBackgroundBash() int {
	bgRegistry.mu.Lock()
	kills := make([]context.CancelFunc, 0, len(bgRegistry.kill))
	ids := make([]string, 0, len(bgRegistry.kill))
	for id := range bgRegistry.kill {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		kills = append(kills, bgRegistry.kill[id])
	}
	bgRegistry.mu.Unlock()

	for _, kill := range kills {
		kill()
	}
	return len(kills)
}

// backgroundSlotsInUse reports how many background slots are occupied. Used
// by tests to wait for the slots to drain after killing commands, since a
// slot is released by the job goroutine and not by the kill itself.
func backgroundSlotsInUse() int {
	bgRegistry.mu.Lock()
	defer bgRegistry.mu.Unlock()
	return bgRegistry.slots
}

// KillJobTool implements the "kill_job" tool: stops a backgrounded shell
// command (killing its whole process group) or a running subagent job
// (plus, via the registry's subtree cascade, every background command that
// subagent itself started). Which path runs is decided by what the id
// resolves to — bgRegistry first for live commands, then the job registry's
// kind dispatch — not by guessing from the id's shape. See killjob.go.
type KillJobTool struct{}

func (t *KillJobTool) Name() string { return "kill_job" }
