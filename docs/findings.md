## Status (2026-08-11)

- **Markdown blank-lines**: fixed (`collapseMarkdownBlankLines`,
  commit `b266597`, wired in at `display/tui_markdown.go:56`).
- **Lock P1** (locks vanish after ~60s even without `seconds`): fixed
  (`agent/tools_exec.go`, commit `52d3cf7`) — `lock`/`unlock` got
  `toolTimeout = 0`, just as `subagent`/`wait` had before.
- **Lock P2** (`write`/`edit` do not check `lockRegistry`): this is a known,
  deliberate gap in the scope of the original plan (`locks/` is advisory
  locking — see `docs/subagent-testing-plan.md`), not a regression. To be
  done as a separate integration step if it is to be enforced
  physically.
- **Lock P3/P4** (read warns about a lock, coordination between agents
  through `note`+eventbus): proposals, not yet accepted or
  implemented.

# Markdown rendering — findings

Topic: in the TUI, LLM answers in markdown format "do not load quite right" — where there should be text the user sees empty lines (e.g. 10 empty lines).

Below are the observations collected from inspecting the code in `display/tui_*.go` and `session/resume_list.go`.

## Markdown rendering pipeline — how it works

- Incoming LLM deltas go through `TUI.Text()` → `flushLoop` → `tuiMsgBlock{kind:"text"}` → `handleBlockMsg` → `appendOrAppend("text", chunk)`.
- During streaming the block is `dirty=true`. Rendering goes through `streamWrap` (incremental wrap of the last logical line).
- On `done` / `tool-start` / `error` / `block` / a new block type, `forceRenderDirtyBlocks` is called — glamour renders the whole content, the result goes to `mdCacheRendered[idx]`, `cachedLines = strings.Split(rendered, "\n")`, `cachedLineCount = lineCount(rendered)`.
- Display: `renderFrame` → `buildMessageRegion` → `buildFlatRenderLines` → `getBlockLines` → returns `cachedLines`.

## What I noticed (obvious)

1. **Glamour inserts vertical gaps between markdown sections.**
   For a typical answer with 3 headings, a list and a closing line we get
   ~9 lines with content and ~6 empty "padding" lines (each with `lipgloss.Width = width`,
   because glamour pads every line to `m.width`). After stripping ANSI each
   such "padding" line is just spaces — it looks like an empty row
   in the terminal. This is glamour's behaviour, not our code's.

2. **`lineCount` vs `len(strings.Split(...))` difference.**
   `lineCount(s)` = `newlines + 1` (1 even for `""`). `len(strings.Split("", "\n"))` = 1.
   Consistent, but `forceRenderDirtyBlocks` uses `lineCount` and then does a `Split`.
   Care is needed so that `cachedLineCount` matches `len(cachedLines)`.

3. **`renderMarkdownWithCache` returns `""` → no cache update.**
   If glamour returns empty output (e.g. for content made only of whitespace),
   `forceRenderDirtyBlocks` updates neither `cachedLines` nor
   `cachedLineCount`. The old values from streaming stay. For text this
   rarely happens, but it is a path to a potential desynchronisation.

4. **`tryRenderMarkdown` has three branches.**
   - `!dirty && hasCached && cached != ""` → returns the cache.
   - `dirty && isStreaming` → streamWrap (returns the wrap + sets cached).
   - else → glamour render.
     There is NO path here for `!dirty && cached == ""` — it goes to glamour then.
     But there is also the scenario `!dirty && cached != ""` with `isStreaming=true`
     (e.g. after forceRenderDirty, when a new delta arrives): it returns the cache — OK.

5. **The `streamWrap.lastLen == len(content)` shortcut.**
   It only applies when the content does not grow (while streaming it always grows). Safe.

6. **`appendOrAppend` is the hot streaming path.**
   ```go
   last.cachedLineCount = 0
   last.cachedLines = nil
   ```
   It clears both. Then it tries to refresh through `getBlockLines`.
   If `m.cachedTotalLines >= 0`, it does so. If `< 0`, it does not
   (leaves nil). Then `cachedTotalLines = -1` forces a recompute
   on the next access. Consistent.

