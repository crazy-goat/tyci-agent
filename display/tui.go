package display

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/stream"
)

const tuiMaxHistory = 500

type tuiMsgBlock struct {
	kind     string // "thinking","text","tool-start","tool-delta","tool-end","tool-progress","usage","error","done","block","set-model","reset","queue-drained"
	content  string
	toolName string
	toolIdx  int // for tool-progress: index in toolQueue
	usage    stream.Usage
	stats    stream.Stats
	// queuedLines: for "queue-drained", the messages that were drained
	// and delivered to the model (FIFO order). The TUI appends each as
	// a "You: …" block in the transcript at this point — when the model
	// actually sees them, not when the user typed them.
	queuedLines []string
}

type tuiInputSubmitted string

// tuiResumeRequestMsg is sent by TUI.OpenResumePicker to activate the
// /resume popup. The bubbletea event loop captures it inside Update() and
// activates the picker state (cursor at 0 = newest); on Enter/Esc, the
// key handler writes back to m.resumeCh — an unbuffered channel shared
// with the outer TUI so the caller's select on SelectedResume() can drive
// the resume flow in lock step with the user's key press.
type tuiResumeRequestMsg struct {
	entries []TuiResumeEntry // caller-supplied; sorted newest-first on the model side
}

// resizeFlushMsg is sent after a debounce delay to flush resize changes.
type resizeFlushMsg struct{}

// statusTickMsg triggers a status-bar repaint while a request is in flight,
// so the elapsed-time counter stays live.
type statusTickMsg struct{}

// ProviderModels groups model names under a provider name.
type ProviderModels struct {
	Name   string
	Models []string
}

// pickerItem represents a single row in the model picker popup.
type pickerItem struct {
	isHeader bool
	label    string // display text
	value    string // "provider/model" for model items, empty for headers
}

// TuiResumeEntry is the data shape the resume picker renders. Defined in
// the display package (instead of importing session.ResumeEntry directly)
// so the picker stays decoupled from the on-disk format and can be
// constructed by an outer caller — typically runTUI, which calls into the
// session package to enumerate the cwd's sessions. Path is the absolute
// file the picker will hand back via the SelectedResume channel; ModTime
// is rendered as the timestamp column; FirstPrompt is the column-2
// preview drawn from the first user message in the JSONL.
type TuiResumeEntry struct {
	Path        string
	Name        string
	ModTime     time.Time
	FirstPrompt string
}

type block struct {
	kind      string
	content   string
	toolName  string
	toolState string // "running","done"
	collapsed bool
	maxLines  int
	output    string        // full tool output (for modal), capped to tuiMaxToolOutput
	startTime time.Time     // when the tool was started (for duration display)
	duration  time.Duration // frozen duration when tool finished (0 = still running)

	// Markdown rendering cache (for "thinking" and "text" blocks)
	dirty bool // content changed since last render

	// Render caches
	cachedLineCount int      // number of display lines for this block (0 = not computed)
	cachedLines     []string // cached split lines (valid when cachedLineCount > 0)

	// Scrollback cache: when true, content/cachedLines/output were flushed to
	// scrollback.file and the in-memory copies are nil/empty. The block keeps
	// its identity (index, kind, toolName, lineCount) so scroll math and the
	// tool queue stay valid; the heavy fields are paged back in on demand by
	// ensureBlockResident. See tui_scrollback.go.
	flushed      bool
	flushedWidth int   // terminal width when the block was flushed (for resize re-wrap)
	fileOffset   int64 // byte offset of this block's rendered lines in the cache file
	fileBytes    int   // byte length of this block's rendered lines in the cache file
}

func defaultMaxLines(toolName string) int {
	switch toolName {
	case "read", "write":
		return 1
	case "bash":
		return 3
	case "subagent":
		return 1
	default:
		return 1
	}
}

