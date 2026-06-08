# TUI Text Selection and Clipboard Plan

## Problem

The current TUI enables Bubble Tea mouse tracking:

```go
tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
```

This lets the app receive mouse events for wheel scrolling and clicking tool blocks, but it also prevents the terminal emulator from doing normal mouse text selection. Users must rely on terminal-specific bypass modifiers:

- Linux terminals usually use `Shift + drag`.
- macOS Terminal/iTerm2 often use `Option/Alt + drag`.
- Some terminals or settings make this unreliable.

The goal is to make text copying reliable without depending on terminal-native selection behavior.

## Goals

1. Allow users to select and copy text inside the TUI using normal mouse drag.
2. Keep existing mouse features:
   - wheel scrolling,
   - clicking tool blocks,
   - modal interaction.
3. Support macOS and Linux clipboard backends.
4. Avoid a risky full rewrite of rendering in one step.
5. Build toward precise character-level selection, but start with a stable line-level implementation.

## Non-goals for the first version

- Pixel-perfect terminal-native selection behavior.
- Selecting from the input textarea.
- Auto-scroll while dragging outside the viewport.
- Perfect handling of every Unicode width edge case.
- Selection across hidden/off-screen history in the first iteration.

## Proposed Approach

Implement selection in phases.

The important architectural change is to introduce a render buffer: instead of treating the TUI view as only a final string, rendering should also produce metadata for visible lines. Mouse coordinates can then be mapped back to rendered text lines.

## Phase 1: Render Buffer

Add a structure representing visible rendered lines.

Example:

```go
type RenderLine struct {
    Text       string
    PlainText  string
    SourceKind string // assistant, user, tool, thinking, error, modal, status, input
    BlockIndex int
    SourceLine int
    Y          int
}
```

Add a render result structure:

```go
type RenderBuffer struct {
    Lines []RenderLine
}
```

The normal `View()` path should continue returning a string, but internally it should also build a `RenderBuffer` and store it on the model for mouse hit-testing and copy operations.

### Requirements

- The render buffer must represent the currently visible screen lines.
- It should avoid ANSI/style codes in `PlainText`.
- It should work with the main transcript view first.
- Modal support can be added separately but should use the same abstraction.

### Expected result

No user-visible behavior change yet. Existing tests should still pass.

## Phase 2: Line-level Mouse Selection

Add model state for selection:

```go
type SelectionState struct {
    Active bool
    AnchorY int
    CursorY int
    Finalized bool
}
```

Handle mouse events:

- `MouseActionPress` with left button starts selection.
- `MouseActionMotion` updates selection.
- `MouseActionRelease` finalizes selection.
- `Esc` clears selection.
- `y` or `Ctrl+Y` copies selected text.

Selection should initially work at whole-line granularity.

Example behavior:

- Drag from line 5 to line 10 selects full visible lines 5 through 10.
- Drag upward works too.
- Wheel scrolling while a selection exists should either clear the selection or keep it only if the selected lines remain mapped correctly. Prefer clearing selection initially.

### Conflict with existing tool click

Current behavior opens a tool modal on left click. Selection must avoid breaking this.

Suggested rule:

- Mouse press records a possible click and possible selection start.
- If the mouse is released on the same cell/line without motion, treat it as a click and keep existing tool modal behavior.
- If there is drag motion, treat it as selection and do not open a modal.

### Expected result

Users can drag normal mouse button without Shift/Alt to select visible lines in the TUI.

## Phase 3: Selection Highlight

Render selected lines with a dedicated style.

Example:

```go
var selectionStyle = lipgloss.NewStyle().
    Background(lipgloss.Color("63")).
    Foreground(lipgloss.Color("230"))
```

For line-level selection, highlight the whole selected line.

### Requirements

- Highlight must not corrupt layout width.
- Highlight should work in the main transcript view.
- Modal highlighting can be added later.

### Expected result

User sees what will be copied.

## Phase 4: Clipboard Backend

Add an internal clipboard helper.

Preferred order:

1. OSC52 terminal clipboard sequence, when safe/enabled.
2. macOS: `pbcopy`.
3. Wayland: `wl-copy`.
4. X11: `xclip -selection clipboard`.
5. X11 fallback: `xsel --clipboard --input`.

Example API:

```go
func CopyToClipboard(text string) error
```

Potential package location:

```text
internal/clipboard
```

### Platform notes

macOS:

```sh
printf '%s' "$text" | pbcopy
```

Linux Wayland:

```sh
printf '%s' "$text" | wl-copy
```

Linux X11:

```sh
printf '%s' "$text" | xclip -selection clipboard
```

Fallback:

```sh
printf '%s' "$text" | xsel --clipboard --input
```

OSC52 can work on macOS, Linux, and over SSH, but some terminals disable it or require explicit configuration. Treat it as optional or configurable.

### Expected result

Selected text can be copied without relying on terminal-native selection.

## Phase 5: Copy Commands Without Selection

Add convenient copy shortcuts even when no selection exists:

- `Ctrl+Y`: copy selected text if selection exists, otherwise copy last assistant response.
- `Alt+Y` or `/copy visible`: copy visible transcript lines.
- `/copy last`: copy last assistant response.
- `/copy all`: copy full conversation transcript.
- In tool modal: `y` copies current tool output.

These commands reduce the need for manual selection.

## Phase 6: Modal Support

Extend render buffer and selection to subagent/tool modals.

Rules:

- Selection inside modal should copy modal content only.
- Main transcript selection should be cleared when opening a modal.
- `y` in modal copies selected modal text if present, otherwise full modal output.

## Phase 7: Character-level Selection

After line-level selection is stable, add precise character-level selection.

Extend state:

```go
type SelectionPoint struct {
    Y int
    X int
}

type SelectionState struct {
    Active bool
    Anchor SelectionPoint
    Cursor SelectionPoint
    Finalized bool
}
```

Required work:

- Map terminal `x` coordinate to rune offset.
- Account for wide Unicode characters.
- Strip ANSI/lipgloss style codes before calculating offsets.
- Preserve correct text when selecting wrapped lines.
- Highlight only selected spans, not whole lines.

Recommended helper:

```go
func RuneOffsetForCell(text string, cellX int) int
```

Use a width library compatible with Bubble Tea/lipgloss behavior.

### Expected result

Selection behaves closer to native terminal selection.

## Phase 8: Optional Features

Potential follow-ups:

- Auto-scroll while dragging near top/bottom.
- Persistent selection across scroll.
- Keyboard selection mode:
  - `v` enters selection mode,
  - arrows move cursor,
  - `y` copies,
  - `Esc` cancels.
- Config flag to disable mouse tracking entirely:
  - `--no-mouse`,
  - `TYCI_TUI_MOUSE=0`.
- Better help text depending on OS/terminal:
  - Linux: `Shift+drag may use terminal-native selection`.
  - macOS: `Option+drag may use terminal-native selection`.

## Implementation Order

Recommended order:

1. Add render buffer with no behavior change.
2. Add selection state and mouse drag detection.
3. Add line-level selected-text extraction.
4. Add visual highlight.
5. Add clipboard helper.
6. Add `y` / `Ctrl+Y` copy action.
7. Add copy commands for last/visible/all.
8. Add modal support.
9. Add character-level selection.
10. Add optional no-mouse mode and OS-specific hints.

## Testing Plan

### Unit tests

- Render buffer line count matches visible output.
- Selection range normalization works for downward and upward drags.
- Selected text extraction joins lines correctly.
- Empty selection returns empty string.
- Clipboard backend chooses expected command based on OS/env.
- Click without drag still opens tool modal.
- Drag over tool block does not open modal.

### Manual tests

macOS Terminal.app:

- Drag selects TUI lines without Option.
- `y` copies selection to clipboard.
- Existing wheel scroll still works.
- Clicking tool block still opens modal.

macOS iTerm2:

- Same as above.

Linux X11 terminal:

- Drag selects TUI lines without Shift.
- Clipboard works with `xclip` or `xsel`.

Linux Wayland terminal:

- Clipboard works with `wl-copy`.

SSH / remote terminal:

- OSC52 behavior checked if enabled.
- Fallback errors are user-friendly.

## Risks

1. Rendering refactor can accidentally change layout.
2. Styled strings can make coordinate mapping hard.
3. Unicode width handling is easy to get wrong.
4. Clipboard command availability varies by system.
5. Mouse drag may conflict with existing click behavior.

## Risk Mitigation

- Start with line-level selection only.
- Keep `View()` output unchanged in Phase 1.
- Add tests around click-vs-drag behavior.
- Keep clipboard backend isolated.
- Add clear status/error messages when copy fails.

## Suggested MVP

The best first deliverable:

- line-level mouse selection over visible transcript lines,
- visual highlight,
- `y` / `Ctrl+Y` copies selection,
- clipboard support for `pbcopy`, `wl-copy`, `xclip`, `xsel`,
- click-vs-drag preservation for tool blocks.

This solves most of the real user pain on macOS and Linux without requiring a risky full character-level selection implementation immediately.
