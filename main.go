package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/eventbus"
	"github.com/decodo/tyci/internal/agentdefs"
	"github.com/decodo/tyci/internal/debug"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/tools"
)

// jobEventBus is the single, process-wide bus carrying JobRegistry's
// status-change events (topic "job.updated") to the TUI (the only
// subscriber today, wired in commands.go's tuiCmd via TUI.SetJobEventBus —
// see jobs.Registry.SetOnEvent). Package-level so both wiring sites share
// the exact same instance without threading it through function
// signatures; every other mode (console, --print, etc.) simply never
// subscribes, so this costs them nothing.
var jobEventBus = eventbus.New(32)

// JobNotices is the single, process-wide queue of short completion notices
// produced by background work — today, shell commands the bash tool moved to
// the background. It has two consumers, wired per mode: the agent loop drains
// it between turns (agent.Config.NextMessages), and an idle REPL selects on
// its Signal to start a turn of its own (see runTUI). Exported so integration
// tests in package main can assert on delivery.
var JobNotices = jobs.NewNotifier()

// mergeNextMessages composes several NextMessages-shaped drain callbacks into
// the single one agent.Config accepts, calling them in the order given so a
// user's own queued line is delivered ahead of a background notice that
// arrived in the same gap. nil callbacks are skipped, so a mode can pass
// whichever sources it actually has.
func mergeNextMessages(drains ...func() []string) func() []string {
	return func() []string {
		var out []string
		for _, drain := range drains {
			if drain == nil {
				continue
			}
			out = append(out, drain()...)
		}
		return out
	}
}

// resumableEntry captures everything agent.Run needs to continue a
// background job's conversation with a new user turn: the mutated messages
// (agent.Run appends every turn onto the slice it's given, so by the time it
// returns msgs holds the full transcript, not just the seed message), the
// resolved model client (already wrapped via withIsolatedPool/fallback
// resolution — reused as-is, never re-resolved), and the agent.Config used
// for the run.
type resumableEntry struct {
	msgs []connector.Message
	mc   connector.ModelClient
	cfg  agent.Config
}

// resumableMu guards resumable. resumable is never pruned — same known,
// already-documented characteristic as JobRegistry itself (see JobRegistry's
// doc comment): every finished async job/btw conversation that ever ran
// stays in memory for the lifetime of the process, in case something later
// calls "resume" on it. Not treated as a new problem to solve here, just not
// hidden either.
var resumableMu sync.Mutex
var resumable = map[string]resumableEntry{}

// agentRunner implements tools.SubAgentRunner by wrapping agent.Run.
type agentRunner struct{}

// resolveModelClient picks the resolved model client for a subagent.
//
// An explicit "provider/model" override is resolved via the registry. Otherwise
// the subagent inherits the parent's model client from context — which is
// already configured with a valid API key — instead of re-guessing via
// FindModel, whose bare-name lookup iterates the provider map in random order
// and can land on a different (unconfigured) provider that happens to list
// the same model.
func resolveModelClient(ctx context.Context, model string) (connector.ModelClient, error) {
	mc := connector.ModelClientFromContext(ctx)
	if mc == nil {
		// No parent model client in context (e.g. tests) — fall back to lookup.
		if p, m, ok := providers.FindModel(model); ok {
			return p.Client(m), nil
		}
		return nil, fmt.Errorf("no provider available for model %q", model)
	}

	mName := model
	if mName == "" {
		mName = mc.Model()
	}
	if mName == "" {
		return nil, fmt.Errorf("no model specified")
	}
	// Inherit BEFORE interpreting a "/" as a provider separator: model IDs can
	// contain slashes themselves (e.g. openrouter's "stealth/ox-alpha"), so
	// the inherited name must stay whole instead of splitting into a
	// nonexistent "stealth" provider.
	if mName == mc.Model() {
		return mc, nil
	}
	// Any remaining slashed name is treated as an explicit provider/model
	// override. FindModel itself first tries the whole string as a full model
	// ID across providers, so an override naming another provider's slashed
	// model ID resolves to that provider instead of being misread as a prefix;
	// only when nothing lists the full string does the first-slash split apply.
	if strings.Contains(mName, "/") {
		if p, m, ok := providers.FindModel(mName); ok {
			return p.Client(m), nil
		}
		return nil, fmt.Errorf("no provider available for model %q", mName)
	}
	// Bare-name override that differs from the parent's current model:
	// keep the parent's provider (its already-resolved credential), bound to
	// the new model. Never re-guess via FindModel here — its map-order bare
	// lookup could land on a different unconfigured provider listing the same
	// name. The provider must be registered under its own name in the catalog
	// for this lookup to succeed — true for every real provider, each
	// registered exactly once at startup.
	prov, ok := providers.GetProvider(mc.Provider())
	if !ok {
		return nil, fmt.Errorf("provider %q not found", mc.Provider())
	}
	return prov.Client(mName), nil
}

