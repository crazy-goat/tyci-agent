package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crazy-goat/tyci-agent/api"
	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/stream"
)

type ToolRunner interface {
	Run(ctx context.Context, name string, args map[string]any) (string, error)
}

// Config is everything the agent loop needs beyond the model client itself.
//
// It deliberately carries NO model or provider name: those are properties of
// the connector.ModelClient handed to Run, read back via mc.Model() /
// mc.Provider(). Two sources of truth for "which model is this" is exactly
// how a request ends up going to one model while the session header records
// another. A caller that needs the name for its own purposes (session
// bookkeeping, a status bar) keeps its own variable.
type Config struct {
	System        string
	MaxRetries    int
	MaxIterations int // max tool-call iterations; -1 or 0 means unlimited
	Debug         bool
	Tools         ToolRunner
	Schema        json.RawMessage
	Session       *session.Session // optional session logging / resume

	// Temperature, when non-nil, is forwarded on every connector.Request this
	// run issues — to the primary model and, if it fails, to every fallback in
	// turn (see runOnce and tryFallback below: both read cfg.Temperature, and
	// tryFallback passes the same cfg straight through, so there is only one
	// place this could get lost). A pointer, not a plain float64, because 0 is
	// a meaningful value ("deterministic") and must stay distinguishable from
	// "the caller never set it" — nil means the parameter is omitted from the
	// wire request entirely, not sent as 0.
	Temperature *float64

	// MaxTokens caps the model's reply length on every connector.Request this
	// run issues, primary and fallbacks alike (same single-place plumbing as
	// Temperature above). Zero means unset, and what that means is the
	// provider's business: Anthropic requires the field so its connector
	// substitutes a default, while OpenAI and Gemini simply omit it.
	MaxTokens int

	// Effort is the reasoning effort sent on every request of this run.
	// Empty means the provider URI option or the provider default.
	Effort string

	// NoPromptCache disables provider-side prompt caching for this run. See
	// connector.Request.NoPromptCache for why it is phrased as a negative.
	NoPromptCache bool

	// Fallbacks are already-resolved fallback models, tried in order when
	// the primary (or the previously-active fallback) fails. The agent does
	// not resolve "provider/model" strings itself — the caller does, before
	// calling Run, so the agent never needs to know about the provider
	// catalog. A spec that fails to resolve is the caller's problem to
	// report; by the time it reaches here every entry is a working client.
	Fallbacks []connector.ModelClient

	// NextMessages is called by the agent loop after each runOnce to drain
	// any user messages queued while a request was in flight (issue #88).
	// The callback returns the messages in FIFO order. If it returns a
	// non-empty slice while the agent would otherwise return (no more
	// tool calls), the agent runs one additional iteration to deliver them.
	NextMessages func() []string

	// PendingTodos, if set, is called when the agent would otherwise finish
	// the turn (the model produced no tool calls and no user messages are
	// queued). It returns the formatted lines of still-open todo items
	// (status todo/doing). When non-empty, the agent injects a system
	// reminder — framed as coming from the harness, not the user — asking
	// the model to either finish those tasks or explicitly resolve them
	// (done/blocked), then runs one more iteration. This happens at most
	// maxTodoReminders times per turn to avoid nagging in a loop.
	PendingTodos func() []string

	// ActiveSubagents, if set, reports whether a subagent is genuinely still
	// running. While true, pending-todo reminders are suppressed: the model is
	// intentionally waiting for delegated work rather than forgetting its plan.
	// Waiting-for-answer and terminal children must return false, so reminders
	// remain actionable once a child reports or finishes.
	ActiveSubagents func() bool

	// PendingJobs, if set, is called when the agent would otherwise finish the
	// turn and returns one line per background job that is still running or —
	// worse — blocked waiting for an answer. When a line describes a blocked
	// job the agent injects a harness-authored reminder and runs one more
	// iteration, the same shape as PendingTodos above. In an interactive
	// session (console and TUI) a running job alone does not: it sends its
	// own completion notice. A non-interactive run (`tyci run`) never drains
	// those notices, so there a running job is reminded about too.
	//
	// This exists because a forgotten blocked child is the most expensive
	// mistake this environment makes possible. It sits there making no
	// progress until its wall clock runs out, at which point everything it did
	// is discarded — and the only thing that could have unblocked it was one
	// answer_job() call from a turn that has already ended.
	PendingJobs func() []string

	// Compactor is called by the model-facing compact tool at a safe turn
	// boundary. It appends an event and updates the live conversation.
	Compactor func(summary, focus string) (string, error)

	// ContextLimitFor returns the current model's published context window.
	// It is evaluated at each turn so a fallback model gets its own limit.
	ContextLimitFor func(provider, model string) int

	// ContextLimit is the published context window in tokens. Zero disables
	// the budget reminder when the catalog has no known limit.
	ContextLimit int

	// SoftLimit and HardLimit are user-set context sizes in tokens (0 =
	// unset). Past SoftLimit the agent gets a notice and may call compact;
	// past HardLimit the harness compacts without asking. Each is capped
	// by the model window; unset ones are computed from the window (see
	// compactThresholds).
	SoftLimit int
	HardLimit int

	// AutoCompactPercent is the legacy way to set the hard limit as a
	// percentage of the window. It only applies when HardLimit is 0. A
	// negative value disables auto-compaction, leaving only the notice.
	AutoCompactPercent int

	// InLoopCompaction is for agents without a session of their own
	// (subagents, flow roles): the soft notice and the hard-limit
	// compaction run after each tool round instead of at the end of the
	// turn, because the end of such a run is the end of its work. The
	// compaction is in memory: it keeps the task (the last message on
	// entry to Run), a note and the last messages (compactInMemory).
	InLoopCompaction bool

	// Interactive reports whether a human is present to answer a blocked
	// job's question — true for the TUI, false for
	// `tyci run` (and anything shelling out to it, e.g. cron: see
	// initCommon's doc comment in commands.go). PendingJobs is wired in
	// every mode (see its own doc comment), but only an interactive mode
	// has a person there to reply in the conversation at all — the wording
	// buildJobReminder picks depends on this, since telling a non-
	// interactive run to wait on a human describes someone who is not
	// there.
	Interactive bool

	// ProgressHeartbeat, if set, is checked once per loop iteration — the
	// same "next iteration boundary" spot as the last-step warning below —
	// and reports whether this run has gone quiet long enough (no
	// report_progress note) to deserve a harness-authored nudge asking it to
	// post one. It only ever applies to a subagent's own loop (wired from
	// tools.JobProgressHeartbeatCheck, bound to that job's own id); the main
	// conversation has nothing watching it that this could unblock, so it is
	// left nil there.
	//
	// The callback carries no threshold or "have I already nagged" state of
	// its own to track here: it already re-arms itself once it fires (see
	// jobs.Registry.NeedsProgressHeartbeat), so calling it more than once
	// per interval simply keeps returning false. That is why, unlike
	// PendingTodos/PendingJobs above, there is no separate
	// maxProgressHeartbeats counter next to maxTodoReminders/
	// maxJobReminders — the time gate itself is what keeps this from
	// crowding out the real conversation, per item 15's decided design
	// (time-based, not step-based; the threshold is ping_interval/2, see main.go and tools.JobProgressHeartbeatCheck).
	ProgressHeartbeat func() bool

	// AutoPing, if set, is called once per loop iteration after a
	// ProgressHeartbeat nudge was sent. last describes the last tool call
	// ("bash: go test ./..."). The callback owns the timing (see
	// jobs.Registry.AutoProgress): it posts only when a full ping interval
	// passed without a note, so calling it every iteration is cheap.
	AutoPing func(last string)
}

