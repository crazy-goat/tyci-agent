package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/decodo/tyci/connector"
)

// scout: a deliberately crippled subagent, item 21 ("Grandchildren").
//
// A general subagent must never nest — jobs, async spawn, worktree
// isolation and named agents all break in user-visible or dangerous ways
// the moment a child of a child can create more of them (see
// AllowedDelegationTool's doc comment in toolgate.go, and the blockers item
// 21 in TODO.md lists for lifting that restriction generally). But a
// narrow, read-only, synchronous lookup that cannot detach and cannot
// outlive its caller is safe at any of the few extra levels this allows,
// for the same reasons at every level:
//
//   - no job of its own: it runs via runSingleTask directly, never through
//     spawn/runAsync, so the flat notice/answer channel, PendingLines and
//     the 50-job retention list never see it, and it can never be the
//     subject of a stale notice sent to the wrong ancestor.
//   - synchronous and uncancellable-by-detach: it dies with its caller,
//     because it IS its caller's own call stack, not a goroutine handed
//     off to a registry.
//   - inherits the caller's remaining deadline (capped at 180s) instead of
//     resetting one, so a chain of these converges on its own instead of
//     nesting 180s inside 180s inside 600s.
//   - MaxIterations capped at 15, not the unlimited default every other
//     subagent gets.
//   - a fixed, narrow tool profile (scoutToolProfile below) that cannot
//     write or edit anything.
//
// What actually bounds the cost once depth reaches its limit is the
// concurrency semaphore below, not the depth cap itself — see
// AllowedDelegationTool's doc comment.
type ScoutTool struct {
	Runner SubAgentRunner
}

func (t *ScoutTool) Name() string { return "scout" }

// scoutMaxIterations is item 21's fixed cap: "not the unlimited default".
const scoutMaxIterations = 15

// scoutMaxDeadline is the hard ceiling on a scout's own wall-clock budget,
// regardless of how much time its caller has left. See
// scoutDeadline's doc comment for how this combines with the caller's own
// remaining deadline.
const scoutMaxDeadline = 180 * time.Second

// scoutToolProfile is the fixed, narrow tool whitelist a scout run gets —
// item 21's "read, grep, glob, ls, bash (read-only), plus help, lua".
//
// This codebase does not have separate "grep"/"glob"/"ls" tools: "find"
// (method="grep" or "glob") already covers both, and directory listing is
// reached through find's glob mode too — there is no dedicated "ls". So the
// profile below is "find" + "read" + "help".
//
// Neither "bash" nor "lua" are in it, and unlike an ordinary subagent's
// tools: whitelist this profile is NOT run through AllowOnlySubagent/
// alwaysAllowedTools — see scoutGate/scoutSchemaJSONForDepth below, which
// build scout's runtime gate and schema directly from this list instead.
// That distinction is load-bearing: alwaysAllowedTools unconditionally
// folds "lua" into every other subagent's schema and gate, and lua can
// dispatch tool("bash", ...) or any other mutating tool internally (a
// script's tool() calls reach RunTool directly — see toolgate.go's package
// doc comment). Earlier drafts of this file included "bash" here reasoned
// as "read-only" with a documented gap; that reasoning was wrong on two
// counts — bash itself has no read-only enforcement ANYWHERE in this
// codebase to lean on, and lua would have reached it regardless of
// whether "bash" was even listed. tools/btw_readonly.go's
// BtwReadOnlyGate is the existing precedent for how this codebase actually
// achieves "read-only": omit every tool (bash AND lua) that could
// possibly write, rather than trying to sandbox one that can. This profile
// follows that precedent.
//
// "scout" itself is deliberately NOT in this list — item 21 is explicit
// that scout's own tool profile must not make the recursion tool visible
// by a name-listing trick. Whether a scout spawned from this one may
// itself call "scout" again is entirely the depth gate's call
// (AllowedDelegationTool), added back into the schema and permitted at
// the runtime gate independently of this whitelist — see
// scoutSchemaJSONForDepth's doc comment and subagentToolRunner.Run in
// main.go.
var scoutToolProfile = []string{"find", "read", "help"}

// ScoutGate builds scout's own runtime tool gate: exactly scoutToolProfile,
// nothing else — in particular NOT alwaysAllowedTools' "lua" (see
// scoutToolProfile's doc comment for why that matters). Mirrors
// BtwReadOnlyGate (tools/btw_readonly.go) rather than going through
// AllowOnlySubagent/newAllowGate, which would fold lua back in. Exported so
// main.go's subagentToolRunner.Run can use it for a scout's runtime
// dispatch (SubagentOptions.ScoutMode).
func ScoutGate() ToolGate {
	allowed := make(map[string]struct{}, len(scoutToolProfile))
	for _, name := range scoutToolProfile {
		allowed[name] = struct{}{}
	}
	permitted := append([]string(nil), scoutToolProfile...)
	sort.Strings(permitted)
	list := strings.Join(permitted, ", ")
	return func(name string) error {
		if _, ok := allowed[name]; ok {
			return nil
		}
		return fmt.Errorf("tool %q is not available to a scout; read-only tools are: %s", name, list)
	}
}