// withIsolatedPool binds mc, and every entry in fallbacks, to ONE HTTP client
// with its own connection pool, so a child agent shares nothing with its
// parent: parent cancellation cannot leak into subagent requests and vice
// versa. Primary and fallback share the pool because within a single child
// run they are never used concurrently — agent.Run tries them one after
// another, never in parallel.
//
// This used to live in tools/subagent.go, which stuffed the client into the
// child's context under an api-package context key. The transport is not
// something the tools package should know about, and the api layer no longer
// reads the context at all; a client now carries its own transport instead.
//
// Etap 5 (docs/architecture-refactor.md) closed a latent gap here: before the
// caller resolved fallbacks, agent/fallback.go pulled a fresh provider from
// the global catalog mid-run, invisibly to this wrapper — a fallback
// triggered inside a child run would have silently fallen back to the shared
// api.defaultClient instead of the child's isolated pool. Now that the
// caller resolves every fallback up front, it can wrap them together with
// the primary in the same call, so the gap cannot reopen without also
// changing this function.
//
// Granularity is otherwise unchanged: agentRunner.run is entered exactly once
// per tools.SubAgentRunner.RunTask/RunTaskWithSystem call, i.e. once per
// runSingleTask, so a parallel subagent(tasks=[a,b,c]) still creates three
// pools — one per child.
//
// A ModelClient that does not implement connector.HTTPInjector (every fake in
// the test suite) is returned untouched and keeps today's "no isolation"
// behavior. That fallback used to be the whole injection path's default
// failure mode, because it ran through three interfaces and a type assertion
// at each hop. It is now a single hop: every client the providers package
// hands out implements connector.HTTPInjector, and providers/client.go asserts
// that at BUILD time, so the assertion below cannot start failing silently
// for production clients.
func withIsolatedPool(mc connector.ModelClient, fallbacks []connector.ModelClient) (connector.ModelClient, []connector.ModelClient) {
	pool := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        2,
			MaxIdleConnsPerHost: 1,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	bind := func(c connector.ModelClient) connector.ModelClient {
		inj, ok := c.(connector.HTTPInjector)
		if !ok {
			return c
		}
		return inj.WithHTTP(pool)
	}
	boundFallbacks := make([]connector.ModelClient, len(fallbacks))
	for i, fb := range fallbacks {
		boundFallbacks[i] = bind(fb)
	}
	return bind(mc), boundFallbacks
}

// RunTask runs a plain subagent (no named agent) with the dedicated subagent
// system prompt.
func (r *agentRunner) RunTask(ctx context.Context, task string, model string, opts tools.SubagentOptions) (string, error) {
	return r.run(ctx, task, model, providers.BuildSubagentSystemPrompt(), opts)
}