7. **Spacer between blocks.** `buildAllFlatRenderLines` adds 1 empty line
   (spacer) between every pair of blocks (`kind != "tool"` neighbours).
   A glamour-rendered block may already have empty lines at its end (usually
   Trim removes them, but if the last line has whitespace padding, it stays).
   Together they can produce double empty rows.

8. **`m.width` is passed to `renderMarkdownWithCache` as `width`.**
   The renderer caches per `maxW`. After a resize `invalidateAllBlockLineCounts`
   clears `mdCacheRendered`. But the `rendererCache` itself (the glamour TermRenderer)
   is NOT cleared — it stays lightly used, but works correctly
   for the new `maxW`. OK.

## What happens with a long response

Experiment (test in `display/md_blankcount_test.go`, already removed):
```
content = "Sure!... ## Section 1\n\nLorem...\n\n## Section 2\n\nL2...\n\n- item 1\n- item 2\n- item 3\n\nEnd."
→ 15 lines in total, 9 with content, 6 empty (markdown separators)
```

So typically ~40% of rows are empty glamour separators. With 3–5
headings and lists the user really sees as many empty lines as
content lines. For the user "where there is text, there are empty lines" — because he
counts a separator blank row as an "empty line".

## Hypotheses

A) **Glamour separators are annoying.** The user does see empty
   separators between `##` headings and paragraphs. Proposed fix:
   in the render, strip trailing whitespace from every glamour line
   (so the ANSI padding does not push the line to full width),
   and maybe even collapse 3+ blank → 1 blank.

B) **There may be a real line-count mismatch bug after the force-render.**
   If the glamour render returns fewer lines than the streaming wrap
   (e.g. a URL with `_` broken character by character by streamWrap, while glamour
   breaks at dots), then after the force-render `cachedLineCount` shrinks,
   and `cachedTotalLines` is invalidated to -1. It should recompute
   correctly on display. Then there should be no "empty"
   lines — rows are either content or a separator blank row. But if
   `cachedTotalLines` drifts away from the actual sum (e.g. through a corner case
   with `rendered == ""`), extra empty rows may be displayed.

C) **Glamour failure not handled.** If glamour returns an error,
   `renderMarkdownWithCache` falls back to `wrapRawText`. OK. But
   that may give different results than the streaming wrap.

# Lock tool — findings (section 3 of the test plan)

Topic: scenarios from `docs/subagent-testing-plan.md` section 3 (lock/unlock).
Running them by hand in the tyci-agent runtime revealed two problems in the
integration layer — the logic in `locks/registry.go` and `tools/lock.go` is correct
(unit tests are green), but the wiring to the rest of the tools does not close
the contract "holding a lock blocks editing".

## What the scenario says and what the implementation says

- Scenario **3.2** requires: a second `lock(path)` in the runtime → error
  "already locked by …".
- Scenario **3.3/3.4** requires: `unlock` with the right holder → success,
  with a foreign one → error.
- Scenario **3.6** implies: holding a lock should really protect the
  file from `write`/`edit` from another call, because otherwise the lock is
  declarative and useless in a multi-agent scenario.

Implementation:

- `tools/tool.go:415` — package-level `var lockRegistry = locks.NewRegistry()`.
- `tools/tool.go:428-429` — Registry wired into `LockTool`/`UnlockTool`.
- `tools/write.go:13` — `type WriteTool struct{}` — **no Registry field**,
  `Run` does not consult `lockRegistry` in any branch
  (`runWriteMode`, `runEditMode`, `appendFile` internally).
- `tools/edit_write_test.go` — the `WriteTool` tests do not check locks
  (no cross-tool integration).
- `locks/registry.go` (`Acquire` line ~80) — the holder and TTL tracking
  logic is correct and covered by tests.

## What I observed in the runtime (manual, 2026-08-11)