// maxTodoReminders bounds how many times, within a single turn, the agent
// will nudge itself about unfinished todos before giving up and returning.
const maxTodoReminders = 2

// maxJobReminders bounds the background-job nudge the same way. One reminder
// is usually enough; a second covers the case where the model answers one
// blocked job and forgets a second.
const maxJobReminders = 2

// lastStepDeadlineThreshold is how much time must remain until ctx's
// deadline before Run injects the last-step warning (see
// buildLastStepWarning). It is a guess by necessity — there is no way to
// know in advance how long the model's next round trip plus any tool call it
// makes will take — so it is picked deliberately generous: long enough to
// cover a slow model response and one ordinary tool call, short enough that
// it still fires with real turns left rather than only in the final second.
// 45s mirrors the same order of magnitude as SubagentBackgroundAfter
// (tools/subagent.go), which makes the same kind of "one more round trip"
// judgment call for a different deadline.
const lastStepDeadlineThreshold = 45 * time.Second

// ErrMaxIterations is returned by Run when the loop reaches MaxIterations
// without the model finishing its turn. Top-level callers treat it as a
// graceful stop (the warning is already shown via the display), but the
// subagent runner surfaces it so the parent agent learns the child ran out
// of budget — likely stuck in a tool-call loop — instead of silently
// receiving an empty or truncated "successful" result.
var ErrMaxIterations = errors.New("agent reached max tool-call iterations without finishing")

