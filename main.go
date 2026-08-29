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

// residualMailboxSweepCap bounds how many of a finished job's leftover
// mailbox entries (see jobs.Job.ResidualMailbox) get forwarded to the main
// queue individually — batch-2 review round 2 finding D5. A handful is a
// genuinely useful, readable heads-up; dozens (an orphaned fork with
// several background bash jobs each posting periodic progress notices
// into it) would just flood the queue. Past this many, the remainder is
// summarized as a count instead of shown one by one.
const residualMailboxSweepCap = 5

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
//
// cfg.NextMessages is a closure bound to the specific job id that was
// running when this entry was stashed (see tools.JobMailboxNextMessages) —
// it is NOT safe to reuse verbatim for a different job id (see
// jobResumerAdapter.Resume in btw.go, which rebinds a fresh local copy of
// cfg rather than mutating the entry stored here). An entry is looked up
// once and then may be resumed more than once — Resume never deletes it —
// so nothing in this map or its cfg must ever be mutated in place; every
// consumer must copy before changing anything.
type resumableEntry struct {
	msgs []connector.Message
	mc   connector.ModelClient
	cfg  agent.Config
	// todoAgentID is whichever agent id the stashed run's own todo(...)
	// calls actually landed under (tools.TodoAgentIDFromContext of the
	// context that run used) — NOT necessarily the job id: an ordinary
	// subagent gets its own TodoAgentCtxKey id, distinct from and taking
	// priority over its job id, while a /btw job or a chained resume has
	// only JobIDCtxKey set and so is keyed by job id after all. Recorded at
	// stash time so a later `resume` can copy the right list forward (see
	// jobResumerAdapter.Resume, tools.CopyTodoListForResume) instead of
	// guessing.
	todoAgentID string

	// depth is the nesting depth (tools.DepthFromContext) the stashed run
	// actually executed at — captured at stash time the same way
	// todoAgentID is, so a later resume can restore it via tools.WithDepth
	// instead of the resumed run silently defaulting back to depth 0. A
	// resumed depth-1 subagent's stashed cfg.Schema (built once at spawn
	// time by agentRunner.run, see depth/GetSubagentToolsSchemaJSONForAtDepth
	// there) still offers "scout" — correct for depth 1 — but without this,
	// the runtime gate on resume would see depth 0 (ctx never re-stamped)
	// and refuse it, the same schema/gate mismatch class as findings 2/4.
	// Zero value (0) is the right default for every stash site that never
	// sets it explicitly (e.g. this package's own tests): depth 0 is what
	// tools.DepthFromContext already returns for a context nobody ever
	// wrapped with WithDepth, so an old/test-built entry with no explicit
	// depth behaves exactly as before this field existed.
	depth int
}

// resumableMu guards resumable and resumableOrder.
//
// Lock ordering (load-bearing, keep it this way): pruneResumableLocked calls
// JobRegistry.IsLive while already holding resumableMu, i.e. resumableMu is
// always acquired BEFORE JobRegistry's own r.mu, never the other way round.
// This is safe today because nothing inside package jobs ever calls back
// into main while holding r.mu: onEvent always fires after r.mu is released
// (see registry.go's Start completion path and Cancel/CancelAll), so there
// is no path back into this package's resumableMu while jobs' lock is held.
// If that ever changes — an onEvent callback taking r.mu while it runs, or a
// callback invoked WITH r.mu held — this ordering would deadlock against any
// code that takes the locks in the other order. Do not acquire JobRegistry's
// lock (directly or via a Registry method) while already holding it in the
// other order anywhere else in this package.
//
// resumable USED to be never pruned at all: every finished async job/btw
// conversation that ever ran stayed in memory for the lifetime of the
// process, on the theory that something might later call "resume" on it.
// Each entry holds a full transcript ([]connector.Message, unbounded) plus a
// live ModelClient — heavier than a jobs.Registry entry's plain Result
// string — so an unbounded map here is worse than the same problem
// JobRegistry already solved for itself (see MaxRetainedTerminalJobs).
// resumableCap gives this map the same discipline, via stashResumable/
// pruneResumableLocked below.
var resumableMu sync.Mutex
var resumable = map[string]resumableEntry{}

