package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
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
	// worse — blocked waiting for an answer. When non-empty the agent injects
	// a harness-authored reminder and runs one more iteration, the same shape
	// as PendingTodos above.
	//
	// This exists because a forgotten blocked child is the most expensive
	// mistake this environment makes possible. It sits there making no
	// progress until its wall clock runs out, at which point everything it did
	// is discarded — and the only thing that could have unblocked it was one
	// answer_job() call from a turn that has already ended.
	PendingJobs func() []string

	// HasTodos, if set, is called before executing tool calls to enforce
	// the "plan first" policy. It returns true when at least one todo
	// item exists (regardless of status). Non-todo tools are blocked with
	// an actionable error until the model creates a plan via the todo
	// tool. This ensures the model thinks through its approach before
	// acting.
	HasTodos func() bool

	// Interactive reports whether a human is present to answer a blocked
	// job's question — true for the console REPL and the TUI, false for
	// `tyci run` (and anything shelling out to it, e.g. cron: see
	// initCommon's doc comment in commands.go). PendingJobs is wired in
	// every mode (see its own doc comment), but only an interactive mode
	// has a person there to reply in the conversation at all — the wording
	// buildJobReminder picks depends on this, since telling a non-
	// interactive run to wait on a human describes someone who is not
	// there.
	Interactive bool
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
// 45s mirrors the same order of magnitude as SubagentBackgroundAfterSec
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

	// Tracks how many todo reminders we've injected this turn (see maxTodoReminders).
	todoReminders := 0
	jobReminders := 0

	// lastStepWarned ensures buildLastStepWarning is injected at most once
	// per Run call: once the model has been told this is its last turn,
	// there is nothing more useful to say even if, against instructions, it
	// spends that turn on a tool call and a further iteration happens.
	lastStepWarned := false

	// Track fallback state across iterations
	fs := fallbackState{idx: -1, mc: mc}

	for iter := 0; cfg.MaxIterations <= 0 || iter < cfg.MaxIterations; iter++ {
		// Warn the model, one turn ahead, that it is about to be cut off —
		// either by the iteration cap or by ctx's wall-clock deadline (the
		// latter is how a subagent's SubagentTimeoutSec actually terminates
		// it in practice; see tools/subagent.go). This must run BEFORE the
		// runOnce call below: at the true last iteration the model gets
		// exactly one more assistant turn, and if that turn is a tool call
		// the loop exits before the model ever sees the result — so the
		// warning has to already be in msgs for that final call to see it.
		if !lastStepWarned {
			warn := cfg.MaxIterations > 0 && iter == cfg.MaxIterations-1
			if !warn {
				if dl, ok := ctx.Deadline(); ok && time.Until(dl) < lastStepDeadlineThreshold {
					warn = true
				}
			}
			if warn {
				lastStepWarned = true
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
		// runOnce accumulates usage into totalUsage and emits d.Total
		// only when it reaches the Summary line. totalEmitted reports
		// whether the call already showed the Costs line; if not, the
		// caller must emit it (typically on error/early-return paths).
		more, _, totalEmitted, err := runOnce(ctx, fs.mc, d, msgs, cfg, &totalUsage)
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
				fbMore, fbErr := tryFallback(ctx, d, msgs, cfg, &fs, &totalUsage, err)
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

			var lastErr error = err
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
				more, _, totalEmitted, err = runOnce(ctx, fs.mc, d, msgs, cfg, &totalUsage)
				if err == nil {
					recovered = true
					break
				}
				lastErr = err
				if !api.IsRetryable(err) {
					// Non-retryable during retry — try fallback if available
					if len(cfg.Fallbacks) > 0 {
						fbMore, fbErr := tryFallback(ctx, d, msgs, cfg, &fs, &totalUsage, err)
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
		if !more && !drained {
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
			// only this turn can open.
			if cfg.PendingJobs != nil && jobReminders < maxJobReminders {
				if pending := cfg.PendingJobs(); len(pending) > 0 {
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

// buildJobReminder produces the harness-authored reminder injected when the
// agent tries to finish with background jobs outstanding.
//
// It leads with the blocked ones because the two cases need opposite
// responses: a running job is something to wait for or leave alone, while a
// blocked job needs a decision now or its work is thrown away. Framed as an
// automated check, not as a user asking — the user did not say this.
//
// interactive selects which decision is available for a WAITING FOR ANSWER
// job: in an interactive session (console/TUI) a human is present, so the
// model is told to relay the question in its reply, wait for the human's
// plain-text answer in the conversation, and then call answer_job itself
// with what they said — there is no dedicated slash command for a person
// to type; the model is the only thing that can ever call answer_job. In a
// non-interactive run (`tyci run`, cron) no one is there to reply to at
// all — telling the model to wait for a human there just stalls until the
// job's own timeout discards its work, so the model is told instead to
// either answer it itself if it genuinely knows the answer, or explicitly
// accept that it will go unanswered and finish without it.
func buildJobReminder(pending []string, interactive bool) string {
	var b strings.Builder
	b.WriteString("[automated check, not the user] Background jobs are still outstanding:\n")
	for _, line := range pending {
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	if interactive {
		b.WriteString("\nFor anything marked WAITING FOR ANSWER: relay the question to the user in your reply, wait for their answer in the conversation, then call answer_job(job_id=..., text=\"...\") with what they said — do not invent an answer on their behalf. Only call answer_job yourself right away if you already genuinely know the answer. Left unanswered it is making no progress and its work is discarded when it times out. ")
	} else {
		b.WriteString("\nFor anything marked WAITING FOR ANSWER: there is no user present in this run to answer it — only call answer_job(job_id=..., text=\"...\") yourself if you already genuinely know the answer from the context you have. Otherwise say plainly that it will go unanswered and finish without it; it will time out and its work will be discarded either way, so waiting for a reply that will never come only wastes the run. ")
	}
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