// RunTaskWithSystem runs a subagent with a named agent's system prompt.
//
// opts.SystemPromptMode picks how `system` (the definition's markdown body,
// or its frontmatter `system` override — see agentdefs.Def.SystemPrompt) is
// used:
//   - "replace": `system` IS the entire system prompt, verbatim — today's
//     pre-existing behavior, kept for definitions that opt in explicitly and
//     want full control (and accept full responsibility for restating
//     anything they need, e.g. the subagent contract or cwd).
//   - anything else, INCLUDING the empty string: `system` is treated as a
//     ROLE layered on top of the standard subagent prompt via
//     providers.BuildSubagentSystemPromptWithRole, so the agent keeps the
//     subagent contract, environment context, tool descriptions and the
//     project's AGENTS.md.
//
// Append is the default branch (not replace) because replace was the ONLY
// behavior before this switch existed, and it silently strips a named agent
// of the subagent contract and AGENTS.md — actively harmful for an agent
// that writes to the repo. Definitions that omit `system_prompt_mode`
// entirely get agentdefs.Def.SystemPromptMode normalized to "append" already
// (see agentdefs.Parse), so an empty opts.SystemPromptMode here only occurs
// for a caller that bypasses agentdefs — and append is still the safe choice
// for it.
func (r *agentRunner) RunTaskWithSystem(ctx context.Context, task string, model string, system string, opts tools.SubagentOptions) (string, error) {
	if opts.SystemPromptMode == "replace" {
		return r.run(ctx, task, model, system, opts)
	}
	return r.run(ctx, task, model, providers.BuildSubagentSystemPromptWithRole(system), opts)
}