// resumableOrder is the FIFO insertion order backing pruneResumableLocked's
// eviction — resumable itself is a map and remembers no ordering. Guarded
// by resumableMu, same as resumable.
var resumableOrder []string

// resumableCap bounds how many resumable entries stay in memory at once.
// 200 — 4x jobs.MaxRetainedTerminalJobs (50) — because a resumable entry is
// looked up by an explicit resume(job_id=...) call, which by nature happens
// well after the fact (unlike wait(job_id=...), which every async spawn's
// own turn is nudged toward calling promptly). A resumed job stashes its OWN
// new entry as terminal id, so a chain of resumes naturally produces one
// entry per hop; 200 gives a long-running session room for many such chains
// before the oldest, least-likely-to-be-revisited entries start being
// dropped.
const resumableCap = 200

// stashResumable records entry under jobID, the single place every
// (re-)stash of a resumable conversation goes through — agentRunner.run's
// initial stash and every resume/fork/promotion re-stash alike — so the cap
// below applies uniformly instead of only to some call sites.
func stashResumable(jobID string, entry resumableEntry) {
	resumableMu.Lock()
	defer resumableMu.Unlock()
	if _, exists := resumable[jobID]; !exists {
		resumableOrder = append(resumableOrder, jobID)
	}
	resumable[jobID] = entry
	pruneResumableLocked()
}