// ScoutSchemaJSONForDepth builds the tool schema offered to a scout's own
// model turn: scoutToolProfile filtered straight out of the plain tool
// registry (no MCP — item 21 does not extend to MCP tools), through
// ScoutGate rather than subagentToolsSchemaFor/GetSubagentToolsSchemaJSONFor
// — those unconditionally fold alwaysAllowedTools ("lua") into ANY
// subagent's schema, which would offer scout a tool its own runtime gate
// (ScoutGate) then refuses, breaking the schema/gate invariant every other
// schema builder in this package keeps. "scout" itself is added back in
// when depth permits a scout to spawn one, the same way
// GetSubagentToolsSchemaJSONForAtDepth adds it back for an ordinary
// whitelisted child — see AllowedDelegationTool's doc comment. Exported so
// main.go's agentRunner.run can use it for a scout's own child (opts.ScoutMode).
func ScoutSchemaJSONForDepth(depth int) json.RawMessage {
	schema := GetToolsSchema()
	gate := ScoutGate()
	filtered := make([]map[string]any, 0, len(schema)+1)
	for _, s := range schema {
		if fn, ok := s["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && gate(name) != nil {
				continue
			}
		}
		filtered = append(filtered, s)
	}
	if AllowedDelegationTool(depth) == "scout" {
		if entry, ok := findSchemaEntryByName(schema, "scout"); ok {
			filtered = append(filtered, entry)
		}
	}
	data, _ := json.Marshal(filtered)
	return data
}

// scoutCallerCtxKey identifies, for the concurrency semaphore below, which
// running child a scout call came from. Stamped by runSingleTask
// (tools/subagent.go) onto every ordinary child's context as that child's
// own todoAgentID — already a fresh, unique id minted once per child —
// rather than JobIDCtxKey, which this file deliberately strips before
// recursing (see scoutRunContext's doc comment): reusing JobIDCtxKey would
// collapse every scout nested under one job, or under another scout (which
// never carries a job id itself), into a single shared bucket instead of
// one per actual caller.
//
// runSingleTask is not the only stamper, though: btw.go's promoted /btw job,
// its resumed-job path, and internal/workflow/engine.go's named-agent
// session all reach scout-eligible depth (>=1) without ever going through
// runSingleTask, so each stamps its own id via WithScoutCaller below instead
// (see that func's doc comment). scoutCallerIDFromContext returning "" means
// none of these ever ran — not "every caller shares one bucket", which used
// to be this comment's (wrong) claim before those three sites existed.
type scoutCallerCtxKey struct{}

func scoutCallerIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(scoutCallerCtxKey{}).(string)
	return id
}

// WithScoutCaller stamps ctx with an explicit scout caller id, for the
// handful of paths that reach scout-eligible depth (>=1) without going
// through runSingleTask (tools/subagent.go), which is otherwise the only
// stamper of scoutCallerCtxKey: btw.go's promoted /btw job and its
// resumed-job path (each already has a process-unique job id in hand, so
// they pass that directly) and internal/workflow/engine.go's named-agent
// workflow session (which has no job id at all, so it mints one via
// NewScoutCallerID instead). Without this, all three would land on
// scoutCallerIDFromContext returning "" and share one 2-slot bucket —
// unrelated callers refusing each other's scout calls.
func WithScoutCaller(ctx context.Context, callerID string) context.Context {
	return context.WithValue(ctx, scoutCallerCtxKey{}, callerID)
}

// scoutExternalCallerCounter backs NewScoutCallerID, mirroring
// todoAgentIDCounter (tools/subagent.go) — a second counter rather than
// exporting that one because it is simpler to give this handful of
// non-runSingleTask callers their own numbering than to export
// subagent.go's private counter for one narrow use.
var scoutExternalCallerCounter uint64

// NewScoutCallerID mints a fresh, process-unique id for WithScoutCaller, for
// a caller with no natural unique identity of its own to reuse (unlike
// btw.go's two sites, which already have a job id in hand — see
// WithScoutCaller's doc comment). label is folded into the id purely for
// readability in logs/debugging; bucket separation comes entirely from the
// counter, not from label being distinct.
func NewScoutCallerID(label string) string {
	n := atomic.AddUint64(&scoutExternalCallerCounter, 1)
	if label == "" {
		return fmt.Sprintf("scout-caller-%d", n)
	}
	return fmt.Sprintf("%s-%d", label, n)
}