// run executes one subagent turn and normalizes the outcome into a result the
// parent can act on. Any hit on the iteration cap — with or without text — is
// returned as a wrapped tools.ErrSubagentTruncated, so the tools package can
// detect it via errors.Is and surface subagentResult.Truncated /
// ToolResult.Truncated without parsing free-form suffixes.
func (r *agentRunner) run(ctx context.Context, task, model, system string, opts tools.SubagentOptions) (string, error) {
	mc, err := resolveModelClient(ctx, model)
	if err != nil {
		return "", err
	}
	// Resolve the named agent's fallback specs (opts.Fallbacks — "provider/
	// model" strings from its frontmatter, threaded through SubagentOptions
	// by tools/subagent.go). Unlike the top-level agent (see resolveFallbacks
	// in commands.go), a subagent cannot report unresolved specs on stderr:
	// it runs mid-session, frequently under the Bubble Tea TUI, and an
	// unguarded stderr write there corrupts the screen instead of politely
	// wrapping a line. resolveFallbacksQuiet reports nothing itself; any
	// unresolved spec is logged to the debug log (if one is active) and
	// otherwise dropped — a fallback is best-effort by nature, so a typo'd
	// spec must not turn into a hard failure for the whole subagent when the
	// primary model is perfectly capable of running on its own.
	clients, unresolved := resolveFallbacksQuiet(opts.Fallbacks)
	if len(unresolved) > 0 {
		if dl := debug.FromContext(ctx); dl != nil {
			for _, spec := range unresolved {
				_, _ = fmt.Fprintf(dl, "subagent: fallback model %q not found, skipping\n", spec)
			}
		}
	}
	// Whatever resolved (possibly none) still goes through the same wrapper
	// as the primary client, so isolation cannot regress now that fallback
	// support is wired up (see withIsolatedPool's doc comment and
	// TestWithIsolatedPool_WrapsFallbacksWithPrimary).
	mc, fallbacks := withIsolatedPool(mc, clients)

	// Resolve the iteration cap: explicit parent override wins; otherwise the
	// (unlimited) default. Tools.ResolveMaxIter centralizes nil/0/negative
	// semantics so this logic is unit-tested in tools/.
	maxIter := tools.ResolveMaxIter(opts)

	// Drive agent.Run with the streaming Sink tools/subagent.go stashed in
	// ctx (see tools.SubagentSink) so the child's Text/Thinking calls reach
	// the parent TUI's subagent modal live, instead of only surfacing once
	// the whole task finishes. Fall back to a plain, non-forwarding
	// collector when none is present (e.g. tests that call run directly).
	var sink agent.Sink
	var collectedText func() string
	if injected, ok := ctx.Value(tools.SubagentSinkCtxKey{}).(tools.SubagentSink); ok {
		sink = injected
		collectedText = injected.CollectedText
	} else {
		c := &collector{}
		sink = c
		collectedText = func() string { return c.text.String() }
	}
	// opts.History, present when the task set inherit_history: true (see
	// tools/subagent.go's runSingleTask), is the parent's transcript up to
	// this call. session.ForkMessagesWithTurn is the same copy-then-append
	// helper /btw's side-conversation fork uses: a new backing array so
	// nothing this child appends can ever alias the parent's own history,
	// with task appended as the child's own new user turn on top of it.
	// Falls back to the plain single-message seed when there is no history
	// to inherit (the common case, and the only option when the call was
	// made outside a running agent.Run round — see
	// connector.ConversationFromContext's doc comment).
	var msgs []connector.Message
	if len(opts.History) > 0 {
		msgs = session.ForkMessagesWithTurn(opts.History, task)
	} else {
		msgs = []connector.Message{
			{
				Role:    "user",
				Content: []connector.ContentBlock{{Type: "text", Text: task}},
			},
		}
	}

	// jobID is the id this child is running under (JobIDCtxKey — set for
	// every real invocation, sync or async, by tools/subagent.go's spawn;
	// only absent in tests that call run() directly). Read here, before
	// agent.Run, so cfg.NextMessages can be wired to drain this exact job's
	// own mailbox — the "message" tool / "/msg" mechanism's delivery point,
	// mirroring how the main agent's NextMessages drains its pending-input
	// queue (see agent.Config.NextMessages's doc comment). It is also what
	// makes the resume hint further down actionable instead of a dead end.
	jobID, _ := ctx.Value(tools.JobIDCtxKey{}).(string)

	cfg := agent.Config{
		System:        system,
		MaxRetries:    1,
		MaxIterations: maxIter,
		Debug:         false,
		Tools:         &subagentToolRunner{allowed: opts.Tools},
		Schema:        tools.GetSubagentToolsSchemaJSONFor(opts.Tools),
		Fallbacks:     fallbacks,
		Temperature:   opts.Temperature,
		MaxTokens:     opts.MaxTokens,
		NoPromptCache: !agent.PromptCacheEnabled(),
		NextMessages:  tools.JobMailboxNextMessages(jobID),
	}

	// Children spend the parent's money, so they record against the same
	// session ledger (internal/ledger) rather than only reporting usage back
	// inside their tool result. Recorded per model call through a wrapped
	// sink, so a child that runs for ten minutes shows up in the parent's
	// cost figure as it works rather than only when it returns — and a child
	// that dies on its last iteration still accounts for what it spent.
	_, err = agent.Run(ctx, mc, ledger.Watch(sink, ledger.Subagent, mc.Provider(), mc.Model(), jobID), &msgs, cfg)
	text := strings.TrimSpace(collectedText())

	// truncated is the iteration-cap cutoff; deadlineExceeded is the
	// wall-clock one (SubagentTimeoutSec, via ctx's deadline — see
	// tools/subagent.go's runSingleTask). Mutually exclusive in practice:
	// agent.Run only ever returns one error. Both leave a resumable,
	// partially-completed conversation behind, so both get the same
	// treatment below.
	truncated := errors.Is(err, agent.ErrMaxIterations)
	deadlineExceeded := !truncated && errors.Is(err, context.DeadlineExceeded)

	// If this run is happening inside a background job, and it actually
	// produced a usable transcript (finished cleanly, hit the iteration cap,
	// or hit the wall-clock deadline — all leave real turns in msgs, as
	// opposed to a hard failure where agent.Run may have barely started),
	// stash the mutated msgs/mc/cfg so a later "resume" tool call can
	// continue this exact conversation as a brand-new job. See
	// resumableEntry's doc comment for why this map is never pruned.
	if jobID != "" && (err == nil || truncated || deadlineExceeded) {
		resumableMu.Lock()
		resumable[jobID] = resumableEntry{msgs: msgs, mc: mc, cfg: cfg}
		resumableMu.Unlock()
	}

	if truncated || deadlineExceeded {
		return subagentCutoffMessage(text, deadlineExceeded, jobID, maxIter, err)
	}
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", fmt.Errorf("subagent finished without producing any text output")
	}
	// Normal completion (no cutoff): still worth a resume hint, on the same
	// terms as subagentCutoffMessage's — the follow-up question ("and what
	// about X?") is common enough that the parent should know it can hand
	// the work back to this exact child instead of starting a fresh one from
	// scratch. resumeHint itself already no-ops when jobID is empty (no job
	// registry — tests only), so nothing here needs to duplicate that check.
	return text + resumeHint(jobID), nil
}