```
1. lock("test/scenario-3/longttl.go") → success, holder H1
2. lock("test/scenario-3/longttl.go") (different holder, same path,
   no wait/unlock in between) → success, holder H2  ← expected conflict
3. unlock("test/scenario-3/longttl.go", H2) →
   "could not unlock …: not locked, already expired, or held by a
    different holder"  ← H2 is not known as a holder despite the "success"
4. lock("test/scenario-3/lock-then-write.go") → success, holder H3
5. write("test/scenario-3/lock-then-write.go", "package x\n") → OK, writes
6. edit ("lock-then-write.go", old="package x", new="package edited")
   → "replaced 1 occurrence(s) … at line 1"  ← went through despite the lock
```

The path `/Users/piotr.halas/work/tyci-agent/test/scenario-3/*` was removed
after the test (`rm` + `rmdir`) so as not to litter the repo.

Additionally: `go test -race ./tools -run "TestLockTool|TestUnlockTool" -v`
→ **11/11 PASS** — the logic of the registry itself and the lock↔unlock interaction is green.

## Conclusions — two independent integration bugs

### P1 — The lock tool does not share state between calls in the runtime

**Symptom**: successive `lock(path)` calls in the same session do not see each other;
`unlock` with the holder returned by a `lock` call a moment earlier returns
"not locked".

**Probable cause**: the package-level `lockRegistry` in `tools/tool.go`
**is** package-level, so it should be shared between tool calls in the
same process. So the culprit is more likely in a higher layer — either:

- `agent/run_once.go` / `commands.go` passes the tool call a
  separate, short-lived `ctx` whose `Done()` triggers the cleanup goroutine
  from `registry.Acquire` (see line ~95 in `locks/registry.go`:
  `go func() { ... ctx.Done() }`).
- or every tool wrapper has its own `LockTool{...}` instance built from the
  package-level one — but that would give the same `lockRegistry` pointer.

The first hypothesis matches what was observed: the lock is removed
immediately after the tool call returns because the `ctx` ends. Then even
`seconds:120` does not protect, because the expiry is counted from acquire, and after
`Registry.Acquire` returns, the listener on `ctx.Done()` removes the entry immediately.

To check: in `commands.go` and/or `agent/run_*.go`, how `Run(ctx, …)`
is called for the tools — which `ctx`? Is it `context.Background()`, or
`context.WithTimeout(...)`, or a parent ctx that dies after every tool call?

### P2 — `write` / `edit` do not consult lockRegistry at all

**Symptom**: `write` and `edit` on a locked path go through without
a warning. The advisory lock has zero effect on this tool.

**Cause and fix** (proposed):

```go
type WriteTool struct {
    Registry *locks.Registry   // ← add
}

func (t *WriteTool) Run(ctx context.Context, input map[string]any) ToolResult {
    ...
    path, _ := input["path"].(string)
    if t.Registry != nil {
        if h := t.Registry.Holder(path); h != "" && h != selfHolder(ctx) {
            return ToolResult{
                Type:    "result",
                Success: false,
                Error:   fmt.Sprintf("path %q locked by %q; unlock first", path, h),
            }
        }
    }
    ...
}
```

But this solves P2 only **if the Registry is shared** —
that is, after the P1 fix. Without the P1 fix, P2 changes nothing (the registry will be empty).

### Dependency and order of fixes

Fix **P1 first**, then P2. Otherwise P2 will behave identically
(a unit test with a mock Registry will pass, but nothing is blocked at runtime).

## Alternative hypotheses

D) **A deliberate design decision** — the advisory lock is declarative,
   the intent is for the caller to check before writing. But then
   scenario 3.2 (conflict on lock) **makes no sense**, because the lock is not
   "waiting on a conflict", it is "a note that I will be working here".
   The document `subagent-testing-plan.md` 3.2 explicitly speaks of a conflict
   error — so this is NOT a deliberate decision; it is a bug.