// Run executes the agent loop. It will make at most MaxRetries retries on
// transient errors, and at most MaxIterations tool-call iterations.
// If MaxIterations <= 0, there is no iteration limit.
// If cfg.Fallbacks is set, non-retryable errors will try fallback models
// before giving up. Once a fallback succeeds, it is used for the rest of the session.
// Returns total usage accumulated during the run.
func Run(ctx context.Context, mc connector.ModelClient, d Sink, msgs *[]connector.Message, cfg Config) (stream.Usage, error) {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 5
	}

	var totalUsage stream.Usage

	// task is the message Run was started with; in-loop compaction keeps it.
	var task *connector.Message
	if cfg.InLoopCompaction && len(*msgs) > 0 {
		m := (*msgs)[len(*msgs)-1]
		task = &m
	}

	// lastRoundUsage is the most recent single model call's raw usage — not
	// totalUsage, which sums every iteration's full resent context and so
	// would overcount the current window many times over across a multi-tool
	// turn. Input+CacheRead+CacheWrite+Output is what actually occupies the
	// window next: a cached prompt prefix (Anthropic reports it separately
	// from Input, and prompt caching is on by default) still counts against
	// the limit even though it is not re-billed at full price. Note:
	// display.TuiModel.contextUsed sums only Input+Output for the status
	// bar's context figure and has the same undercount on a cache hit — a
	// pre-existing, separate bug outside this item's scope.
	var lastRoundUsage stream.Usage

	// Tracks how many todo reminders we've injected this turn (see maxTodoReminders).
	todoReminders := 0
	jobReminders := 0
	contextReminded := false
	autoCompacted := false

	// lastStepWarned ensures buildLastStepWarning is injected at most once
	// per Run call: once the model has been told this is its last turn,
	// there is nothing more useful to say even if, against instructions, it
	// spends that turn on a tool call and a further iteration happens.
	lastStepWarned := false

	// Track fallback state across iterations
	fs := fallbackState{idx: -1, mc: mc}

	nudged := false
	for iter := 0; cfg.MaxIterations <= 0 || iter < cfg.MaxIterations; iter++ {
		// Warn the model, one turn ahead, when an explicit iteration cap or
		// caller deadline is about to stop this run. Ordinary subagent runs
		// configure neither, so they do not receive a synthetic last-step
		// warning. This must run before runOnce so a capped final turn sees it.
		//
		// warnedThisIteration tracks only THIS turn's last-step warning — unlike
		// lastStepWarned (sticky for the rest of Run, so the warning itself is
		// never repeated), it exists solely to keep the progress-heartbeat nudge
		// below off the same turn as the warning. Using lastStepWarned for that
		// too was a bug (review finding on item 15): it made the heartbeat
		// permanently disabled after the FIRST warning fired, instead of only
		// skipped on that one turn — a subagent that somehow keeps running past
		// its warned "last" turn (e.g. a caller deadline that never actually
		// cancels the ctx) would then go the rest of its life with no nudge at
		// all.
		warnedThisIteration := false
		if !lastStepWarned {
			warn := cfg.MaxIterations > 0 && iter == cfg.MaxIterations-1
			if !warn {
				if dl, ok := ctx.Deadline(); ok && time.Until(dl) < lastStepDeadlineThreshold {
					warn = true
				}
			}
			if warn {
				lastStepWarned = true
				warnedThisIteration = true
				reminder := buildLastStepWarning()
				*msgs = append(*msgs, connector.Message{
					Role:    "user",
					Content: []connector.ContentBlock{{Type: "text", Text: reminder}},
				})
				if cfg.Session != nil {
					blocks := []session.ContentBlock{{Type: "text", Text: reminder}}
					_ = cfg.Session.WriteMessage("user", blocks, nil)
				}
			}
		}
		// The progress-heartbeat nudge is independent of lastStepWarned's
		// sticky state — only gated on NOT sharing a turn with the last-step
		// warning above (which forbids tool calls; report_progress is one).
		if !warnedThisIteration && cfg.ProgressHeartbeat != nil && cfg.ProgressHeartbeat() {
			nudged = true
			reminder := buildProgressHeartbeatReminder()
			*msgs = append(*msgs, connector.Message{
				Role:    "user",
				Content: []connector.ContentBlock{{Type: "text", Text: reminder}},
			})
			if cfg.Session != nil {
				blocks := []session.ContentBlock{{Type: "text", Text: reminder}}
				_ = cfg.Session.WriteMessage("user", blocks, nil)
			}
		}
		if nudged && cfg.AutoPing != nil {
			cfg.AutoPing(lastToolSummary(*msgs))
		}
		// runOnce accumulates usage into totalUsage and emits d.Total
		// only when it reaches the Summary line. totalEmitted reports
		// whether the call already showed the Costs line; if not, the
		// caller must emit it (typically on error/early-return paths).
		more, roundUsage, totalEmitted, err := runOnce(ctx, fs.mc, d, msgs, cfg, &totalUsage)
		if roundUsage != nil {
			lastRoundUsage = *roundUsage
		}
		if err != nil {
			// Check for context cancellation first
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				if !totalEmitted {
					d.Total(totalUsage)
				}
				return totalUsage, err
			}

			// Fallbacks are for terminal/non-retryable failures. A transient
			// 429/5xx/network error must go through the primary retry policy
			// first; switching models immediately can create an unexpected,
			// expensive second request while the primary was recoverable.
			if len(cfg.Fallbacks) > 0 && !api.IsRetryable(err) {
				fbMore, fbUsage, fbErr := tryFallback(ctx, d, msgs, cfg, &fs, &totalUsage, err)
				if fbUsage != nil {
					lastRoundUsage = *fbUsage
				}
				if fbErr == nil {
					// Fallback succeeded — runOnce inside tryFallback
					// already emitted d.Total.
					if !fbMore {
						return totalUsage, nil
					}
					continue
				}
				// All fallbacks failed
				d.Error(fmt.Errorf("all fallback models exhausted: %v", fbErr))
				if !totalEmitted {
					d.Total(totalUsage)
				}
				return totalUsage, fbErr
			}

			// Use retry logic for retryable errors. With no fallbacks this is
			// also the only path available; with fallbacks, the retry path is
			// intentionally still preferred for transient failures.
			if !api.IsRetryable(err) {
				d.Error(err)
				if !totalEmitted {
					d.Total(totalUsage)
				}
				return totalUsage, err
			}

			lastErr := err
			recovered := false
			for attempt := 0; attempt < cfg.MaxRetries; attempt++ {
				backoff := api.CalcBackoff(attempt, lastErr, api.RetryConfig{MaxRetries: cfg.MaxRetries})
				d.ToolBlock(fmt.Sprintf("retry %d/%d — %s", attempt+1, cfg.MaxRetries, lastErr.Error()))
				if err := sleepWithCountdown(ctx, backoff); err != nil {
					if !totalEmitted {
						d.Total(totalUsage)
					}
					return totalUsage, err
				}
				var roundUsage *stream.Usage
				more, roundUsage, totalEmitted, err = runOnce(ctx, fs.mc, d, msgs, cfg, &totalUsage)
				if roundUsage != nil {
					lastRoundUsage = *roundUsage
				}
				if err == nil {
					recovered = true
					break
				}
				lastErr = err
				if !api.IsRetryable(err) {
					// Non-retryable during retry — try fallback if available
					if len(cfg.Fallbacks) > 0 {
						fbMore, fbUsage, fbErr := tryFallback(ctx, d, msgs, cfg, &fs, &totalUsage, err)
						if fbUsage != nil {
							lastRoundUsage = *fbUsage
						}
						if fbErr == nil {
							if !fbMore {
								return totalUsage, nil
							}
							recovered = true
							break
						}
						d.Error(fmt.Errorf("all fallback models exhausted: %v", fbErr))
						if !totalEmitted {
							d.Total(totalUsage)
						}
						return totalUsage, fbErr
					}
					d.Error(err)
					if !totalEmitted {
						d.Total(totalUsage)
					}
					return totalUsage, err
				}
			}
			if !recovered {
				d.Error(fmt.Errorf("all %d retries exhausted: %v", cfg.MaxRetries, lastErr))
				if !totalEmitted {
					d.Total(totalUsage)
				}
				return totalUsage, lastErr
			}
		}
		// Issue #88: drain the pending-message queue after EVERY runOnce
		// (not only when more == false). This is the "next safe point":
		// the model has just returned, tool results have been appended,
		// and the next runOnce will call the model again. Draining here
		// means the model sees the user's queued follow-ups on the very
		// next call — including in the middle of a tool-call loop —
		// rather than waiting for the entire turn to finish.
		drained := false
		// Do not drain after the final allowed iteration: there is no next
		// runOnce to deliver those messages to. A completion notice must remain
		// queued for the next turn (or an idle wakeup), rather than being
		// consumed and silently lost here.
		canDrain := cfg.MaxIterations <= 0 || iter+1 < cfg.MaxIterations
		if canDrain && cfg.NextMessages != nil {
			if pending := cfg.NextMessages(); len(pending) > 0 {
				for _, line := range pending {
					*msgs = append(*msgs, connector.Message{
						Role:    "user",
						Content: []connector.ContentBlock{{Type: "text", Text: line}},
					})
					if cfg.Session != nil {
						blocks := []session.ContentBlock{{Type: "text", Text: line}}
						_ = cfg.Session.WriteMessage("user", blocks, nil)
					}
				}
				drained = true

				// A person can type at any moment, including while work is
				// running, and a tool that could hand its work to the
				// background does so as soon as that happens. So the model
				// arrives here mid-task and needs to be told two things it
				// cannot see: that nothing was cancelled, and that answering
				// now is the right move. Without this it reads an
				// out-of-nowhere question as a change of instructions and
				// abandons what it was doing.
				if cfg.PendingJobs != nil {
					if jobs := cfg.PendingJobs(); len(jobs) > 0 {
						note := buildInterruptNote(jobs)
						*msgs = append(*msgs, connector.Message{
							Role:    "user",
							Content: []connector.ContentBlock{{Type: "text", Text: note}},
						})
						if cfg.Session != nil {
							blocks := []session.ContentBlock{{Type: "text", Text: note}}
							_ = cfg.Session.WriteMessage("user", blocks, nil)
						}
					}
				}
			}
		}
		// A real user follow-up starts a fresh sub-task; reset the reminder
		// budget so we're willing to nag again about whatever it leaves open.
		if drained {
			todoReminders = 0
			jobReminders = 0
		}
		contextLimit := func() int {
			if cfg.ContextLimitFor != nil {
				return cfg.ContextLimitFor(fs.mc.Provider(), fs.mc.Model())
			}
			return cfg.ContextLimit
		}
		if cfg.InLoopCompaction && (more || drained) {
			used := lastRoundUsage.Input + lastRoundUsage.CacheRead + lastRoundUsage.CacheWrite + lastRoundUsage.Output
			softAt, hardAt := compactThresholds(contextLimit(), cfg.SoftLimit, cfg.HardLimit, cfg.AutoCompactPercent)
			switch {
			case used > 0 && hardAt > 0 && used >= hardAt:
				if compactInMemory(msgs, task, buildInLoopCompactNote(used, hardAt)) {
					contextReminded = false
				}
			case used > 0 && softAt > 0 && used >= softAt && !contextReminded:
				contextReminded = true
				reminder := buildContextBudgetReminder(used, hardAt, false)
				*msgs = append(*msgs, connector.Message{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: reminder}}})
			}
		}
		if !more && !drained {
			limit := contextLimit()
			if !cfg.InLoopCompaction && (limit > 0 || cfg.SoftLimit > 0 || cfg.HardLimit > 0) {
				// Input+Output alone undercounts on providers that report a
				// cached prompt prefix separately (Anthropic: CacheRead/
				// CacheWrite, see api/anthropic.go — prompt caching is on by
				// default, connector/anthropic.go). Cached tokens still
				// occupy the context window even though they were not
				// re-billed at full price, so they must count here.
				used := lastRoundUsage.Input + lastRoundUsage.CacheRead + lastRoundUsage.CacheWrite + lastRoundUsage.Output
				softAt, hardAt := compactThresholds(limit, cfg.SoftLimit, cfg.HardLimit, cfg.AutoCompactPercent)
				justAutoCompacted := false
				// cfg.Session != nil (review, F5 HIGH-3): a /btw or
				// fork/resume child keeps the PARENT's Compactor (it closes
				// over the main conversation, not this one — btwConfig,
				// btw.go) while its own cfg.Session is nil. Without this
				// guard, a long-running child would auto-compact the live
				// main conversation on the harness's own initiative, no
				// model or user action involved — the automatic version of
				// the hole F10 closed for the model-driven path.
				if !autoCompacted && hardAt > 0 && cfg.Compactor != nil && cfg.Session != nil &&
					used > 0 && used >= hardAt {
					autoCompacted = true
					dumpPath := ""
					if cfg.Session != nil {
						dumpPath = session.DumpPathFor(cfg.Session.Path())
					}
					summary := buildAutoCompactSummary(used, hardAt, dumpPath)
					_, compactErr := cfg.Compactor(summary, "")
					// Whether or not compaction succeeded, do NOT `continue`
					// here (review of F5): the model has already finished
					// its answer for this turn (!more && !drained), and
					// every other `continue` in this block first appends a
					// user-role message to *msgs — re-invoking the provider
					// right now would send a history ending on the
					// assistant's own just-delivered turn, which Anthropic
					// treats as an invalid prefill (rejects trailing
					// whitespace) and which every other provider would just
					// answer again with no new instruction. The benefit of
					// compacting lands on the NEXT turn's request, which is
					// the point of a backstop — fall through to return
					// normally for this one.
					if compactErr == nil {
						// A fresh compaction leaves a tiny history; give the
						// model a full reminder cycle again if it somehow
						// grows back past the (lower) reminder threshold on
						// a later turn. justAutoCompacted also skips the
						// reminder check just below for THIS turn — `used`
						// was measured before compaction ran, so checking it
						// against the (unchanged) reminder threshold right
						// now would fire a budget reminder based on a number
						// that is already stale.
						contextReminded = false
						justAutoCompacted = true
					}
					// Compaction failing (e.g. no writable session) is not
					// re-reported here — the reminder below still gets the
					// fact in front of the model even though the harness
					// could not act on it itself, same as before this fix.
				}
				if !justAutoCompacted && !contextReminded && used > 0 && softAt > 0 && used >= softAt {
					contextReminded = true
					// cfg.Compactor != nil (F10 review follow-up, deferred
					// until package B's agent.go rewrite landed): unlike a
					// plain subagent (never had compact — subagentDeniedTools
					// already excludes it), a /btw or fork/resume child has
					// no Compactor of its own (btwConfig nils it, F10) and
					// compact is no longer even in its schema
					// (GetAllToolsSchemaJSONWithout) — telling it to call
					// compact(...) anyway would point at a tool it does not
					// have, the wasted-round-trip class F10's review found.
					// The budget fact itself is still worth surfacing (it
					// can still persist to memory/a file), so only the
					// compact-specific instruction is conditional, not the
					// whole reminder.
					// Name the hard limit only when auto-compaction can fire
					// for this agent (same gate as above); children without
					// their own Compactor and Session would be told a lie.
					noticeHardAt := 0
					if cfg.Compactor != nil && cfg.Session != nil {
						noticeHardAt = hardAt
					}
					reminder := buildContextBudgetReminder(used, noticeHardAt, cfg.Compactor != nil)
					*msgs = append(*msgs, connector.Message{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: reminder}}})
					if cfg.Session != nil {
						_ = cfg.Session.WriteMessage("user", []session.ContentBlock{{Type: "text", Text: reminder}}, nil)
					}
					continue
				}
			}
			// The model thinks it's done. Before returning, check whether it
			// left todos open and, if so, nudge it once more (up to
			// maxTodoReminders) with a harness-authored reminder — not a user
			// message — asking it to finish or explicitly resolve them.
			if cfg.PendingTodos != nil && todoReminders < maxTodoReminders &&
				(cfg.ActiveSubagents == nil || !cfg.ActiveSubagents()) {
				if pending := cfg.PendingTodos(); len(pending) > 0 {
					todoReminders++
					reminder := buildTodoReminder(pending)
					*msgs = append(*msgs, connector.Message{
						Role:    "user",
						Content: []connector.ContentBlock{{Type: "text", Text: reminder}},
					})
					if cfg.Session != nil {
						blocks := []session.ContentBlock{{Type: "text", Text: reminder}}
						_ = cfg.Session.WriteMessage("user", blocks, nil)
					}
					continue
				}
			}
			// Same idea one level out: a background job left running is
			// usually fine, but a job blocked on a question is a dead end
			// only this turn can open. In an interactive session a running
			// job sends its own completion notice, so only a blocked one is
			// worth a reminder there. A non-interactive run never drains
			// notices and exits when the turn ends, so it reminds about
			// running jobs too.
			if cfg.PendingJobs != nil && jobReminders < maxJobReminders {
				if pending := cfg.PendingJobs(); hasBlockedJob(pending) || (!cfg.Interactive && len(pending) > 0) {
					jobReminders++
					reminder := buildJobReminder(pending, cfg.Interactive)
					*msgs = append(*msgs, connector.Message{
						Role:    "user",
						Content: []connector.ContentBlock{{Type: "text", Text: reminder}},
					})
					if cfg.Session != nil {
						blocks := []session.ContentBlock{{Type: "text", Text: reminder}}
						_ = cfg.Session.WriteMessage("user", blocks, nil)
					}
					continue
				}
			}
			// runOnce emitted d.Total as part of the Summary block.
			return totalUsage, nil
		}
		// If more == true, loop continues naturally (tool call).
		// If more == false but drained, loop continues (forced) so the
		// model sees the queued messages. MaxIterations still applies.
	}

	if cfg.MaxIterations > 0 {
		d.Text(fmt.Sprintf("\n⚠️ Agent executed %d tool-call iterations – possible infinite loop. Stopping.\n", cfg.MaxIterations))
		return totalUsage, ErrMaxIterations
	}
	return totalUsage, nil
}