// subagentCutoffMessage builds the (content, error) pair run() returns once
// a child has been cut off — by the iteration cap or by the wall-clock
// deadline — factored out of run() so this decision can be exercised
// directly with a synthetic text/jobID, independent of whatever agent.Run
// actually manages to produce in a given scenario (in particular, the
// harness's own "possible infinite loop" diagnostic — see agent.Run's
// d.Text call right before it returns ErrMaxIterations — means text is
// essentially never really empty on the iteration-cap path in practice; the
// text == "" branches below exist for correctness regardless).
//
// deadlineWasHit distinguishes the wall-clock case from the iteration-cap
// one for wording only; deadlineErr is the original error to wrap so
// errors.Is(err, context.DeadlineExceeded) still holds for a caller that
// cares (tools/subagent.go's runSingleTask does, via ErrSubagentTimedOut's
// wrapping — see its doc comment).
func subagentCutoffMessage(text string, deadlineWasHit bool, jobID string, maxIter int, deadlineErr error) (string, error) {
	if text == "" {
		// Hit the cutoff and produced nothing to show — but if a resumable
		// entry was just stashed for jobID, the conversation is NOT a dead
		// end: agent.Run still appended whatever partial turns happened
		// before the cutoff, and resume() can pick them up. Only when there
		// is no job id at all (jobID == "", so nothing was stashed) is
		// "narrow the task or split it" the honest answer.
		if deadlineWasHit {
			if jobID != "" {
				return "", fmt.Errorf("subagent exceeded its wall-clock time limit without producing any output, but the conversation so far is resumable: resume(job_id=%q, task=\"...\") to continue it", jobID)
			}
			return "", fmt.Errorf("subagent exceeded its wall-clock time limit without producing any output; narrow the task or split it into smaller subagent calls")
		}
		if jobID != "" {
			return "", fmt.Errorf("subagent hit its %d-iteration limit without producing a final answer, but the conversation so far is resumable: resume(job_id=%q, task=\"...\") to continue it", maxIter, jobID)
		}
		return "", fmt.Errorf("subagent hit its %d-iteration limit without producing a final answer (likely stuck in a tool-call loop); narrow the task or split it into smaller subagent calls", maxIter)
	}
	// Partial: keep the text, annotate it with a resume hint, and wrap a
	// sentinel (ErrSubagentTruncated / ErrSubagentTimedOut) so the tools
	// package can detect it via errors.Is and surface it as a partial
	// success (Truncated=true) rather than a bare failure with the content
	// thrown away.
	if deadlineWasHit {
		return text + fmt.Sprintf("\n\n[note: subagent exceeded its wall-clock time limit; the result above may be incomplete.%s]", resumeHint(jobID)),
			fmt.Errorf("%w: result may be incomplete: %w", tools.ErrSubagentTimedOut, deadlineErr)
	}
	return text + fmt.Sprintf("\n\n[note: subagent stopped at its %d-iteration limit; the result above may be incomplete.%s]", maxIter, resumeHint(jobID)),
		fmt.Errorf("%w: stopped at its %d-iteration limit; result may be incomplete", tools.ErrSubagentTruncated, maxIter)
}