// Concurrency semaphore — item 21's primary safety control once depth
// reaches its limit ("what actually bounds the burn ... is the concurrency
// semaphore ... not a secondary one"). Two simple counters, not anything
// fancier:
//
//   - scoutProcessSem cap the process-wide total (6).
//   - scoutCallerCounts caps how many of a single caller's own scout
//     children may run at once (2), keyed by scoutCallerCtxKey above.
//
// A caller failing to reserve a slot gets a plain refusal, not a block:
// blocking here risks eating a caller's own (possibly short, inherited)
// deadline waiting on a slot that may never free, and a refusal the model
// can act on (retry once a sibling finishes, or narrow the fan-out) is
// simpler to reason about than a wait with no visible progress.
const (
	maxScoutsPerCaller   = 2
	maxScoutsProcessWide = 6
)

var (
	scoutProcessSem  = make(chan struct{}, maxScoutsProcessWide)
	scoutCallerMu    sync.Mutex
	scoutCallerCount = map[string]int{}
)

// SnapshotScoutConcurrencyForTesting resets scout's concurrency semaphore
// (scoutProcessSem/scoutCallerCount) to a fresh, empty, full-capacity state
// and returns a func that restores whatever was there before — same
// snapshot/restore shape as SnapshotLuaRunHistoryForTesting
// (tools/lua_tool.go): `defer tools.SnapshotScoutConcurrencyForTesting()()`
// at the top of a test that depends on the semaphore's exact fill level.
//
// It exists because scoutProcessSem/scoutCallerCount are process-global and
// never reset between tests. TestAcquireScoutSlot_ProcessWideCap fills all
// maxScoutsProcessWide slots and asserts the next acquire is refused; that
// assertion is only correct while every earlier test released everything it
// acquired AND no other test is concurrently holding a slot of its own —
// true today only because nothing in this package uses t.Parallel(). The
// first parallel test added to tools/ would race this one's fill-then-
// overflow check against whatever slots that other test happened to be
// holding, with no compile-time or immediate-failure signal — it would
// just start flaking under -race/-parallel, exactly the class of latent
// cross-test bleed this repo has hit before. Swapping in fresh state (not
// merely recording a baseline count) is the point: a concurrency test needs
// the full complement of slots to itself, not "whatever happened to be
// free".
func SnapshotScoutConcurrencyForTesting() func() {
	scoutCallerMu.Lock()
	savedCount := scoutCallerCount
	savedSem := scoutProcessSem
	scoutCallerCount = map[string]int{}
	scoutProcessSem = make(chan struct{}, maxScoutsProcessWide)
	scoutCallerMu.Unlock()
	return func() {
		scoutCallerMu.Lock()
		scoutCallerCount = savedCount
		scoutProcessSem = savedSem
		scoutCallerMu.Unlock()
	}
}

// acquireScoutSlot reserves one concurrency slot for a scout call from
// callerID, enforcing both the per-caller and process-wide caps. On
// success it returns a release func the caller must invoke exactly once
// (defer it immediately) to free the slot again; on failure it returns
// (nil, false) having reserved nothing.
func acquireScoutSlot(callerID string) (release func(), ok bool) {
	scoutCallerMu.Lock()
	if scoutCallerCount[callerID] >= maxScoutsPerCaller {
		scoutCallerMu.Unlock()
		return nil, false
	}
	// Capture the channel under the lock rather than reading the
	// scoutProcessSem package var again in the release closure below. The
	// two normally name the same channel, but SnapshotScoutConcurrencyForTesting
	// swaps scoutProcessSem for a fresh one under scoutCallerMu — if the
	// closure read the var itself (outside the lock, on release), a release
	// racing a swap could drain the NEW channel instead of the one it
	// actually filled here, leaking a slot in the old one forever (and, for
	// any release still in flight when a test asserts on the old semaphore's
	// fill level, a goroutine blocked forever on a channel nothing will ever
	// receive from again).
	sem := scoutProcessSem
	select {
	case sem <- struct{}{}:
	default:
		scoutCallerMu.Unlock()
		return nil, false
	}
	scoutCallerCount[callerID]++
	scoutCallerMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			scoutCallerMu.Lock()
			scoutCallerCount[callerID]--
			if scoutCallerCount[callerID] <= 0 {
				delete(scoutCallerCount, callerID)
			}
			scoutCallerMu.Unlock()
			<-sem
		})
	}, true
}