type TuiModel struct {
	width, height int
	blocks        []block
	input         textarea.Model
	ready         bool
	quitting      bool
	lastUsage     stream.Usage
	lastStats     stream.Stats
	reading            bool
	requestStartTime   time.Time // set on submit, cleared on done/reset; used by buildStatus for live elapsed counter
	status             string   // "idle", "thinking", "responding", "tool"
	statusMessage      string   // transient user-facing status, e.g. copy result
	modelName     string // model name shown in status bar

	// Model switching (Tab/Shift+Tab) — uses favoriteModels when available.
	models          []string          // all available models (format: "provider/model")
	favoriteModels  []string          // favorite models for quick Tab/Shift+Tab cycling
	favoriteSet     map[string]bool   // set lookup for favorites (for picker rendering)
	onFavoriteToggled func(model string, favorite bool) // called when a favorite is toggled (persist to config)
	defaultModel    string            // default model (one, highlighted in picker)
	onDefaultChanged func(string)     // called when default model changes (persist to config)
	favIdx          int               // index of current model in favoriteModels slice
	modelIdx        int               // index of current model in models slice (for picker)
	modelChanges    chan<- string      // channel to notify outer TUI of model changes

	// Model picker (/model command)
	pickerActive bool
	pickerFilter string
	pickerCursor int              // index into pickerItems (only model entries)
	pickerItems  []pickerItem     // filtered list for display
	allProviders []ProviderModels // grouped provider->models for the picker

	// Resume picker (/resume command). Similar shape to the model picker:
	// shown as a full-screen popup with arrow-key navigation, Enter loads,
	// Esc closes without action. Entries are pre-resolved (cwd-derived) by
	// the caller so the picker only renders a sorted list and a chosen path
	// flows back over resumeCh — the model stays unaware of the on-disk
	// session dir layout. resumeCh is an unbuffered channel shared with the
	// outer TUI: a successful Enter sends the chosen path, an Esc sends "".
	// The channel header survives bubbletea's value-copy of the model on
	// every Update, so it's safe to read from this struct in the key handler.
	resumePickerActive  bool                                  // true while the popup is open
	resumePickerEntries []TuiResumeEntry                       // sorted (newest first) entries to list
	resumePickerCursor  int                                    // index into resumePickerEntries
	resumeCh            chan string                            // set once at construction; nil disables picker

	// Cancel signal: sent on when ESC pressed during agent run
	cancelCh chan<- struct{}

	// Scroll: offset in rendered lines from the bottom.
	// 0 = bottom (newest messages). Positive = scrolled up.
	scrollLine int
	atBottom   bool // true when user is at newest content (auto-scroll follows new content)

	// Saved scroll state before opening subagent modal (restored on close)
	savedScrollLine int
	savedAtBottom   bool

	// Subagent modal (live streaming output from child agents)
	subagentModalActive  bool
	subagentModalTitle   string           // task description (first ~60 chars)
	subagentModalContent *strings.Builder // accumulated output
	subagentModalScroll  int              // scroll offset within modal
	subagentModalToolIdx int              // tool queue index for this modal
	subagentModalDone    bool             // true when subagent finished (ESC to close)

	// Resize debounce
	resizePending bool // if true, a resize is pending debounce
	resizeWidth   int  // most recent resize width
	resizeHeight  int  // most recent resize height

	// Markdown render cache maps (keyed by block index)
	dirtyBlocks      map[int]bool        // block indices with content changed
	mdCacheRendered  map[int]string      // cached rendered ANSI output per block
	streamWraps      map[int]*streamWrap // incremental raw-wrap state during streaming
	toolDisplayCache map[int]string      // cached formatToolCall result per tool block index

	// Total line count cache (invalidated on block add/change/resize)
	cachedTotalLines int

	// Periodic OS memory release: freed slices/maps (e.g. dropped tool
	// output, flushed blocks) are only GC-eligible, not necessarily
	// returned to the OS. Count block-lifecycle events and nudge the
	// runtime every memFreeEveryBlocks so long sessions don't look like
	// they're leaking in RSS. See maybeFreeOSMemory.
	blockEventCount int

	// Message region cache (issue #84). The message region is the transcript
	// area between the top bar and the status bar. The status tick fires every
	// 250ms while a request is in flight; without this cache, every tick
	// rebuilds the full frame string even though the message region hasn't
	// changed. Pointer so it survives the value-copy bubbletea performs on
	// every Update (same pattern as scrollback and painter).
	messageRegion *messageRegionCache

	// Scrollback disk cache: keeps rendered lines for old blocks out of RAM.
	// Old blocks (beyond the resident window) are flushed to a temp file; their
	// in-memory content/cachedLines/output are nil and paged back in on demand
	// when the viewport scrolls up to them or a resize needs to re-wrap them.
	// See tui_scrollback.go.
	//
	// Pointer so the value-receiver TuiModel (bubbletea copies it on every
	// Update) never copies the cache's sync.Mutex.
	scrollback *scrollbackCache

	// Current working directory (set once at startup).
	cwd  string // absolute path of working directory
	home string // user home directory (for ~ shortening)

	// Metadata for the currently visible transcript lines. Built by View().
	renderBuffer      RenderBuffer
	modalRenderBuffer RenderBuffer

	// Custom event-driven renderer. Always set by NewTUI in production; nil only
	// in tests that build a model without wiring a terminal. When non-nil, View()
	// hands each frame to it. See tui_painter.go.
	painter *painter

	// Mouse text selection over visible transcript lines.
	selection        SelectionState
	selectionVersion int
	selectionFlash   bool // briefly true after successful copy, for visual feedback

	submitResult chan<- string
	toolQueue    []int // FIFO of block indices for ToolCallStart->ToolCallEnd matching

	// Input history for Up/Down navigation
	inputHistory []string
	historyIdx   int    // -1 = current input, 0..len-1 = browsing history
	stashedInput string // saved current input while browsing history
	historyPath  string // path to history file for persistence

	// Ctrl+R history search modal
	historySearchActive  bool     // true when the search modal is open
	historySearchFilter  string   // live filter substring
	historySearchCursor  int      // index into historySearchResults
	historySearchResults []string // filtered matching history entries
	stashedSearchInput   string   // textarea value preserved for Esc cancel

	// Pending-message queue snapshot (issue #88). Updated synchronously by
	// the bubbletea event loop on submit (when busy) and on ESC/reset. The
	// agent goroutine drains the actual channel (TUI.queue) via the
	// NextMessages callback; this slice is purely for rendering.
	queueItems []string

	// queue is the shared pending-message channel set by NewTUI after this
	// model is constructed. bubbletea copies the model on every Update, but
	// the channel reference is stable so all copies share the same backing
	// channel. nil in unit tests that never call submit() while busy.
	queue chan string

	// Todo list modal (shown when clicking todos counter in top bar)
	todoModalActive bool
	todoModalScroll int

	// Context counts for the top status bar (computed in commands.go and passed in).
	toolCount  int // total tools available (built-in + Lua + MCP)
	skillCount int // skills loaded from skills directory
	mcpCount   int // MCP tools available
}