// Automatic limits used when the user sets no soft or hard limit: percent of
// the model's context window.
const (
	autoSoftPercent = 80
	autoHardPercent = 95
)

// buildLastStepWarning produces the harness-authored message injected right
// before what the harness expects to be the model's final turn — see the
// lastStepWarned block in Run for the two triggers (iteration cap, wall-clock
// deadline). Framed as an automated check, not the user, same as
// buildTodoReminder/buildJobReminder below.
//
// It explicitly forbids tool calls this turn. Whichever limit is about to
// fire ends the loop the instant this turn completes, so a tool call here
// would never have its result seen by the model — the only useful thing it
// can do with this turn is write its summary as plain text right now.
// compactThresholds returns the token counts at which the soft notice and the
// automatic compaction fire. A user limit is capped by the window. An unset
// soft limit is autoSoftPercent of the window; an unset hard limit is
// hardPercent (legacy auto_compact_percent) or autoHardPercent of the
// window. With an unknown window (0) only user limits apply. A 0 in the
// result means that trigger is off. The two values are independent: a hard
// limit below the automatic soft value means the compaction fires first and
// the soft notice does not appear. Set a soft limit to get the notice.
func compactThresholds(window, soft, hard, hardPercent int) (softAt, hardAt int) {
	capped := func(v int) int {
		if window > 0 {
			return min(v, window)
		}
		return v
	}
	switch {
	case soft > 0:
		softAt = capped(soft)
	default:
		softAt = window * autoSoftPercent / 100
	}
	switch {
	case hard > 0:
		hardAt = capped(hard)
	case hardPercent < 0:
		hardAt = 0
	case hardPercent > 0:
		hardAt = window * hardPercent / 100
	default:
		hardAt = window * autoHardPercent / 100
	}
	return softAt, hardAt
}

