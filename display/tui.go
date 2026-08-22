package display

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/jobs"
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
	// duration: for "tool-end", how long that individual tool call took, as
	// measured by the dispatcher. Zero means "not reported" — the block then
	// falls back to timing from its own start, which for a batch of parallel
	// calls is the whole batch's wall-clock.
	duration time.Duration
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

// tuiBtwOpenMsg registers a new /btw entry and opens its live modal
// immediately — before the background job has produced any output. Sent by
// TUI.OpenBtw, strictly before the caller starts the job's goroutine, so
// Update always registers the entry before any tuiBtwStreamMsg for the same
// id can arrive.
type tuiBtwOpenMsg struct {
	id        string
	question  string
	createdAt time.Time
}

// tuiBtwJobIDMsg records the background job's ID on an already-open /btw
// entry, once tools.JobRegistry.Start has returned one. Delivered as its own
// message (like every other cross-goroutine mutation here) rather than
// written directly to the entry, so the field is only ever touched from the
// single bubbletea event-loop goroutine.
type tuiBtwJobIDMsg struct {
	id    string
	jobID string
}

// tuiBtwStreamMsg carries streamed output for a /btw entry, keyed by id.
// kind is one of "text", "thinking", "tool", "block", "error", "done".
type tuiBtwStreamMsg struct {
	id      string
	kind    string
	content string
}

// tuiBtwListOpenMsg opens the /btw list popup (bare "/btw").
type tuiBtwListOpenMsg struct{}

// resizeFlushMsg is sent after a debounce delay to flush resize changes.
type resizeFlushMsg struct{}

// statusTickMsg triggers a status-bar repaint while a request is in flight,
// so the elapsed-time counter stays live.
type statusTickMsg struct{}

// tuiMsgJobUpdate is sent by TUI.SetJobEventBus's subscriber goroutine
// whenever jobs.Registry reports a background job's status changed (started
// running, or reached a terminal status). Handled unconditionally at the top
// of Update() — see tui_update.go — so backgroundJobs stays current no
// matter which overlay (if any) is active.
type tuiMsgJobUpdate struct {
	Job jobs.Job
}

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