// scoutDeadline derives the nested scout's own context from ctx: it
// inherits the caller's remaining deadline, capped at scoutMaxDeadline, so
// a chain of scouts converges on its own instead of nesting a fresh 180s
// inside whatever window is already running out. A caller with no deadline
// at all (e.g. a context nobody ever wrapped with one) falls back to a
// plain scoutMaxDeadline from now — this is the one case a scout DOES get
// a fresh 180s, because there is no caller budget to inherit from.
func scoutDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if dl, ok := ctx.Deadline(); ok {
		remaining := time.Until(dl)
		if remaining > scoutMaxDeadline {
			remaining = scoutMaxDeadline
		}
		if remaining < 0 {
			remaining = 0
		}
		return context.WithTimeout(ctx, remaining)
	}
	return context.WithTimeout(ctx, scoutMaxDeadline)
}

// Run executes one scout task synchronously and returns its conclusion.
// Deliberately narrow input: exactly {"task": "..."} — no tasks array, no
// async, no isolation, no agent/model choice, no timeout override. See
// ScoutTool's doc comment for why each of those is missing on purpose.
func (t *ScoutTool) Run(ctx context.Context, input map[string]any) ToolResult {
	if t.Runner == nil {
		return ToolResult{Type: "result", Success: false, Error: "scout runner not configured"}
	}

	task, ok := input["task"].(string)
	if !ok || strings.TrimSpace(task) == "" {
		return validationResult(`"task" (string) is required`)
	}

	if mc := connector.ModelClientFromContext(ctx); mc == nil {
		return ToolResult{Type: "result", Success: false, Error: "no model specified and no default model set"}
	}

	// callerID identifies which running child this scout call's concurrency
	// slot is booked against (see acquireScoutSlot below). It is only ever
	// "" — collapsing every such caller into one shared per-caller bucket
	// instead of one bucket per actual caller — if ctx was never stamped
	// with scoutCallerCtxKey at all.
	//
	// Every route into ScoutTool.Run goes through RunTool, which is only
	// reachable at depth >= 1 for "scout" (see AllowedDelegationTool/
	// ToolAllowedAtDepth in toolgate.go). depth >= 1 is reached two ways:
	// the common one is runSingleTask (tools/subagent.go), which
	// unconditionally stamps scoutCallerCtxKey before any caller could
	// dispatch a nested scout call. But it is NOT the only one — btw.go's
	// promoted /btw job, its resumed-job path, and
	// internal/workflow/engine.go's named-agent session all set depth >= 1
	// directly (they run the same restricted-child tool gate runSingleTask's
	// children do, just outside runSingleTask itself), and each of those
	// three now stamps scoutCallerCtxKey explicitly via WithScoutCaller
	// (see its doc comment) for exactly this reason. If a future path ever
	// sets depth >= 1 without going through one of those four stampers,
	// every such caller would silently share one 2-slot bucket instead of
	// getting its own, which would look like unrelated scouts randomly
	// refusing each other rather than a caller hitting its own cap.
	callerID := scoutCallerIDFromContext(ctx)
	release, acquired := acquireScoutSlot(callerID)
	if !acquired {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   fmt.Sprintf("too many scouts already running (limit: %d per caller, %d process-wide) — wait for one to finish, or narrow this into fewer scout calls", maxScoutsPerCaller, maxScoutsProcessWide),
		}
	}
	defer release()

	runCtx, cancel := scoutDeadline(ctx)
	defer cancel()

	// A scout registers no job of its own: strip whatever job identity this
	// call happened to inherit from its caller (a running job, or another
	// scout that already stripped it) BEFORE runSingleTask ever reaches
	// main.go's agentRunner.run. Without this, jobID there would resolve
	// to the CALLER's own job id, and stashResumable/JobMailboxNextMessages/
	// JobProgressHeartbeatCheck would all act as if this scout call WERE
	// that job — overwriting the caller's own resumable stash with the
	// scout's transcript, draining the caller's message mailbox mid-scout,
	// and answering the caller's own progress-heartbeat nudge on its
	// behalf. An empty string is not "unset": ctx.Value still returns it,
	// so every one of those call sites' own `jobID != ""` / `jobID == ""`
	// guards does exactly the right thing.
	runCtx = context.WithValue(runCtx, JobIDCtxKey{}, "")

	scoutTask := subagentTask{
		Task: task,
		// toolsOverride is kept in sync with scoutToolProfile for
		// documentation/debugging (SubagentOptions.Tools ends up carrying
		// it), but the actual schema/gate enforcement goes through
		// scoutMode below (scoutGate/scoutSchemaJSONForDepth), not
		// opts.Tools — see ScoutMode's doc comment in tools/tool.go for
		// why AllowOnlySubagent/GetSubagentToolsSchemaJSONForAtDepth must
		// not be the path a scout's opts.Tools drives.
		toolsOverride:    scoutToolProfile,
		maxIterationsCap: scoutMaxIterations,
		scoutMode:        true,
	}
	res := runSingleTask(runCtx, t.Runner, scoutTask, 0, true)
	return resultsToToolResult([]subagentResult{res})
}