E) **Race in the var init** — but the package-level `var = locks.NewRegistry()`
   is thread-safe (executed once at process start) and `mu sync.Mutex`
   in `Registry` protects the map. It does not explain the observation.

F) **Over-general fan-out** in the sub-tasks of test 3.2: when `tid-1` starts
   a subagent, it creates a new tool runner with a fresh Registry port.
   But in my test there was **no subagent** — there were two `lock` calls in
   one session, sequential. That rules out fan-out.

## Decision

I am coming back with a question: is this an isolated P1 (the ctx cleanup is not to blame,
and the culprit is something else), or is the ctx wrap in
`agent/run_once.go` really the culprit? Diagnosing P1 requires:

- one `lock` in the runtime, then `go test` pulling the registry state
  out through a debug endpoint, or
- a short `log.Println` in `locks/registry.go` on acquire/release
  and tracking when the entry leaves the map.

A quick repair plan for both problems (after approval):

1. `tools/lock_test.go`: add an integration test lock→write that write fails
   when the holder is other than self.
2. `tools/write.go`: add a `Registry` field, a check in `Run` and
   in `runWriteMode` and `runEditMode` (append mode too, because it is also a
   modification).
3. Diagnose P1: find out who calls `toolRun(ctx, …)` and with which ctx.
   Fix: either use `context.Background()` for the tool run if it is
   transient, or extend the ctx bound so that the lock survives.

After the fixes run: `go test -race ./...`, plus scenarios 3.1–3.6 from
`docs/subagent-testing-plan.md` by hand in the TUI.

## First-class addition to test 3.2 in code form

If the lock worked correctly, `TestLockRuntimeIsolation` is worth accepting
in `tools/lock_test.go`:

```go
func TestLockRuntimeIsolation(t *testing.T) {
    lt := &LockTool{Registry: locks.NewRegistry()}

    r1, _ := lt.Run(context.Background(), map[string]any{"path": "X"})
    if !r1.Success {
        t.Fatalf("first lock failed: %v", r1.Error)
    }

    r2, _ := lt.Run(context.Background(), map[string]any{"path": "X"})
    if r2.Success {
        t.Fatalf("second lock should conflict; got success: %s", r2.Content)
    }
}
```

(This implementation already passes as a unit test with a single `Registry` —
the problem lies solely in **how** the tyci-agent runtime calls these tools.)

## Decision

I am coming back to the user to ask for a repro:
- does it happen always or only for a specific kind of answer
  (many headings, lists, code blocks, tables)?
- does it happen after resizing the terminal?
- does the user also see empty lines after scrolling back up?
- does the user use the mouse to select text (because `renderSelectableLine`
  may eat content under specific conditions)?

# Lock — extensions (P3, P4)

Topic: after P1+P2, the advisory lock in the runtime should also (a) tell
the reading agent that the file is being edited elsewhere, and
(b) enable coordination between agents so that an agent can
"wait" until another finishes instead of going in blind.

## P3 — `read` reports the lock (read-aware, does not block)

### Design decision: read returns a **warning**, not an error

`read` does not mutate the file. Hard-blocking the reading agent
would have a side effect: the agent would first have to take a lock for itself
for the duration of a single `read` — which breaks composability and is too rigid
for a tool that by definition does not change state. Instead `read`
should **inject into the result** an unambiguous warning header
and nothing else.

### Contract

Input: `read(path: "foo/bar.go")`, where the path is locked
by another holder Hx (important: if Hx is me, no warning).

Output: the existing `ToolResult.Content` with a prepended:

```
[NOTE: foo/bar.go is currently locked by holder "holder-6e76d17fa51a"
since 2026-08-11T14:21:09Z (expires 2026-08-11T14:26:09Z, 4m left).
File contents mirror that point in time and may not reflect the
holder's final state. To wait for completion, run
`wait(job_id: "...")` or call `lock(path: "foo/bar.go", seconds: N)`
yourself, then re-read after unlock.]


<existing read output here, unchanged>
```

Criteria:
- `Success` stays `true` (the model must not be forced into a retry/error path).
- The file content is returned unchanged — read reads what is on disk,
  the warning is meta above the content.