func newModel(submitResult chan<- string, modelName string, historyPath string, models []string, modelChanges chan<- string, allProviders []ProviderModels, cancelCh chan<- struct{}, favoriteModels []string, onFavoriteToggled func(model string, favorite bool), defaultModel string, onDefaultChanged func(string), toolCount int, skillCount int, mcpCount int) TuiModel {
	ta := textarea.New()
	ta.Placeholder = "Type message (Enter send, Alt+Enter / Ctrl+N newline)"
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.SetWidth(80)
	ta.SetHeight(1)
	focusedStyle, blurredStyle := textarea.DefaultStyles()
	focusedStyle.CursorLine = lipgloss.NewStyle()
	blurredStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	ta.FocusedStyle = focusedStyle
	ta.BlurredStyle = blurredStyle
	ta.Focus()

	// Disable cursor blinking to keep the TUI idle at 0% CPU.
	// Blinking causes the renderer to re-render View() every ~530ms.
	ta.Cursor.SetMode(cursor.CursorStatic)

	modelIdx := 0
	for i, m := range models {
		if m == modelName {
			modelIdx = i
			break
		}
	}
	favIdx := 0
	for i, m := range favoriteModels {
		if m == modelName {
			favIdx = i
			break
		}
	}
	favoriteSet := make(map[string]bool, len(favoriteModels))
	for _, m := range favoriteModels {
		favoriteSet[m] = true
	}

	return TuiModel{
		blocks:               make([]block, 0, 1024),
		input:                ta,
		submitResult:         submitResult,
		ready:                true,
		reading:              true,
		toolQueue:            make([]int, 0, 16),
		modelName:            modelName,
		models:               models,
		favoriteModels:       favoriteModels,
		favoriteSet:          favoriteSet,
		onFavoriteToggled:    onFavoriteToggled,
		defaultModel:         defaultModel,
		onDefaultChanged:     onDefaultChanged,
		favIdx:               favIdx,
		modelIdx:             modelIdx,
		modelChanges:         modelChanges,
		allProviders:         allProviders,
		cancelCh:             cancelCh,
		atBottom:             true,
		savedAtBottom:        true,
		inputHistory:         loadTuiHistory(historyPath),
		historyIdx:           -1,
		historyPath:          historyPath,
		subagentModalToolIdx: -1,
		subagentModalContent: &strings.Builder{},
		dirtyBlocks:          make(map[int]bool),
		mdCacheRendered:      make(map[int]string),
		streamWraps:          make(map[int]*streamWrap),
		toolDisplayCache:     make(map[int]string),
		cachedTotalLines:     -1,
		scrollback:           &scrollbackCache{},
		messageRegion:        &messageRegionCache{},
		toolCount:            toolCount,
		skillCount:           skillCount,
		mcpCount:             mcpCount,
	}
}
