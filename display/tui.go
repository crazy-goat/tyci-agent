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
	kind     string // "thinking","text","tool-start","tool-delta","tool-end","tool-progress","usage","error","done","block","set-model","reset"
	content  string
	toolName string
	toolIdx  int // for tool-progress: index in toolQueue
	usage    stream.Usage
	stats    stream.Stats
}

type tuiInputSubmitted string

// resizeFlushMsg is sent after a debounce delay to flush resize changes.
type resizeFlushMsg struct{}

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
	case "read", "write", "edit":
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
	reading       bool
	status        string // "idle", "thinking", "responding", "tool"
	statusMessage string // transient user-facing status, e.g. copy result
	modelName     string // model name shown in status bar

	// Model switching (Tab/Shift+Tab)
	models       []string      // available models (format: "provider/model")
	modelIdx     int           // index of current model in models slice
	modelChanges chan<- string // channel to notify outer TUI of model changes

	// Model picker (/model command)
	pickerActive bool
	pickerFilter string
	pickerCursor int              // index into pickerItems (only model entries)
	pickerItems  []pickerItem     // filtered list for display
	allProviders []ProviderModels // grouped provider->models for the picker

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

	// Scrollback disk cache: keeps rendered lines for old blocks out of RAM.
	// Old blocks (beyond the resident window) are flushed to a temp file; their
	// in-memory content/cachedLines/output are nil and paged back in on demand
	// when the viewport scrolls up to them or a resize needs to re-wrap them.
	// See tui_scrollback.go.
	//
	// Pointer so the value-receiver TuiModel (bubbletea copies it on every
	// Update) never copies the cache's sync.Mutex.
	scrollback *scrollbackCache

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
}

func newModel(submitResult chan<- string, modelName string, historyPath string, models []string, modelChanges chan<- string, allProviders []ProviderModels, cancelCh chan<- struct{}) TuiModel {
	ta := textarea.New()
	ta.Placeholder = "Type message (Enter send, Alt+Enter / Ctrl+N newline)"
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.SetWidth(80)
	ta.SetHeight(3)
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

	return TuiModel{
		blocks:               make([]block, 0, 1024),
		input:                ta,
		submitResult:         submitResult,
		ready:                true,
		reading:              true,
		toolQueue:            make([]int, 0, 16),
		modelName:            modelName,
		models:               models,
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
	}
}