// pruneResumableLocked drops the oldest entries past resumableCap, in
// insertion order, skipping (and keeping) any whose job is still live —
// running OR waiting_answer; a job blocked on a question is still live and
// must never have its resumable transcript evicted out from under it.
// JobRegistry.IsLive reads Job.Status under the registry's own lock, unlike
// JobRegistry.Get (which hands back the live *Job pointer with no lock held
// on return — reading its Status field afterwards races against Start's
// completion path writing it). Every resumableEntry is stashed for an
// already terminal job (see resumableEntry's doc comment), so in practice
// this guard should never trigger — it exists so a fix elsewhere that
// stashes early cannot silently turn into "prune a job's only resumable
// copy while it is still in flight". Caller must hold resumableMu; see this
// var block's doc comment above for why calling into JobRegistry while
// holding resumableMu is safe.
func pruneResumableLocked() {
	excess := len(resumable) - resumableCap
	if excess <= 0 {
		return
	}
	kept := make([]string, 0, len(resumableOrder))
	removed := 0
	for _, id := range resumableOrder {
		if removed < excess {
			if JobRegistry.IsLive(id) {
				kept = append(kept, id)
				continue
			}
			delete(resumable, id)
			removed++
			continue
		}
		kept = append(kept, id)
	}
	resumableOrder = kept
}

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
	// opts.Tools is the agent's `tools:` whitelist (empty/nil means
	// unrestricted); tools.IsSubagentDenied("ask_parent") is always false, so
	// hasAskParent is true unless a non-empty whitelist explicitly omits it.
	// See F22 in TODO.md: the prompt must not claim ask_parent is real for an
	// agent that was never actually given it.
	hasAskParent := len(opts.Tools) == 0
	for _, name := range opts.Tools {
		if name == "ask_parent" {
			hasAskParent = true
			break
		}
	}
	return r.run(ctx, task, model, providers.BuildSubagentSystemPromptWithRole(system, hasAskParent), opts)
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

	// Subagent iteration limits are accepted in SubagentOptions for API
	// compatibility, but child runs are intentionally unlimited. The only
	// ordinary stop signals remain context cancellation (including kill_job).

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

	// depth is this child's own nesting depth, stashed on ctx by
	// runSingleTask (tools/subagent.go) via tools.WithDepth right next to
	// SubagentSinkCtxKey — top level is depth 0, so every subagent's own
	// child lands here at depth >= 1. Both the schema below and
	// subagentToolRunner.Run's runtime gate read the SAME
	// tools.AllowedDelegationTool/ToolAllowedAtDepth helpers for the same
	// depth, so a delegation tool offered here is always one the gate
	// will actually let this child call.
	depth := tools.DepthFromContext(ctx)

	// opts.ScoutMode picks the schema builder: a scout's schema comes from
	// tools.ScoutSchemaJSONForDepth (ScoutGate's own tool list, no
	// alwaysAllowedTools "lua" folded in), never
	// GetSubagentToolsSchemaJSONForAtDepth, which folds lua into every
	// ordinary subagent's schema regardless of its tools: whitelist — see
	// SubagentOptions.ScoutMode's doc comment (tools/tool.go).
	schema := tools.GetSubagentToolsSchemaJSONForAtDepth(opts.Tools, depth)
	if opts.ScoutMode {
		schema = tools.ScoutSchemaJSONForDepth(depth)
	}

	cfg := agent.Config{
		System:     system,
		MaxRetries: 1,
		// opts.MaxIterationsCap is 0 (unlimited) for every ordinary
		// subagent — only tools/scout.go ever sets it, to 15. Plain
		// MaxIterations stays deliberately unpopulated; see its doc
		// comment in tools/tool.go.
		MaxIterations: opts.MaxIterationsCap,
		Debug:         false,
		Tools:         &subagentToolRunner{allowed: opts.Tools, scoutMode: opts.ScoutMode},
		Schema:        schema,
		Fallbacks:     fallbacks,
		Temperature:   opts.Temperature,
		MaxTokens:     opts.MaxTokens,
		NoPromptCache: !agent.PromptCacheEnabled(),
		NextMessages:  tools.JobMailboxNextMessages(jobID),
	}

	// Item 15: nudge this child, at most once per SubagentBackgroundAfterSec,
	// to post a report_progress note when it has gone quiet. Only wired when
	// report_progress is actually reachable for this agent — report_progress
	// is NOT in alwaysAllowedTools (tools/toolgate.go), so a non-empty
	// tools: whitelist that omits it (e.g. builtin "reviewer": find, read,
	// bash, or "locator": find, read) leaves the child with no way to ever
	// satisfy this nudge: LastProgressAt would never advance, and the
	// reminder would re-fire roughly every SubagentBackgroundAfterSec for
	// the rest of the run — exactly the "crowd out the real conversation"
	// outcome item 15 exists to avoid. Same hasAskParent-shaped check as
	// RunTaskWithSystem above (opts.Tools empty/nil means unrestricted).
	hasReportProgress := len(opts.Tools) == 0
	for _, name := range opts.Tools {
		if name == "report_progress" {
			hasReportProgress = true
			break
		}
	}
	if hasReportProgress {
		cfg.ProgressHeartbeat = tools.JobProgressHeartbeatCheck(jobID)
	}

	// Children spend the parent's money, so they record against the same
	// session ledger (internal/ledger) rather than only reporting usage back
	// inside their tool result. Recorded per model call through a wrapped
	// sink, so a child that runs for ten minutes shows up in the parent's
	// cost figure as it works rather than only when it returns — and a child
	// that dies on its last iteration still accounts for what it spent.
	//
	// A scout is recorded under ledger.Scout rather than ledger.Subagent so
	// its cost is countable on its own (see ledger.Kind's doc comment) —
	// same jobID either way, so UsageByJob's per-child tracking is unaffected.
	kind := ledger.Subagent
	if opts.ScoutMode {
		kind = ledger.Scout
	}
	_, err = agent.Run(ctx, mc, ledger.Watch(sink, kind, mc.Provider(), mc.Model(), jobID), &msgs, cfg)
	text := strings.TrimSpace(collectedText())

	// stoppedByUser is the kill switch (jobs.Registry.Cancel, reached today
	// only via kill_job). Mutually exclusive in practice: agent.Run only ever
	// returns one error. All leave a resumable, partially-completed
	// conversation behind, so all get the same treatment below.
	truncated := errors.Is(err, agent.ErrMaxIterations)
	deadlineExceeded := !truncated && errors.Is(err, context.DeadlineExceeded)
	stoppedByUser := isStoppedByUser(err, ctx)

	// If this run is happening inside a background job, and it actually
	// produced a usable transcript (finished cleanly, hit the iteration cap,
	// hit the wall-clock deadline, or was stopped by kill_job — all leave
	// real turns in msgs, as opposed to a hard failure where agent.Run may
	// have barely started), stash the mutated msgs/mc/cfg so a later "resume"
	// tool call can continue this exact conversation as a brand-new job. See
	// stashResumable's doc comment for the (bounded) retention policy.
	if jobID != "" && (err == nil || truncated || deadlineExceeded || stoppedByUser) {
		stashResumable(jobID, resumableEntry{msgs: msgs, mc: mc, cfg: cfg, todoAgentID: tools.TodoAgentIDFromContext(ctx), depth: depth})
	}

	if stoppedByUser {
		// Killed mid-flight: whatever text exists is real partial work. Wrap
		// the sentinel so runSingleTask (errors.Is) can surface it as a
		// partial success with Truncated=true instead of a bare failure,
		// exactly like the deadline path below. The stash above already
		// happened, so a resume(job_id=...) hint here is actionable.
		return subagentStoppedMessage(text, jobID)
	}
	if truncated || deadlineExceeded {
		// agentRunner configures an ORDINARY child run without a
		// MaxIterations cap or subagent-specific deadline, so
		// tools.DefaultSubagentMaxIterations (-1) is the right number to
		// report for one of those — legacy cutoff normalization for
		// externally supplied contexts and compatibility with older
		// runners. A scout (tools/scout.go) is the one caller that DOES
		// set a real cap via opts.MaxIterationsCap, and truncated here can
		// only mean IT was hit (cfg.MaxIterations came straight from that
		// field above) — report the real number instead of the sentinel,
		// or a truncated scout's cutoff message would nonsensically read
		// "hit its -1-iteration limit".
		maxIter := tools.DefaultSubagentMaxIterations
		if cfg.MaxIterations > 0 {
			maxIter = cfg.MaxIterations
		}
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

// subagentStoppedMessage builds the (content, error) pair run() returns when
// a child was stopped by kill_job (context.Canceled unwrapped from the
// registry's Cancel) — factored out like subagentCutoffMessage so it can be
// exercised directly with synthetic input. The partial text is kept and
// annotated; ErrSubagentStoppedByUser is wrapped so tools/subagent.go's
// runSingleTask detects it via errors.Is and surfaces a partial success
// (Truncated=true) rather than discarding everything.
func subagentStoppedMessage(text string, jobID string) (string, error) {
	if text == "" {
		if jobID != "" {
			return "", fmt.Errorf("%w before producing any output, but the conversation so far is resumable: resume(job_id=%q, task=\"...\") to continue it", tools.ErrSubagentStoppedByUser, jobID)
		}
		return "", fmt.Errorf("%w without producing any output", tools.ErrSubagentStoppedByUser)
	}
	return text + fmt.Sprintf("\n\n[note: subagent was stopped by user (kill_job); the result above may be incomplete.%s]", resumeHint(jobID)),
		fmt.Errorf("%w: result may be incomplete", tools.ErrSubagentStoppedByUser)
}

// isStoppedByUser reports whether an agent.Run error is a kill_job stop (the
// switch item 26 ships). A bare context.Canceled is NOT sufficient: in the
// no-handoff mode (tyci run / --print) runWithHandoff cancels still-running
// children itself via cancelRemaining, and that path stamps AskUnroutableCtxKey
// on the child's context (see its doc in tools/ask.go). Those children stop
// because the parent's tool call was cut off, not because anyone stopped
// them — labelling them "stopped by user (kill_job)" would be a lie. In every
// other mode the child's context is detached at spawn (WithoutCancel), so the
// only context.Canceled a child can return is the registry Cancel — a genuine
// kill_job stop. Factored out so the attribution decision is testable on its
// own, independent of a full agent harness.
func isStoppedByUser(err error, ctx context.Context) bool {
	noHandoff, _ := ctx.Value(tools.AskUnroutableCtxKey{}).(bool)
	return errors.Is(err, context.Canceled) && !noHandoff
}

// subagentCutoffMessage builds the (content, error) pair run() returns once
// a child has been cut off — by the iteration cap or by the wall-clock
// deadline — factored out of run() so this decision can be exercised
// directly with a synthetic text/jobID. The legacy cutoff arguments remain
// for compatibility with callers that may still report an externally imposed
// deadline or iteration cap; built-in subagent runs no longer create either.
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
	// real enforcement point. Empty/nil means no restriction: every tool
	// except "subagent" is allowed (and, at depth 1-3 only, "scout" — see
	// Run's depth check below, which decides both regardless of what
	// allowed says). Ignored entirely when scoutMode is true.
	allowed []string

	// scoutMode, when true, makes Run enforce tools.ScoutGate() instead of
	// tools.AllowOnlySubagent(allowed) for every non-delegation tool call —
	// see SubagentOptions.ScoutMode's doc comment (tools/tool.go) for why
	// AllowOnlySubagent must never be the enforcement path for a scout (it
	// unconditionally folds "lua" back in, which a scout must never have).
	scoutMode bool
}

