package display

import (
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/decodo/tyci/jobs"
)

// statusTickInterval is the interval between status-bar repaints while a
// request is in flight. 250ms reduces idle CPU compared to the previous
// 100ms (issue #83) while still providing smooth elapsed-time updates with
// 0.1s precision.
const statusTickInterval = 250 * time.Millisecond

// statusTickCmd returns a command that ticks every statusTickInterval while
// a request is in flight, keeping the elapsed-time counter in the status bar
// live.
func statusTickCmd() tea.Cmd {
	return tea.Tick(statusTickInterval, func(time.Time) tea.Msg {
		return statusTickMsg{}
	})
}

func (m TuiModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m TuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handled first and unconditionally: a background job can finish while
	// any overlay (subagent modal, picker, …) is active, and backgroundJobs
	// must stay current regardless of what's on screen.
	if reset, ok := msg.(tuiMsgJobsReset); ok {
		m.backgroundJobs = make(map[string]jobs.Job)
		if m.ignoredJobIDs == nil {
			m.ignoredJobIDs = make(map[string]bool)
		}
		for _, id := range reset.jobIDs {
			m.ignoredJobIDs[id] = true
		}
		m.invalidateTotalLines()
		return m, nil
	}
	if upd, ok := msg.(tuiMsgJobUpdate); ok {
		m.applyJobUpdate(upd.Job)
		return m, nil
	}
	// A finished file scan (tui_filecomplete.go) is pure state: it must land
	// whatever overlay happens to be open, or the popup would sit on
	// "scanning…" until the next keystroke.
	if idx, ok := msg.(tuiFileIndexMsg); ok {
		m.applyFileIndex(idx)
		return m, nil
	}
	// The Sidebar's Sessions tab callback (see TUI.SetSessionLister) can
	// arrive at any point in startup, regardless of what's on screen.
	if sl, ok := msg.(tuiSetSessionListerMsg); ok {
		m.sessionLister = sl.fn
		return m, nil
	}
	if tp, ok := msg.(tuiSetTranscriptProviderMsg); ok {
		m.transcriptProvider = tp.fn
		return m, nil
	}
	// Same delivery pattern, same "can arrive at any point in startup"
	// guarantee: the sidebar persistence callback (TUI.SetSidebarPersister).
	if sp, ok := msg.(tuiSetSidebarPersisterMsg); ok {
		sidebarSaveVisible = sp.fn
		return m, nil
	}
	// Background stream blocks must never be lost just because some other
	// popup happens to be open when they arrive. Dispatch them unconditionally,
	// ahead of every exclusivity check below, just like /btw messages.
	if block, ok := msg.(tuiMsgBlock); ok {
		m.handleBlockMsg(block)
		return m, nil
	}
	// /btw entries run independently of the main view and likewise must land
	// regardless of which overlay is active.
	switch msg.(type) {
	case tuiBtwOpenMsg, tuiBtwJobIDMsg, tuiBtwStreamMsg:
		return m.updateBtwMsg(msg)
	}
	if m.btwListActive {
		return m.updateBtwList(msg)
	}
	if m.btwModalActive {
		return m.updateBtwModal(msg)
	}
	if m.historySearchActive {
		return m.updateHistorySearch(msg)
	}
	if m.resumePickerActive {
		return m.updateResumePicker(msg)
	}
	if m.pickerActive {
		return m.updatePicker(msg)
	}
	if m.todoModalActive {
		return m.updateTodoModal(msg)
	}
	if m.jobsModalActive {
		return m.updateJobsModal(msg)
	}
	if m.transcriptViewerActive {
		return m.updateTranscriptViewer(msg)
	}
	if m.sidebarActive {
		if handled, model, cmd := m.routeSidebarMsg(msg); handled {
			return model, cmd
		}
		// Falls through to the normal (non-sidebar) handling below: the
		// sidebar is open but unfocused (m.sidebarFocused == false), and
		// this message type isn't one routeSidebarMsg claims outright
		// (WindowSizeMsg, MouseMsg, or a focused KeyMsg) — so the
		// conversation keeps behaving exactly like the sidebar wasn't open
		// (typing lands in the input, ticks/streamed blocks still land,
		// etc.). See tui_sidebar.go's package doc comment for the focus
		// state machine this implements.
	}
	if m.subagentModalActive {
		return m.updateSubagentModal(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleResize(msg)
	case resizeFlushMsg:
		return m.handleResizeFlush()
	case selectionFlashDoneMsg:
		m.selectionFlash = false
		return m, nil
	case selectionAutoCopyMsg:
		if msg.version == m.selectionVersion && m.selection.Active {
			m = m.copySelection()
			return m, copyFeedbackCmd(m)
		}
		return m, nil
	case statusTickMsg:
		// Keep ticking while request is in flight; stop when idle.
		if !m.reading {
			return m, statusTickCmd()
		}
		return m, nil
	case statusMessageClearMsg:
		if m.statusMessage == msg.message {
			m.statusMessage = ""
		}
		return m, nil
	case tuiBtwListOpenMsg:
		m.openBtwList()
		return m, nil
	case tuiResumeRequestMsg:
		// /resume opened (typically bubbles in while reading=true). Make
		// sure no active model-picker is also active — the two popup
		// modes are mutually exclusive for the centered overlay.
		m.openResumePicker(msg.entries)
		return m, nil
	case tea.KeyMsg:
		return m.handleKeyMsg(msg)
	case tuiMsgBlock:
		m.handleBlockMsg(msg)
		return m, nil
	case tea.MouseMsg:
		return m.handleMouseMsg(msg)
	}
	return m, nil
}

func (m TuiModel) handleResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	// mainColumnWidth(), not raw msg.Width: with the sidebar open, the
	// input's real column is the narrowed main column, not the full
	// terminal — using msg.Width here undid F20's fix on the very next
	// resize (m.input.Width() would jump back to the full-terminal value
	// even though renderWidth()/mainColumnWidth() had already narrowed).
	// capInputHeight follows the width change: SetWidth alone does not
	// recompute how many rows the textarea needs (that is capInputHeight's
	// job, tui_input.go) — without it the input renders too few/many rows
	// for its own new wrap until the next keystroke happens to trigger it.
	m.input.SetWidth(max(10, m.mainColumnWidth()-2))
	m.capInputHeight()
	m.resizeWidth = msg.Width
	m.resizeHeight = msg.Height
	// F14: m.width takes effect immediately (renderWidth/mainColumnWidth
	// read it live), but the width-keyed streaming caches used to stay
	// untouched until handleResizeFlush fired ~100ms later, so a line
	// cached at the old width could be emitted at the new renderWidth() and
	// get shredded by buildViewportRows' overlong-line safety net. An
	// earlier version of this fix called the FULL invalidateAllBlockLineCounts
	// here on every resize event, which a review round measured at up to
	// ~37x the per-event cost (200 blocks: 1.3ms -> 49ms) — the expensive
	// part is not the invalidation itself but cachedTotalLines=-1 forcing
	// totalRenderedLines' very next call (View() runs right after every
	// Update()) to sweep every resident block and page every FLUSHED one
	// back in from disk, on every intermediate resize event during a drag,
	// not once per gesture. invalidateDirtyBlockWidthCaches (tui_scroll.go)
	// is the narrower, genuinely cheap fix: it only clears the width-keyed
	// caches for blocks actively streaming right now (typically 0 or 1),
	// which is the only place a stale-width line can visibly reappear
	// before the debounce settles. The full invalidateAllBlockLineCounts
	// still runs, unchanged, in handleResizeFlush below — clampScroll's
	// recompute and the painter's full-screen repaint stay debounced there
	// too, so a fast resize drag still only pays the expensive path once.
	m.invalidateDirtyBlockWidthCaches()
	if !m.resizePending {
		m.resizePending = true
		return m, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
			return resizeFlushMsg{}
		})
	}
	return m, nil
}

