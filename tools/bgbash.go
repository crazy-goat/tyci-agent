package tools

import (
	"context"
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

	// BashBackgroundAfterSec is the most a bash call blocks. A command still
	// running then is moved to the background, and the agent gets its turn
	// back. The command is not killed at this point, unless no background slot
	// is free (see Run). Set above the runtime of an ordinary build or test
	// run: below that, the handoff would cost the model an extra polling turn
	// for commands that were about to finish anyway.
	BashBackgroundAfterSec = 30

	// BashBackgroundLimitSec caps the total run time of a command, also after
	// it has been moved to the background. A larger timeout is capped to it.
	// Nothing else bounds a backgrounded command once it is detached from the
	// tool call's context, so without this a wedged process would live until
	// tyci exits.
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

// JobNotifier is the view of the main conversation's notices that this
// package needs. The main conversation's notices travel on the bus; main()
// wires an implementation with SetJobNotifier.
//
// MarkAskShown tells the bus dedup (see bus_wiring.go) that a handoff message
// already carried question seq of job jobID, so the notice of that ask is left
// out of the next drain. It keys on seq, not on the question text, because
// jobs.Job.QuestionSeq is unique per ask (item 54 review finding 1). handOff
// (subagent.go) is the only caller.
//
// Queued returns how many notices reached the main conversation. wait compares
// two calls to see whether a new notice arrived.
type JobNotifier interface {
	MarkAskShown(jobID string, seq int)
	Queued() uint64
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
// JobNotifier (in practice the app's noticeCounter in bus_wiring.go, whose queue is
// drained into the agent loop and also wakes an idle REPL).
func SetJobNotifier(n JobNotifier) {
	jobNotifierMu.Lock()
	jobNotifier = n
	jobNotifierMu.Unlock()
}

// getJobNotifier copies the current JobNotifier out under RLock — see
// getJobMailbox's doc comment (message.go) for why callers never hold the lock
// while calling into the interface.
func getJobNotifier() JobNotifier {
	jobNotifierMu.RLock()
	defer jobNotifierMu.RUnlock()
	return jobNotifier
}

// NoticePublisher publishes one completion notice to the agent parentID, or to
// the orchestrator when parentID is "". A quiet notice waits for the next drain
// and does not wake an idle chat. The bus rewrites a notice to an agent that is
// no longer live. main() wires it with SetNoticePublisher.
type NoticePublisher func(parentID, text string, quiet bool)

var (
	noticePublisherMu sync.RWMutex
	noticePublisher   NoticePublisher
)

// SetNoticePublisher wires the completion notices of background work to fn.
func SetNoticePublisher(fn NoticePublisher) {
	noticePublisherMu.Lock()
	noticePublisher = fn
	noticePublisherMu.Unlock()
}

// sendNotice sends text to parentID through the notice publisher. A notice
// with no publisher wired is dropped; production always wires one.
func sendNotice(parentID, text string, quiet bool) {
	if text == "" {
		return
	}
	noticePublisherMu.RLock()
	fn := noticePublisher
	noticePublisherMu.RUnlock()
	if fn != nil {
		fn(parentID, text, quiet)
	}
}

// notifyToParent sends a notice that wakes an idle chat, see sendNotice.
func notifyToParent(parentID, text string) {
	sendNotice(parentID, text, false)
}

// parentEnded reports whether parentID names a job that can no longer receive
// a message. The registry marks a job terminal before it stops the background
// commands that job started, so a command stopped by its parent's end sees
// true here. With no mailbox wired the answer is unknown, and this reports
// false.
func parentEnded(parentID string) bool {
	mb := getJobMailbox()
	return parentID != "" && mb != nil && !mb.IsLive(parentID)
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