// resumeHint is appended to a child's result text — either inside the
// "[note: ...]" subagentCutoffMessage builds when a child was cut off
// (iteration cap or wall-clock deadline), or directly onto the plain text of
// a child that finished normally (see run()'s success return above) — so
// both cases advertise the same "resume(job_id=...)" escape hatch in the
// same words. Without the job id, "use resume" is advice the model cannot
// act on — ResumeTool.Run (tools/resume.go) requires job_id as input, and
// this text is the only place that id ever reaches the model in the
// blocking-subagent path. Empty when no job id is available (e.g. no job
// registry wired — tests only; every real invocation has one, per
// tools/subagent.go's spawn/jobStarter wiring).
func resumeHint(jobID string) string {
	if jobID == "" {
		return ""
	}
	return fmt.Sprintf(" Continue this exact conversation (it still has its full context, nothing needs restating) with resume(job_id=%q, task=\"...\").", jobID)
}

// subagentToolRunner wraps the global tool registry so subagents can use tools.
type subagentToolRunner struct {
	// allowed, when non-empty, restricts which tools this child may actually
	// invoke (from a named agent's frontmatter `tools:` list). The schema
	// passed to the model (tools.GetSubagentToolsSchemaJSONFor) is only a
	// hint — a model can still emit a call for a tool it wasn't offered
	// (stale cached tool list, hallucinated name, etc.) — so this is the
	// real enforcement point. Empty/nil means no restriction (today's
	// behavior): every tool except "subagent" is allowed.
	allowed []string
}

func (r *subagentToolRunner) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	if name == "subagent" {
		return "", fmt.Errorf("subagent tool is not available to subagents (recursion denied)")
	}
	// tools.AllowOnlySubagent is the single source of truth for what a
	// whitelisted child may call: it mirrors
	// tools.GetSubagentToolsSchemaJSONFor tool for tool — same
	// alwaysAllowedTools (help, lua) folded in, same subagentDeniedTools
	// ("subagent", "agents") dropped even when explicitly listed — so a
	// call permitted here is always one the schema offered the model, and a
	// call the schema never offered is always refused here.
	//
	// Checking the call against a hand-rolled loop here — as this used to
	// do — meant a whitelisted child was offered "help" in its schema but
	// refused when it actually called it, because the loop only ever
	// consulted r.allowed verbatim: neither alwaysAllowedTools nor
	// subagentDeniedTools entered into it.
	var gate tools.ToolGate
	if len(r.allowed) > 0 {
		gate = tools.AllowOnlySubagent(r.allowed)
		if err := gate(name); err != nil {
			return "", err
		}
	}
	// The check above only covers a whitelisted child, and only covers
	// calls the model makes directly. A tool can dispatch further tools
	// itself — the "lua" tool exists to do exactly that — and those go
	// straight to tools.RunTool, below this function. Carrying the
	// restriction in the context makes both gaps covered too:
	//
	//   - tools.DenySubagentRecursion denies every subagentDeniedTools
	//     entry ("subagent", "agents") unconditionally, so even an
	//     UNRESTRICTED child (no tools: whitelist at all, gate == nil
	//     above) cannot reach a tool GetSubagentToolsSchemaJSON never
	//     offered it — whether the call is direct or made from inside a
	//     lua script. Denying only "subagent" here used to leave "agents"
	//     reachable by exactly that path.
	//   - the whitelisted gate, when non-nil, applies to nested
	//     dispatches too, so a script cannot reach a tool its agent was
	//     denied, or spawn a grandchild.
	ctx = tools.WithToolGate(ctx, tools.DenySubagentRecursion())
	if gate != nil {
		ctx = tools.WithToolGate(ctx, gate)
	}

	res := tools.RunTool(ctx, name, args)
	if res.Success {
		return res.Content, nil
	}
	return res.Content, fmt.Errorf("%s", res.Error)
}

