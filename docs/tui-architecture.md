# TUI architecture & rendering

How the interactive TUI (`display/`) is built, how it renders efficiently, and
which optimizations we considered along the way. Written for anyone touching the
render path — the invariants here are easy to break by accident.

The overriding goal: **run well on low-end boxes**. Concretely that means near-0%
CPU when idle, instant key echo, and cheap repaints while a response streams in.
The transcript behaves like a scrolling log stream, and scrollback is a hard
requirement.

---

## 1. The big picture

The TUI is a [bubbletea](https://github.com/charmbracelet/bubbletea) program
(Elm-style Model/Update/View):

- **Model** — `TuiModel` in `tui.go`: the whole UI state (blocks, scroll
  position, input, status, picker/modal state, caches).
- **Update** — `tui_update.go` + handlers (`tui_keys.go`, `tui_mouse.go`,
  `tui_messages.go`, …): every message (`tea.Msg`) mutates the model.
- **View** — `tui_view.go`: turns the model into the frame string.

The agent talks to the UI from another goroutine through channels; those are
turned into `tea.Msg`s and fed to Update. The public surface is `TUI` in
`tui_api.go` (`NewTUI`, streaming helpers, `ModelChanges`, …).

```
agent goroutine ──chan──▶ TUI.post ──▶ p.Send(msg) ──▶ bubbletea event loop
                                                          │
                                                    Update(model, msg)
                                                          │
                                                    View() → frame
                                                          │
                                                  painter.paintRegion()  ← we own this
                                                          │
                                                     terminal (stdout)
```

### The event loop and idle CPU

bubbletea's event loop blocks on its message channel. When nothing is happening
— we're waiting on a prompt — **there are no messages, so the loop sleeps and
wakes zero times.** That is the foundation of 0% idle CPU.

The catch is bubbletea's *standard renderer*: it runs a `time.Ticker` at the
configured framerate (`WithFPS`) and wakes a goroutine every tick to diff and
flush the frame buffer — even when nothing changed. At 30fps that's a small but
nonzero idle floor (~0.3% CPU) plus up to `1/fps` of latency before a keypress
is echoed. On a low-end box that floor matters. This is why we wrote our own
renderer (see §4).

---

## 2. Rendering pipeline (View)

`renderFrame()` (in `tui_view.go`) assembles the frame top-to-bottom:

1. **Message area** — the transcript viewport (`visibleLines()` rows).
2. **Status bar** — one row (`tui_status.go`).
3. **Input** — the `textarea` (3 rows).

Full-screen overlays (model picker, subagent modal) short-circuit and render
their own view instead.

### Virtual viewport — only render what's visible

The transcript can be huge, so we never render all of it. `buildFlatRenderLines()`
(`tui_render_buffer.go`) produces a flat line list **covering only the visible
viewport**: it skips blocks entirely above the viewport and stops once the
window is full. This keeps a frame O(visible lines), not O(history).

### Per-block caches — don't re-render markdown every frame

Markdown rendering (glamour) is the expensive part, so it's cached per block
(`tui_render_block.go`):

- `mdCacheRendered[idx]` — rendered ANSI output per block.
- `dirtyBlocks[idx]` — set when a block's content changes; cleared after render.
- `streamWraps[idx]` — incremental raw-wrap state so a *streaming* block shows
  wrapped raw text without a full glamour re-render on every chunk.
- `cachedLineCount` / `cachedLines` — per-block line splits for scroll math.
- `toolDisplayCache[idx]` — formatted tool-call output.

Net effect: `renderFrame()` reads a lot of state but recomputes almost nothing —
the heavy work only runs for blocks that actually changed.

### Lazy ANSI stripping

`RenderLine.PlainText` (the styling-free form used for copy/hit-testing) is
computed on demand via `plain()`, so the strip cost is only paid when a selection
is actually copied, not on every render.

---

## 3. Streaming coalescing

Agent output arrives token-by-token. Repainting on every token would be wasteful,
so `flushLoop` (`tui_api.go`) batches appends:

- Content is accumulated in a buffer; `wakeFlush` signals the loop.
- The loop waits a **coalescing window** before emitting one `tuiMsgBlock`:
  - `coalesceCold` (33ms) — first chunk after a quiet period flushes fast so the
    response appears promptly.
  - `coalesceHot` (100ms) — once the stream is clearly sustained, batch harder
    (~3× fewer repaints, invisible at reading speed).
- When nothing streams, the loop is idle (zero wakeups).

---

## 4. The custom painter (`tui_painter.go`)

To get true 0% idle CPU and instant key echo we bypass bubbletea's ticker
renderer and drive the terminal ourselves. This is **on by default**; set
`TYCI_TUI_PAINTER=0` to fall back to the standard renderer.

### How it's wired

bubbletea has no public "inject a renderer" hook in v1.3.10. The only way to stop
its ticker is `tea.WithoutRenderer()`, which installs a *nil renderer* whose
terminal-control methods are all no-ops. So:

- `View()` still runs on every message (bubbletea always calls it). Instead of
  relying on the discarded return value, we hand the frame to the painter:
  `painter.paintRegion(frame, w, h, scrollBottom)`. Painting is thus **driven by
  View, not by a clock** → idle frames cost nothing, keypresses repaint instantly.

- **`WithoutRenderer` is designed for headless use**: bubbletea's `initTerminal()`
  early-returns for a nil renderer, skipping `initInput()`. That means stdin is
  never put in raw mode *and* no `WindowSizeMsg` is ever delivered (the model
  renders at width 0 → garbled). We do that setup ourselves in
  `setupPainterTerminal()`:
  - `term.MakeRaw(stdin)` (+ restore on exit),
  - initial size via `term.GetSize` → `p.Send(WindowSizeMsg{…})`,
  - live resize by forwarding `SIGWINCH` (`tui_painter_resize_unix.go`; a no-op on
    Windows, `tui_painter_resize_windows.go`, since Windows has no SIGWINCH).

  > ⚠️ This is the subtle part. If you ever see the painter render narrow/garbled
  > or input not echo, suspect this setup path first.

### What paintRegion does

The frame diff mirrors bubbletea's alt-screen `flush()`:

1. Guard: if the size isn't known yet (`w<=0||h<=0`), paint nothing — the first
   visible paint must be correctly sized.
2. On first paint, `enter()` the alt screen (alt-buffer, bracketed paste, optional
   mouse, hide cursor, clear).
3. If the frame is unchanged, return (no terminal I/O at all).
4. Otherwise diff **positionally, line by line** against the last frame: unchanged
   lines are skipped by advancing the cursor; only changed lines are rewritten
   (truncated to width, cleared to EOL when shorter).
5. Clamp to the bottom `height` lines (we can't move the cursor into scrollback).

State (`lastFrame`, `lastLines`, `linesRendered`) is guarded by a mutex; the diff
runs on the single event-loop goroutine, `stop()`/`repaint()` from the program
goroutine.

### Hardware scroll region (the headline optimization)

A streaming transcript grows at the bottom: every new line shifts the whole
message area up by one. A naïve positional diff then matches *nothing*
(line `i` is now old line `i+1`) and repaints the entire screen on every chunk.

Instead, `paintRegion` takes a **scroll-region hint** — the message area is rows
`[0, scrollBottom)`, passed from `View()` via `paintScrollBottom()` (the status
bar and input below it must *not* scroll). Then:

1. `detectScrollUp(old, new, scrollBottom)` checks whether the region is simply
   the previous frame shifted up by N lines (`old[i+N] == new[i]`).
2. If so, emit `DECSTBM` (set scroll region) + `SU N` (scroll up) so the **terminal
   moves those lines in hardware**, then restore the full-screen region.
3. `shiftLinesUp` mirrors the shift in `lastLines` (freed bottom rows blanked), so
   the normal positional diff that follows repaints **only the N newly revealed
   lines** — not the whole screen.

For a 40-row transcript this turns ~40 lines of output per chunk into ~1.

This is why **jump-scroll is disabled under the painter** (`tui_render_buffer.go`,
guarded by `m.painter == nil`): jump-scroll was a hack for the ticker renderer
(quantize the viewport start so rows stay stationary and the positional diff can
skip them). With hardware scroll, per-line shifts are cheap, so we pin exactly to
the bottom for genuinely smooth scrolling.

### Synchronized output (DEC 2026)

Every paint is wrapped in `\e[?2026h … \e[?2026l` (`ansi.ModeSynchronizedOutput`).
The terminal buffers the whole update and presents it atomically → no tearing or
flicker while scrolling or over a slow link. Terminals without support ignore the
markers.

---

## 5. Optimizations we considered

| Idea | Status | Notes |
|---|---|---|
| **Event-driven painter** (drop the ticker) | ✅ shipped, default | 0% idle CPU, instant key echo. §4. |
| **Hardware scroll region** (DECSTBM + SU) | ✅ shipped | ~40× less output per streamed line for a full-height transcript. §4. |
| **Synchronized output** (DEC 2026) | ✅ shipped | Atomic frames, no flicker. Cheap (~a few bytes/frame), safe. |
| **Jump-scroll** (quantize viewport start) | ✅ standard renderer only | Quarter-screen steps (`msgHeight/4`). Superseded by hardware scroll under the painter. |
| **Per-block markdown cache** | ✅ shipped | `mdCacheRendered` + `dirtyBlocks` + `streamWraps`. §2. |
| **Virtual viewport** | ✅ shipped | Render O(visible), not O(history). §2. |
| **Streaming coalescing** | ✅ shipped | Cold/hot windows batch repaints. §3. |
| **Scrollback compaction** | ✅ shipped | `compactBlocks(tuiMaxHistory)` drops oldest blocks + reindexes caches. §5a. |
| **Tool output cap** | ✅ shipped | `tuiMaxToolOutput` (1 MiB) bounds each tool block's `.output`. §5a. |
| **Subagent modal cap** | ✅ shipped | `tuiMaxModalBuffer` (1 MiB) bounds the streaming modal accumulator. §5a. |
| **Dead `rendered` field removal** | ✅ shipped | Was only ever zeroed, never read; removed to shrink `block`. |
| **`View()` memoization** | ❌ rejected | See below. |
| **Intra-line column diff** | ❌ rejected | Repaint only changed *characters* in a line. Complex with ANSI/wide chars; negligible gain over line-level diff. |
| **Absolute-CUP skip jumps** | ❌ rejected | Use absolute cursor moves instead of `\n` to skip runs of unchanged lines. Micro-optimization, not worth the complexity. |

### Why we rejected `View()` memoization

The idea: cache the frame string and skip `renderFrame()` when nothing changed.
On inspection it's a net negative here:

- **Idle is already 0%** — with the painter there's no ticker, so the event loop
  simply doesn't run View when nothing happens.
- **`renderFrame()` is already cheap** — it's O(visible lines) thanks to the
  virtual viewport, and the expensive markdown render is already cached per block.
  The residual cost is assembling a handful of visible lines.
- **The painter already dedupes identical frames** at the I/O layer (step 3 of
  `paintRegion`): an unchanged frame produces zero terminal output regardless.
- The only frames that would be "saved" are spurious re-renders (e.g. mouse-motion
  events), where the cost is microseconds of string building that the painter's
  I/O dedup already neutralizes.
- A correct memo would need a cache key covering **every** view-affecting field
  (~20 of them). Miss one and the UI freezes / goes stale. High risk, ~no reward.

### 5a. Memory bounds (`tui_memory.go`)

The TUI retains every block ever shown so the user can scroll back through the
whole conversation. Without bounds, a long coding session — thousands of tool
calls, each with full file contents in `.output` — grows the heap without limit:
the `m.blocks` slice and the per-block index maps (`mdCacheRendered`,
`toolDisplayCache`, `streamWraps`, `dirtyBlocks`) all keep references to evicted
content, so the GC can never reclaim it. Three mechanisms cap the worst case:

- **Scrollback compaction** — `compactBlocks(tuiMaxHistory)` runs whenever a new
  block is appended (`tool-start`, `usage`, `error`, `block`, and the new-block
  path of `appendOrAppend`). When `len(m.blocks)` exceeds the cap (500), the
  oldest blocks are dropped and every index-keyed map is rebuilt with shifted
  keys, which also frees the cached strings for the evicted blocks. The tool
  queue is reindexed in lockstep. The cost is O(cap) but only when the cap is
  exceeded — the streaming append-to-last-block path never triggers it.
- **Tool output cap** — `tuiMaxToolOutput` (1 MiB) bounds each tool block's raw
  `.output` buffer (the source shown in the click-to-expand modal). `appendTool`
  and `finishToolAt` trim to the tail at a line boundary, so a chatty tool
  (e.g. `bash` printing a 50 MB log) can't blow up the heap. The modal still
  shows the most recent output.
- **Subagent modal cap** — `tuiMaxModalBuffer` (1 MiB) bounds the modal streaming
  accumulator (`subagentModalContent`). A runaway child agent streaming forever
  would otherwise keep the builder growing until the modal is closed.

The compaction reindex is the subtle part: the maps are keyed by block index, so
after dropping `d` oldest blocks, the entry at key `k` must move to `k-d`, and
entries with `k < d` are dropped. `reindexBoolMap`/`reindexStringMap`/
`reindexStreamWrapMap` do this. If a still-open subagent modal's backing block is
evicted, the modal is closed and its buffer reset — otherwise the user would be
staring at a modal whose block no longer exists.

---

## 6. Configuration (environment variables)

| Variable | Default | Effect |
|---|---|---|
| `TYCI_TUI_PAINTER` | on | Set `0`/`false`/`off`/`no` to use bubbletea's standard renderer instead of the custom painter. |
| `TYCI_TUI_FPS` | 30 | Framerate for the **standard renderer only** (5–60). Directly sets its idle CPU floor. Ignored by the painter. |
| `TYCI_TUI_MOUSE` | on | Set `0`/`false`/`off`/`no` to disable mouse tracking (enables native terminal selection instead of the built-in drag-to-copy). |

---

## 7. Invariants worth protecting

- **View must stay a pure function of the model.** The painter (and any future
  memo) relies on this. Don't do I/O or time-based rendering inside `View()`.
- **The scroll-region hint must exclude the status bar and input.** They live
  below the message area; scrolling them in hardware would corrupt the screen.
  `paintScrollBottom()` returns 0 for overlays that don't scroll.
- **After a resize, call `painter.repaint()`** (done in `handleResizeFlush`): the
  previous frame's geometry is invalid, so force a full redraw.
- **Keep the terminal-restore path intact** (`painter.stop()` + `restoreTerm()` in
  `NewTUI`'s run goroutine). The nil renderer never restores anything, so if this
  is skipped the user's terminal is left in raw mode / alt screen on exit.
- **Every new-block path must call `compactBlocks(tuiMaxHistory)`.** The index
  maps are keyed by block position, so any append that can grow `m.blocks` past
  the cap must compact, or the maps leak entries for evicted blocks. The
  streaming append-to-last-block path is exempt (it never adds a block).
- **After compaction, index-keyed maps must be reindexed, not cleared.** Clearing
  would force a full re-render of every retained block on the next View; reindexing
  preserves the cached renders of blocks that survived.