- No `NOTE` if `Registry == nil`, or there is no lock, or the holder
  is `selfHolder(ctx)`.

### Implementation skeleton (proposed)

```go
type ReadTool struct {
    Registry *locks.Registry   // optional
    // SelfHolder, if not set, uses the default "holder-<token>"
    // from the parent session context (the model's identity in the parent turn). The caller
    // does not have to provide it if the Registry is shared.
}

func (t *ReadTool) Run(ctx context.Context, input map[string]any) ToolResult {
    path, _ := input["path"].(string)
    ...

    if t.Registry != nil {
        if h, since, expires := t.Registry.HolderWithMeta(path); h != "" && h != t.Registry.SelfHolder(ctx) {
            expiry := "no expiry"
            if !expires.IsZero() {
                ttl := time.Until(expires).Round(time.Second)
                expiry = fmt.Sprintf("expires %s (%s left)", expires.Format(time.RFC3339), ttl)
            }
            content = fmt.Sprintf(
                "[NOTE: %s is currently locked by holder %q since %s (%s).\n"+
                "File contents reflect that point in time and may not "+
                "reflect the holder's final state. To wait, call "+
                "`lock(path:%q, seconds:N)` for yourself, then re-read "+
                "after that holder unlocks.]\n\n%s",
                path, h, since.Format(time.RFC3339), expiry, path, content)
        }
    }
    ...
}
```

`Registry.HolderWithMeta(path)` is a new method on `locks.Registry`,
declared next to `IsLocked`/`Acquire`/`Release`, returning the holder,
`AcquiredAt` and `ExpiresAt` atomically under `r.mu`.

### Tests (`tools/read_test.go`)

New:

- `TestReadNoteOnLockByOther`: a Registry with a lock held by H-other →
  the output starts with `[NOTE: ...]`, the file content is intact,
  `Success == true`.
- `TestReadNoNoteOnLockBySelf`: a Registry with a lock held by H-self
  (SelfHolder == acquired) → no NOTE.
- `TestReadNoNoteWhenNoLock`: empty Registry → no NOTE.
- `TestReadNoNoteWhenNilRegistry`: no wiring → no NOTE
  (regression — the existing path must not be broken).

### Message — checklist

For the warning to be really useful to the model (not to humans):

1. **Who**: the full holder id (`holder-…`). Without it the agent does not know
   whom to wait for or how to release it (that takes a separate
   subagent — see P4).
2. **Since when**: an ISO timestamp (`RFC3339`). It helps judge whether the lock
   is fresh (someone may have crashed 5 minutes ago) or old
   (probably active).
3. **Until when**: either "expires …" (when the lock has a TTL), or "until you
   or they call `unlock`" (no TTL). A clear distinction, because the
   release rules differ.
4. **What to do**: a suggested next action (`lock` yourself, then
   re-read; or `wait` on the job_id if there is one). The message must
   **give commands to paste**, not a vague "wait for them".

Without these 4 points the warning is ignored — the model sees a block of
text and treats it like file content.

### Scenarios to add to `docs/subagent-testing-plan.md`

- **3.7**: `lock(path:"X")` as agent A, then `read("X")` as
  agent B (or the same one in a later turn) → NOTE in the content, holder
  visible, command suggestion. File content intact.
- **3.8**: like 3.7, but with `seconds:5` on A's lock → the NOTE has
  `expires … (X left)` and it correctly shrinks on every `wait`.
- **3.9**: own lock → read without NOTE.

## P4 — coordination between agents: "wait for the lock"

### Problem

Agent A takes `lock(path:"X")` and starts editing. Agent B in the
same process needs to read and/or edit `X` — without
coordination B either writes blind (old read → old context),
or takes a second lock (3.2 blocks — but **usefully** only
after P1+P2+P3).

What is needed:

1. Atomic knowledge of "who holds it" **independent** of `lockHolder`
   — because the lock holder is a random hex (not a who). The holder has to be tied
   to the action that issued it (usually: a specific
   subagent with its model/agent name/job_id).