func (r *subagentToolRunner) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	// Which of "subagent"/"scout" (if either) this child may reach is a
	// property of its nesting depth alone (tools.AllowedDelegationTool),
	// checked here BEFORE the whitelist gate below — this child is always
	// at depth >= 1 (subagentToolRunner.Run only ever runs a real child's
	// own tool calls), so "subagent" is always denied here exactly like
	// before this item existed. "scout" is new: a depth 1-3 child may call
	// it even though it is deliberately never present in that child's own
	// tools: whitelist, including scout's own (see scoutToolProfile's doc
	// comment in tools/scout.go) — depth decides this, not the whitelist,
	// so the whitelist-membership check further down must be skipped for
	// these two names rather than asked to agree with a list that was
	// never meant to mention them.
	depth := tools.DepthFromContext(ctx)
	isDelegationTool := name == "subagent" || name == "scout"
	if isDelegationTool && !tools.ToolAllowedAtDepth(depth, name) {
		if name == "subagent" {
			return "", fmt.Errorf("subagent tool is not available to subagents (recursion denied)")
		}
		return "", fmt.Errorf("%s", tools.DelegationDepthError(depth, name))
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
	switch {
	case isDelegationTool:
		// Handled entirely by the depth check above: neither r.allowed
		// nor r.scoutMode's ScoutGate ever lists "subagent"/"scout"
		// themselves (see this func's doc comment above and
		// scoutToolProfile's doc comment in tools/scout.go), so asking
		// either gate about these two names would only ever refuse a call
		// the depth check just approved.
	case r.scoutMode:
		gate = tools.ScoutGate()
		if err := gate(name); err != nil {
			return "", err
		}
	case len(r.allowed) > 0:
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
	// runWithHandoff's watcher and handoff-message peek go through
	// WaitObserve, not Wait — see JobObserver's doc comment (batch-2 review
	// finding C1) for why counting them as ordinary "wait" callers would
	// suppress the only delivery a question had.
	tools.SetJobObserver(jobObserverAdapter{reg: JobRegistry})
	tools.SetJobStarter(jobStarterAdapter{reg: JobRegistry})
	tools.SetJobAsker(jobAskerAdapter{reg: JobRegistry})
	tools.SetJobExtensionRequester(jobExtensionRequesterAdapter{reg: JobRegistry})
	tools.SetJobAnswerer(jobAnswererAdapter{reg: JobRegistry})
	tools.SetJobProgressReporter(jobProgressAdapter{reg: JobRegistry})
	tools.SetJobActivityToucher(jobActivityToucherAdapter{reg: JobRegistry})
	tools.SetJobProgressHeartbeat(jobProgressHeartbeatAdapter{reg: JobRegistry})
	tools.SetJobMailbox(jobMailboxAdapter{reg: JobRegistry})
	tools.SetJobResumer(jobResumerAdapter{reg: JobRegistry})
	tools.SetJobPromoter(btwPromotionAdapter{})
	// kill_job's subagent path + its inside-a-child subtree check (see
	// tools/killjob.go).
	tools.SetJobCanceler(jobCancelerAdapter{reg: JobRegistry})
	tools.SetJobLister(listJobsAdapter{reg: JobRegistry})

	// Wire JobRegistry's status-change events onto jobEventBus so the TUI
	// (see tuiCmd in commands.go) can show a live background-jobs panel. A
	// no-op for every other mode, which never calls TUI.SetJobEventBus.
	// Captured as locals, not read from the package globals inside the
	// closure below: wireTools can be called again later (tests swap
	// jobEventBus/JobNotices for isolation — see withTestWiring) to point a
	// FUTURE registry's events at a FUTURE bus/notifier, but a job started
	// on THIS JobRegistry, right now, must always report to THIS bus and
	// THIS notifier — the ones actually wired in below — no matter what
	// the globals get reassigned to later while that job is still running.
	// Reading the globals at event-fire time instead of at wiring time let
	// a job finished on one test's registry deliver its completion event
	// into whatever jobEventBus/JobNotices the NEXT test had already
	// swapped in by then (Start's completion goroutine closes job.done,
	// then calls onEvent — see jobs/registry.go — so a test that only
	// waits on job.done can already have moved on and rewired the globals
	// by the time onEvent actually runs).
	bus, notices, reg := jobEventBus, JobNotices, JobRegistry
	JobRegistry.SetOnEvent(func(j jobs.Job) {
		bus.Publish("job.updated", j)

		// A job that called "ask_parent" is now blocked, and it stays blocked until
		// someone calls "answer_job" or its wall-clock limit expires — at which
		// point everything it had done is thrown away. Relying on the parent
		// to poll for that is not good enough: it has no reason to suspect a
		// question is pending, and a model that forgets to poll silently
		// wastes the whole child run. So the question is pushed into the
		// parent's next turn (and wakes an idle REPL) the same way a finished
		// background command is.
		//
		// j.QuestionHasWaiter (B7) is true when some caller was already
		// blocked inside jobs.Registry.Wait — specifically Wait, never
		// WaitObserve — for this exact job the moment this question was
		// posed. Only the "wait" tool goes through Wait: a blocking
		// subagent call's own handoff watch (tools/subagent.go's
		// watchForWaiting) deliberately goes through WaitObserve instead,
		// since it only wakes an unrelated select and never itself
		// reports the question to anyone (batch-2 review finding C1 — see
		// jobs.Registry.WaitObserve's doc comment). A genuine Wait caller
		// is about to receive the same question back as its own,
		// synchronous Wait/wait() result (see JobStatus.Waiting), so
		// queuing this notice too would deliver the same question twice in
		// one turn. Skip it in that case; the notice path stays the
		// authoritative (and only) one otherwise.
		if j.Status == jobs.StatusWaitingAnswer && !j.QuestionHasWaiter {
			text := fmt.Sprintf(
				"[background job] %s is BLOCKED waiting for an answer: %q (job_id=%s)\n"+
					"Relay this question to the user in your reply, wait for their answer in the conversation, then deliver it — do not invent an answer on their behalf. "+
					"Only call answer_job(job_id=%q, text=\"...\") yourself if you already genuinely know the answer. "+
					"Until it is answered it makes no progress, and its work is discarded when it times out.",
				j.Description, j.Question, j.ID, j.ID)

			// B4: address this to whoever spawned j (j.ParentID), not
			// unconditionally to the main queue — a job spawned from an
			// independent fork (a /btw side-conversation, or a subagent
			// nested inside one) must notify that fork, never the main
			// conversation it must never touch (see btwConfig's doc
			// comment in btw.go). reg.Post delivers into the parent job's
			// own mailbox, drained at its next agent-loop iteration the
			// same way "message"/"/msg" already work; it returns false
			// when parentID is unknown or that job has already finished.
			// Design choice for that case: forward to main rather than
			// drop the notice silently — a dropped notice leaves no trace
			// that a child ever asked anything, while a forwarded one at
			// least reaches someone, tagged as not its original addressee.
			// TODO(item 54 review finding 2): the reg.Post branch below is NOT
			// covered by the MarkQuestionShown dedup — that only suppresses a
			// duplicate on the main JobNotifier queue (the j.ParentID == ""
			// case). A mid-level subagent (one with a live ParentID) whose
			// blocking call hands a child off still gets this same ask-notice
			// duplicated in its own mailbox exactly as before item 54, since
			// nothing here checks or records "already shown" against a
			// mailbox-routed message. Fixing it properly needs the same key
			// (jobID+QuestionSeq) threaded through jobs.Job's
			// mailbox/Post/DrainMessages path, which item 54 did not have
			// time to do — see the PR discussion for triage.
			if j.ParentID == "" || !reg.Post(j.ParentID, text) {
				if j.ParentID != "" {
					text = fmt.Sprintf("[for job %s, which has already finished — forwarded here instead] %s", j.ParentID, text)
				}
				// NotifyQuestion (not plain Notify): keyed by j.ID/j.QuestionSeq
				// — an unforgeable per-ask id, not the question text itself,
				// so an identically-worded LATER ask from the same job is
				// never mistaken for this one (item 54 review finding 1) — so
				// a blocking subagent call's handoff message, if it ends up
				// carrying this exact ask too (see tools/subagent.go's
				// handOff/markQuestionsShown), can mark this entry shown and
				// Drain will not repeat it.
				notices.NotifyQuestion(j.ID, j.QuestionSeq, text)
			}
		}

		// C3 (batch-2 review): a message sitting in a job's mailbox when
		// that job goes terminal will never be drained by anyone — its own
		// agent loop has stopped for good, and nothing else ever reads
		// that mailbox (see jobs.Job.ResidualMailbox's doc comment). That
		// includes a notice this very hook routed there via reg.Post above,
		// on some earlier event, for a fork that then finished before its
		// own next iteration got around to draining it. Before this swept
		// it to main, tagged, it simply vanished — silently dropping a
		// notice is exactly the failure this whole notify-routing design
		// exists to avoid.
		//
		// Capped (batch-2 review round 2 finding D5): an orphaned fork
		// that had several background bash jobs each posting periodic
		// progress notices into its mailbox would otherwise dump every one
		// of them into main at once. Still far better than vanishing, but
		// past residualMailboxSweepCap this says how many were left out
		// instead of flooding the queue with all of them individually.
		if n := len(j.ResidualMailbox); n > 0 {
			shown := j.ResidualMailbox
			if n > residualMailboxSweepCap {
				shown = j.ResidualMailbox[:residualMailboxSweepCap]
			}
			for _, m := range shown {
				notices.Notify(fmt.Sprintf("[for job %s, which finished before this could be delivered to it — forwarded here instead] %s", j.ID, m))
			}
			if remaining := n - len(shown); remaining > 0 {
				notices.Notify(fmt.Sprintf("[for job %s] and %d more queued message(s) that finished before delivery — not shown, not delivered", j.ID, remaining))
			}
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