// buildContextBudgetReminder is the soft-limit notice. hardAt is the token
// count of the automatic compaction, 0 when it is off.
func buildContextBudgetReminder(used, hardAt int, canCompact bool) string {
	msg := fmt.Sprintf("[automated context budget reminder, not the user] Your context is now %d tokens.", used)
	if hardAt > 0 {
		msg += fmt.Sprintf(" At %d tokens (hard limit) it will be compacted automatically.", hardAt)
	}
	if canCompact {
		return msg + " Do you want to compact now? If yes, persist anything important, then call compact(summary=\"...\", focus=\"...\")."
	}
	return msg + " Persist anything important now if continuing would crowd out useful history — compact is not available in this conversation."
}

// buildInLoopCompactNote is the note that replaces the removed history in an
// in-loop compaction (Config.InLoopCompaction).
func buildInLoopCompactNote(used, hardAt int) string {
	return fmt.Sprintf("[automated context compaction, not the user] Your context reached %d tokens (hard limit %d), so the harness removed the older messages. Your task and the last messages remain. Read again any file you still need, then continue the task.", used, hardAt)
}

// buildAutoCompactSummary produces the lead message for a compaction the
// harness triggered, not the model. There is no model-authored summary to
// use (unlike the compact tool), but per item 10 design point (a) compaction
// never deletes anything — the raw JSONL and the dump at dumpPath both
// survive — so a factual marker naming the measured usage and where the
// full record lives costs nothing and lets the model recover context on
// request. Mirrors commands.go's manualCompactSummary for /compact.
func buildAutoCompactSummary(used, limit int, dumpPath string) string {
	return fmt.Sprintf("Automatic compaction triggered at %d context tokens (hard limit %d; no model or user request). Earlier turns are not repeated here — the raw session file and its markdown dump at %s hold the full record.", used, limit, dumpPath)
}