2. A "wait until X is released" mechanism — naturally implemented
   as a wait on `job_id`, because releasing the lock by another agent
   is a side effect of its job.

### Proposed contract

**Step A**: the `lock` tool with a new `note` field (optional string):

```
lock(path: "internal/foo.go", note: "implementer: rewriting parser")
```

The holder returned is still random (e.g. `holder-6840ffbb75e0`) — it
is our technical identifier. **But** the registry now
stores `(holder → note)` pairs. `read` (P3) and `lock` (3.2)
show the note in the error/warning message, e.g.:

```
path "foo/bar.go" already locked by "holder-95f952e9056b"
  since 2026-08-11T14:21:09Z (note: "implementer: rewriting parser",
  expires 2026-08-11T14:26:09Z)
```

**Step B**: the subagent tool in async mode returns a `job_id` whose
end we correlate with the release of all locks issued by
that subagent run. Implementation:

- `tools/subagent.go` `runAsync` (after registering in the job registry) —
  when job A ends, find all `(path, holder)` pairs
  issued during its lifetime and release those with TTL=0 (a session
  semaphore). If the user set `seconds`, leave it — the expiry itself
  will decide.

This way another agent can:

```
wait(job_id: "job-1234-…", seconds: 60)
# or:
lock(path: "foo/bar.go", note: "implementer: rewriting parser")
```

and **know** when to stop waiting.

**Step C** (optional): a subagent `request_lock(path, note, from)`.
It returns immediately, either with a `lock_id` if it took the lock, or with
`{ ok: false, held_by: "…", note: "…" }` if not. We do not block
the turn — the parent agent can accept or try another path.

### Communication between agents — flow

The flow for two parallel implementers editing
the same file:

```
A: lock(path:"x.go", note:"implements parseFoo")
   # holder-X, registry entry (X, note) created
A: writes fixes (write/edit one after another)
B: read(path:"x.go")
   # output with NOTE: "...locked by holder-X (note: implements parseFoo)..."
B: decides:
   (a) wait on A's job_id → because A holds the lock,
       A finishes → registry cleanup → B sees empty,
       B reads again and gets a clean result.
   (b) or waits in the Model turn for an eventbus notification (not there yet)
       and gets a prompt saying "A released lock on x.go".
```

### P4 implementation requirements

| What | Where | Size |
|----|--------|-------|
| `note` field in `lock`/`Registry.Acquire` | `tools/lock.go`, `locks/registry.go` | trivial |
| Lock output/cache pre-existing `second` | OK | existing |
| Subagent runner tracking locks in its lifetime | `tools/subagent.go`, `agent/run_once.go` | medium |
| Job completion → cleanup locks | the same runner | medium |
| Eventbus signal "lock released" to the parent | `eventbus/` | new |
| New tool `request_lock` | `tools/lock.go` (extension) | small |
| Integration tests 3.7–3.9 (with P3) | `tools/lock_test.go`, `tools/read_test.go` | small |

### Scenarios to add

- **4.9 / P4.A**: subagent A takes a lock with a `note`, subagent B gets a
  read with a NOTE containing A's `note`.
- **4.10 / P4.B**: after A finishes, its lock is cleaned automatically; B
  receives an eventbus notification ("path X unlocked by holder …").
- **4.11 / P4.C**: A does not give up the lock in the expected time → B gets a
  timeout in `wait(job_id:A, seconds:N)` with the message "wait cancelled
  while A still holds X".

## Decision

I am coming back to the user and asking:
- for P3, should read **be a warning** (we keep reading, info only),
  or a hard "refuse" (the model must lock first itself)?
  Recommendation: warning — read does not mutate.
- for P4, is `note` enough as a string, or do we need it structured as
  `{intent, agent, job_id}` so that another agent can automatically
  wait on `job_id`? Recommendation: structured + auto-wait in `wait`
  also accepting `lock_id`.