// BtwEntry records one /btw side-conversation forked from the main thread
// during this TUI session. In-memory only — it does not survive a restart —
// and its content is never merged back into the main conversation.
//
// content is a pointer (like block.output) so a TuiModel value copy
// (bubbletea copies the model on every Update) never copies-after-write a
// strings.Builder, which panics. All fields are mutated exclusively from the
// bubbletea event-loop goroutine, driven by tuiBtwOpenMsg/tuiBtwJobIDMsg/
// tuiBtwStreamMsg — never written directly from the goroutine running the
// background job, which only ever posts messages through BtwSink.
type BtwEntry struct {
	ID        string
	Question  string
	JobID     string
	CreatedAt time.Time

	content *strings.Builder
	done    bool
	errMsg  string
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
	width, height    int
	blocks           []block
	input            textarea.Model
	ready            bool
	quitting         bool
	lastUsage        stream.Usage
	lastStats        stream.Stats
	reading          bool
	requestStartTime time.Time // set on submit, cleared on done/reset; used by buildStatus for live elapsed counter
	status           string    // "idle", "thinking", "responding", "tool"
	statusMessage    string    // transient user-facing status, e.g. copy result
	modelName        string    // model name shown in status bar

	// Model switching (Tab/Shift+Tab) — uses favoriteModels when available.
	models            []string                          // all available models (format: "provider/model")
	favoriteModels    []string                          // favorite models for quick Tab/Shift+Tab cycling
	favoriteSet       map[string]bool                   // set lookup for favorites (for picker rendering)
	onFavoriteToggled func(model string, favorite bool) // called when a favorite is toggled (persist to config)
	defaultModel      string                            // default model (one, highlighted in picker)
	onDefaultChanged  func(string)                      // called when default model changes (persist to config)
	favIdx            int                               // index of current model in favoriteModels slice
	modelIdx          int                               // index of current model in models slice (for picker)
	modelChanges      chan<- string                     // channel to notify outer TUI of model changes

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
	resumePickerActive  bool             // true while the popup is open
	resumePickerEntries []TuiResumeEntry // sorted (newest first) entries to list
	resumePickerCursor  int              // index into resumePickerEntries
	resumeCh            chan string      // set once at construction; nil disables picker

	// Cancel signal: sent on when ESC pressed during agent run
	cancelCh chan<- struct{}

	// Scroll: offset in rendered lines from the bottom.
	// 0 = bottom (newest messages). Positive = scrolled up.
	scrollLine int
	atBottom   bool // true when user is at newest content (auto-scroll follows new content)

	// Saved scroll state before opening subagent modal (restored on close)
	savedScrollLine int
	savedAtBottom   bool

	// Tool output modal (live subagent stream and click-to-expand tool output).
	// The modal owns no content of its own: it is a view onto one tool block's
	// .output buffer (subagentModalBlockIdx). Every tool's progress accumulates
	// in its own block, so opening/closing the modal or starting another tool
	// can never drop what a block already collected.
	subagentModalActive   bool
	subagentModalTitle    string // task description (first ~60 chars)
	subagentModalBlockIdx int    // block index the modal renders (-1 = none)
	subagentModalScroll   int    // scroll offset within modal
	subagentModalDone     bool   // true when the viewed tool finished (Enter to close)
	// subagentModalStaticText backs the modal when subagentModalBlockIdx < 0
	// — content not tied to any tool block, e.g. a finished background job's
	// Result/Err shown from the jobs modal (Ctrl+B). See subagentModalText.
	subagentModalStaticText string

	// Queue index of the most recent "subagent" tool call. Used only to route
	// streamed argument deltas to the right block when the subagent is not the
	// last started tool; content routing goes through msg.toolIdx.
	subagentToolIdx int

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

	// Metadata for the currently visible transcript lines. Built by
	// buildMessageRegion/the modal render functions, which run through a
	// pointer receiver on a value copy renderFrame()/View() made (View
	// itself is a value receiver, per bubbletea's Model interface) — so
	// like messageRegion/scrollback above, this must be a pointer or every
	// write is discarded when that copy goes out of scope, leaving
	// blockAtVisibleLine's fast path permanently empty and click handling
	// silently falling back to a second, independently recomputed mapping
	// that can disagree with what was actually drawn (wrong tool block
	// opens on click).
	renderBuffer      *RenderBuffer
	modalRenderBuffer *RenderBuffer

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

	// "@" file-path completion (see tui_filecomplete.go). The index is built
	// off the UI thread on first use and refreshed on a TTL, so a file the
	// agent just created becomes completable without a restart.
	fileCompleteActive bool
	fileCompleteItems  []string
	fileCompleteCursor int
	fileCompleteAt     int // byte offset of the "@" being completed
	fileCompleteEnd    int // byte offset just past the query (the cursor)
	fileIndex          []string
	fileIndexBuiltAt   time.Time
	fileIndexPending   bool
	fileIndexTruncated bool

	// Pending-message queue snapshot (issue #88). Updated synchronously by
	// the bubbletea event loop on submit (when busy) and on ESC/reset. The
	// agent goroutine drains the actual channel (TUI.queue) via the
	// NextMessages callback; this slice is purely for rendering.
	queueItems []string

	// commands carries the slash commands the main loop owns (see
	// handleLocalSlashCommand) when it is not reading, i.e. while a turn is in
	// flight. Without it those lines were queued as prompts and the model was
	// asked to interpret a command meant for the interface.
	commands chan string

	// queue is the shared pending-message channel set by NewTUI after this
	// model is constructed. bubbletea copies the model on every Update, but
	// the channel reference is stable so all copies share the same backing
	// channel. nil in unit tests that never call submit() while busy.
	queue chan string

	// Todo list modal (shown when clicking todos counter in top bar)
	todoModalActive bool
	todoModalScroll int

	// /btw side-conversations: entries recorded this session (newest last),
	// the live/preview modal for one entry, and the list popup for browsing
	// them all. See btw.go (main package) for how a job is started, and
	// tui_btw.go for the update/render logic.
	btwEntries     []*BtwEntry
	btwModalActive bool
	btwModalEntry  *BtwEntry
	btwModalScroll int
	btwListActive  bool
	btwListCursor  int

	// Background jobs: async subagent jobs registered via jobs.Registry and
	// mirrored here through the eventbus subscription set up by
	// TUI.SetJobEventBus (see tui_jobs_api.go). nil-safe map; empty (or
	// never wired) means the inline panel and Ctrl+B modal render nothing,
	// so users who never touch subagent(async: true) see no UI change.
	backgroundJobs  map[string]jobs.Job
	jobsModalActive bool
	jobsModalCursor int // index into the sorted (newest-first) job list

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
		blocks:                make([]block, 0, 1024),
		input:                 ta,
		submitResult:          submitResult,
		ready:                 true,
		reading:               true,
		toolQueue:             make([]int, 0, 16),
		modelName:             modelName,
		models:                models,
		favoriteModels:        favoriteModels,
		favoriteSet:           favoriteSet,
		onFavoriteToggled:     onFavoriteToggled,
		defaultModel:          defaultModel,
		onDefaultChanged:      onDefaultChanged,
		favIdx:                favIdx,
		modelIdx:              modelIdx,
		modelChanges:          modelChanges,
		allProviders:          allProviders,
		cancelCh:              cancelCh,
		atBottom:              true,
		savedAtBottom:         true,
		inputHistory:          loadTuiHistory(historyPath),
		historyIdx:            -1,
		historyPath:           historyPath,
		subagentToolIdx:       -1,
		subagentModalBlockIdx: -1,
		dirtyBlocks:           make(map[int]bool),
		mdCacheRendered:       make(map[int]string),
		streamWraps:           make(map[int]*streamWrap),
		toolDisplayCache:      make(map[int]string),
		cachedTotalLines:      -1,
		scrollback:            &scrollbackCache{},
		messageRegion:         &messageRegionCache{},
		renderBuffer:          &RenderBuffer{},
		modalRenderBuffer:     &RenderBuffer{},
		backgroundJobs:        make(map[string]jobs.Job),
		toolCount:             toolCount,
		skillCount:            skillCount,
		mcpCount:              mcpCount,
	}
}