// buildProgressHeartbeatReminder produces the harness-authored nudge
// injected into a subagent's own loop when it has gone quiet — no
// report_progress note — for longer than ping_interval/2 (see
// tools.JobProgressHeartbeatCheck and jobs.Registry.NeedsProgressHeartbeat).
// Fire-and-forget by design (item 15): nothing requires the model to answer
// in any particular shape or even acknowledge this message, only to call
// report_progress once, so whoever is watching this job (wait, the jobs
// panel) is not blind for the whole run the way a child that never reports
// is today.
func buildProgressHeartbeatReminder() string {
	var b strings.Builder
	b.WriteString("<system-reminder>\n")
	b.WriteString("This is an automated check from the harness, not a message from the user. ")
	b.WriteString("You have been running for a while without posting a status update. ")
	b.WriteString("Call report_progress with one short line on where you are and what you're doing next, then continue your work as normal.\n")
	b.WriteString("</system-reminder>")
	return b.String()
}

func buildLastStepWarning() string {
	var b strings.Builder
	b.WriteString("<system-reminder>\n")
	b.WriteString("This is an automated check from the harness, not a message from the user. ")
	b.WriteString("You are about to run out of turns on this task — this is your LAST step. ")
	b.WriteString("Do NOT call any tools this turn: if you do, the harness stops before you would ever see the result, and that work is lost. ")
	b.WriteString("Instead, write your final summary now, as plain text: what you found or did, what remains unfinished, and anything a caller needs to pick up where you left off.\n")
	b.WriteString("</system-reminder>")
	return b.String()
}