// wireTools installs the composition-root wiring the tool registry needs to
// actually run subagents/wait/jobs against the app's shared JobRegistry and
// jobEventBus: tools.SetSubAgentRunner/SetJobWaiter/SetJobStarter and
// JobRegistry's onEvent hook. Extracted from main() so integration tests can
// call the EXACT same wiring code main() calls (see wiring_test.go) rather
// than a hand-rolled reimplementation that could drift from production.
// Idempotent: calling it again (e.g. after swapping JobRegistry/jobEventBus
// for test isolation) simply re-points everything at the current globals,
// with no duplicate event delivery — SetOnEvent replaces the previous hook,
// it does not add to it.
func wireTools() {
	// Register the subagent runner so the "subagent" tool (advertised in the
	// tool schema) is actually executable. Without this, RunTool returns
	// "unknown tool: subagent".
	tools.SetSubAgentRunner(&agentRunner{})

	// Wire the "wait" tool and the "subagent" tool's async spawn path to the
	// app's one shared job registry (see btw.go) so job_id polling works
	// against real background jobs — /btw side-conversations and async
	// subagents alike — instead of each running on its own registry.
	tools.SetJobWaiter(jobWaiterAdapter{reg: JobRegistry})
	tools.SetJobStarter(jobStarterAdapter{reg: JobRegistry})
	tools.SetJobAsker(jobAskerAdapter{reg: JobRegistry})
	tools.SetJobAnswerer(jobAnswererAdapter{reg: JobRegistry})
	tools.SetJobProgressReporter(jobProgressAdapter{reg: JobRegistry})
	tools.SetJobActivityToucher(jobActivityToucherAdapter{reg: JobRegistry})
	tools.SetJobMailbox(jobMailboxAdapter{reg: JobRegistry})
	tools.SetJobResumer(jobResumerAdapter{reg: JobRegistry})
	// kill_job's subagent path + its inside-a-child subtree check (see
	// tools/killjob.go).
	tools.SetJobCanceler(jobCancelerAdapter{reg: JobRegistry})
	tools.SetJobLister(listJobsAdapter{reg: JobRegistry})

	// Wire JobRegistry's status-change events onto jobEventBus so the TUI
	// (see tuiCmd in commands.go) can show a live background-jobs panel. A
	// no-op for every other mode, which never calls TUI.SetJobEventBus.
	JobRegistry.SetOnEvent(func(j jobs.Job) {
		jobEventBus.Publish("job.updated", j)

		// A job that called "ask_parent" is now blocked, and it stays blocked until
		// someone calls "answer_job" or its wall-clock limit expires — at which
		// point everything it had done is thrown away. Relying on the parent
		// to poll for that is not good enough: it has no reason to suspect a
		// question is pending, and a model that forgets to poll silently
		// wastes the whole child run. So the question is pushed into the
		// parent's next turn (and wakes an idle REPL) the same way a finished
		// background command is.
		if j.Status == jobs.StatusWaitingAnswer {
			JobNotices.Notify(fmt.Sprintf(
				"[background job] %s is BLOCKED waiting for an answer: %q (job_id=%s)\n"+
					"Relay this question to the user in your reply, wait for their answer in the conversation, then deliver it — do not invent an answer on their behalf. "+
					"Only call answer_job(job_id=%q, text=\"...\") yourself if you already genuinely know the answer. "+
					"Until it is answered it makes no progress, and its work is discarded when it times out.",
				j.Description, j.Question, j.ID, j.ID))
		}
	})

	// Wire background-command completion notices to the shared queue. This is
	// only half the story: a notice is queued from here, but whether anything
	// consumes it depends on the mode wiring up JobNotices.Drain /
	// JobNotices.Signal — which is exactly why backgrounding itself stays off
	// until a mode opts in via tools.SetBackgroundBashEnabled.
	tools.SetJobNotifier(JobNotices)
}

func main() {
	wireTools()

	// Unpack the builtin agent definitions (internal/agentdefs/builtin) into
	// ~/.tyci/agents/ so tyci is useful with zero setup. This runs on every
	// invocation, including `tyci --help`, so it must stay cheap and silent:
	// Sync's steady-state cost is a handful of file stats/reads plus a few
	// sha256 sums over KB-sized files, and any error is swallowed rather than
	// reported. Reporting it would mean deciding where to put the message —
	// stderr corrupts `--print`/piped output and the TUI, and there is no
	// display object here yet to route it through — for a background
	// convenience step whose failure never blocks anything the user asked
	// for. `tyci agent sync` (commands.go) is the one place this is loud.
	_, _ = agentdefs.Sync(agentdefs.GlobalDir(), false)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