func (m TuiModel) handleResizeFlush() (tea.Model, tea.Cmd) {
	if m.resizePending {
		m.resizePending = false
		if m.width != m.resizeWidth || m.height != m.resizeHeight {
			m.width = m.resizeWidth
			m.height = m.resizeHeight
			m.input.SetWidth(max(10, m.mainColumnWidth()-2))
			m.capInputHeight()
		}
		// Unconditional (not just on the width/height mismatch above):
		// m.width/m.resizeWidth are already synced by handleResize on every
		// event, so that branch is rarely taken — this is where the full,
		// correctness-complete cache invalidation for EVERY block (not just
		// the actively-streaming ones handleResize covers immediately) and
		// the total-line recompute actually happen, exactly once per resize
		// gesture.
		m.invalidateAllBlockLineCounts()
		m.clampScroll()
		if m.painter != nil {
			// Geometry changed; force a clear+full redraw so no stale cells
			// from the previous size linger.
			m.painter.repaint()
		}
	}
	return m, nil
}

func (m *TuiModel) switchModel(delta int) {
	list := m.favoriteModels
	if len(list) == 0 {
		list = m.models
	}
	if len(list) == 0 {
		return
	}
	// Include the current model in the cycle even if it isn't a favorite, so
	// Tab always starts from where you are — without persisting it as one.
	inList := false
	cur := 0
	for i, mm := range list {
		if mm == m.modelName {
			inList = true
			cur = i
			break
		}
	}
	if !inList {
		list = append([]string{m.modelName}, list...)
		cur = 0
	}
	m.favIdx = (cur + delta + len(list)) % len(list)
	newModel := list[m.favIdx]
	m.modelName = newModel
	if m.modelChanges != nil {
		select {
		case m.modelChanges <- newModel:
		default:
		}
	}
}