// buildTodoReminder produces the harness-authored reminder injected when the
// agent tries to finish with open todos. It is deliberately framed as an
// automated system check (not a user message) and asks the model to either do
// the remaining work or explicitly resolve each item (done/blocked) — it must
// never leave items in todo/doing without explanation, and must not mark work
// done that it did not actually complete.
func buildTodoReminder(pending []string) string {
	var b strings.Builder
	b.WriteString("<system-reminder>\n")
	b.WriteString("You indicated you are finished, but the following todo items are still open (status todo/doing):\n\n")
	for _, line := range pending {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("\nThis is an automated check from the harness, not a message from the user. ")
	b.WriteString("Are you sure you're done? If these tasks still need doing, complete them now. ")
	b.WriteString("If a task is no longer relevant or cannot be done, set its status explicitly ")
	b.WriteString("(done, or blocked with a brief reason) via the todo tool. Do not leave items in ")
	b.WriteString("todo/doing without explanation, and do not mark anything done that you did not actually complete.\n")
	b.WriteString("</system-reminder>")
	return b.String()
}

// isBlockedJobLine reports whether one PendingJobs line describes a job that
// waits for an answer. The line format is the one of jobs.Registry.PendingLines.
func isBlockedJobLine(line string) bool {
	return strings.HasPrefix(line, "WAITING FOR ANSWER:")
}

// hasBlockedJob reports whether one of the PendingJobs lines describes a job
// that waits for an answer.
func hasBlockedJob(lines []string) bool {
	for _, line := range lines {
		if isBlockedJobLine(line) {
			return true
		}
	}
	return false
}

// buildJobReminder produces the harness-authored reminder injected when the
// agent tries to finish with background jobs outstanding.
//
// It leads with the blocked ones because the two cases need opposite
// responses: a running job is something to wait for or leave alone, while a
// blocked job needs a decision now or its work is thrown away. Framed as an
// automated check, not as a user asking — the user did not say this.
//
// interactive selects which decision is available for a WAITING FOR ANSWER
// job: in an interactive session (TUI) a human is present, so the
// model is told to relay the question in its reply, wait for the human's
// plain-text answer in the conversation, and then call answer_job itself
// with what they said — there is no dedicated slash command for a person
// to type; the model is the only thing that can ever call answer_job. In a
// non-interactive run (`tyci run`, cron) no one is there to reply to at
// all — telling the model to wait for a human there just stalls until the
// job's own timeout discards its work, so the model is told instead to
// either answer it itself if it genuinely knows the answer, or explicitly
// accept that it will go unanswered and finish without it.
//
// In an interactive session only the blocked lines are listed and no running
// job is mentioned: a running job sends its own completion notice there, so
// a reminder about it would only make the model wait for it.
func buildJobReminder(pending []string, interactive bool) string {
	var b strings.Builder
	b.WriteString("[automated check, not the user] Background jobs are still outstanding:\n")
	for _, line := range pending {
		if interactive && !isBlockedJobLine(line) {
			continue
		}
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	if interactive {
		b.WriteString("\nFor anything marked WAITING FOR ANSWER: relay the question to the user in your reply, wait for their answer in the conversation, then call answer_job(job_id=..., text=\"...\") with what they said — do not invent an answer on their behalf. Only call answer_job yourself right away if you already genuinely know the answer. Left unanswered it is making no progress and its work is discarded when it times out.")
		return b.String()
	}
	b.WriteString("\nFor anything marked WAITING FOR ANSWER: there is no user present in this run to answer it — only call answer_job(job_id=..., text=\"...\") yourself if you already genuinely know the answer from the context you have. Otherwise say plainly that it will go unanswered and finish without it; it will time out and its work will be discarded either way, so waiting for a reply that will never come only wastes the run. ")
	b.WriteString("For a job still running, either read it with wait(job_id=...) if you need the result, or say plainly in your reply that you are leaving it running — do not silently end the turn on it.")
	return b.String()
}

// buildInterruptNote explains the situation the model finds itself in when a
// person types while work is still running.
//
// The three things it has to say, in order of how badly they are missed:
// nothing was interrupted (or the model apologises and restarts work that is
// fine); answer the person first (they are waiting, the jobs are not); and
// there is no need to sit and wait, because a notice will arrive.
//
// Framed as the harness, not the user, for the same reason as the other
// injected reminders: the user did not write this.
func buildInterruptNote(jobs []string) string {
	var b strings.Builder
	b.WriteString("[automated note, not the user] They typed that while work was still running. ")
	b.WriteString("Nothing was cancelled — this is still going:\n")
	for _, line := range jobs {
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\nAnswer them first; they are waiting and the work is not. ")
	b.WriteString("You do not need to wait for any of it: you will be notified as each finishes, and wait(job_id=...) reads a result once you are told. ")
	b.WriteString("If they asked about the work itself, check it with wait(job_id=...) and tell them what you find.")
	return b.String()
}
