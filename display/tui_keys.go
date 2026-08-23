package display

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m TuiModel) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The "@" file-path popup claims Up/Down/Tab/Enter/Esc while it is open,
	// and it has to be asked first: Tab below switches model, and the global
	// handler binds the arrows to history. See tui_filecomplete.go.
	if m.handleFileCompleteKey(msg) {
		return m, nil
	}

	switch msg.Type {
	case tea.KeyTab:
		m.switchModel(1)
		return m, nil
	case tea.KeyShiftTab:
		m.switchModel(-1)
		return m, nil
	}

	if handled, model, cmd := m.handleGlobalKey(msg); handled {
		return model, cmd
	}
	if !m.reading {
		return m.handleKeyWhileBusy(msg)
	}

	switch msg.Type {
	case tea.KeyCtrlR:
		m.openHistorySearch()
		return m, nil
	case tea.KeyEscape:
		if m.selection.Active || m.selection.Candidate {
			return m.clearSelection(), nil
		}
		m.input.Reset()
		m.closeFileComplete()
		return m, nil
	case tea.KeyEnter:
		if msg.Alt {
			// Pre-set height so that repositionView inside the textarea's
			// Update already uses the correct viewport height. Without this,
			// repositionView runs with the old (smaller) height, decides the
			// new cursor line is out of view, scrolls down, and then
			// SetHeight in capInputHeight can't undo that scroll — the first
			// line of input disappears.
			newH := m.input.LineCount() + 1
			if newH < 1 {
				newH = 1
			} else if newH > 10 {
				newH = 10
			}
			m.input.SetHeight(newH)
			m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m.capInputHeight()
			return m, nil
		}
		if handled, next := m.handleLocalSlashCommand(); handled {
			return next, nil
		}
		return m.submit(), statusTickCmd()
	case tea.KeyCtrlN, tea.KeyCtrlJ:
		// Same pre-set height logic as Alt+Enter above.
		newH := m.input.LineCount() + 1
		if newH < 1 {
			newH = 1
		} else if newH > 10 {
			newH = 10
		}
		m.input.SetHeight(newH)
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.capInputHeight()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.capInputHeight()
	// Opening, filtering and closing the "@" popup all follow from the text
	// that is now in the input, rather than from tracking which key did what.
	if scan := m.refreshFileComplete(); scan != nil {
		return m, tea.Batch(cmd, scan)
	}
	return m, cmd
}

// handleLocalSlashCommand deals with the commands the TUI owns itself, before
// the line is submitted or queued. Returns handled=false for anything else.
//
// Shared by both Enter paths on purpose. The busy handler used to fall
// straight through to submit(), which meant a "/model" typed while the agent
// was thinking was queued and later delivered to the model as a prompt — the
// picker never opened, and the model was asked to interpret a command meant
// for the interface. Its own comment claimed it mirrored the idle handler; it
// did not, and one shared function is the only way to keep that claim true.
//
// Only commands with no effect on the conversation are handled here outright.
// The rest (/new, /resume, /btw, /exit) belong to the main loop, which owns the
// session and the history — but while a turn is in flight that loop is blocked
// inside the agent run and is not reading, so they cannot simply fall through
// to submit() either: that queued them as prompts and the model was handed a
// command meant for the interface. So each one is either routed to the command
// channel (safe to start mid-turn) or refused with the reason.
func (m TuiModel) handleLocalSlashCommand() (bool, tea.Model) {
	line := strings.TrimSpace(m.input.Value())
	switch strings.ToLower(line) {
	case "/model":
		m.input.Reset()
		m.input.SetHeight(1)
		m.closeFileComplete()
		m.openModelPicker()
		return true, m
	}
	lower := strings.ToLower(line)
	if m.reading || !strings.HasPrefix(line, "/") {
		return false, m
	}
	switch {
	case lower == "/btw" || strings.HasPrefix(lower, "/btw "):
		// A side conversation is a fork, so it neither touches the running
		// turn nor waits for it to end.
		m.input.Reset()
		m.input.SetHeight(1)
		m.closeFileComplete()
		if m.commands != nil {
			enqueueOrStatus(m.commands, line, &m.statusMessage)
		}
		return true, m
	case strings.HasPrefix(lower, "/msg "):
		// Posting to a job's mailbox doesn't touch the conversation the
		// running turn is writing to, but resolving/posting needs
		// JobRegistry, which this package cannot import (main imports
		// display, not the other way around) — so it's routed the same way
		// as /btw, to whichever loop actually reads m.commands.
		m.input.Reset()
		m.input.SetHeight(1)
		m.closeFileComplete()
		if m.commands != nil {
			enqueueOrStatus(m.commands, line, &m.statusMessage)
		}
		return true, m
	case lower == "/new", lower == "/exit", lower == "/resume", strings.HasPrefix(lower, "/resume "):
		// These replace or end the conversation the turn is writing to. The
		// typed line is deliberately left in the input: press Esc to stop the
		// turn, then Enter.
		m.statusMessage = lower + " has to wait — it changes the conversation this turn is writing to. Esc stops the turn, then press Enter."
		return true, m
	}
	return false, m
}

func (m TuiModel) handleGlobalKey(msg tea.KeyMsg) (bool, tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyPgUp:
		m = m.clearSelection()
		m.atBottom = false
		m.scrollLine += max(1, m.messageRegionHeight())
		m.clampScroll()
		return true, m, nil
	case tea.KeyPgDown:
		m.scrollDown(max(1, m.messageRegionHeight()))
		return true, m, nil
	case tea.KeyCtrlUp:
		m = m.clearSelection()
		m.atBottom = false
		m.scrollLine++
		m.clampScroll()
		return true, m, nil
	case tea.KeyCtrlDown:
		m.scrollDown(1)
		return true, m, nil
	case tea.KeyUp:
		return true, m.historyOlder(), nil
	case tea.KeyDown:
		return true, m.historyNewer(), nil
	case tea.KeyHome:
		m = m.clearSelection()
		m.atBottom = false
		m.scrollLine = m.totalRenderedLines()
		return true, m, nil
	case tea.KeyEnd:
		m = m.clearSelection()
		m.atBottom = true
		m.scrollLine = 0
		return true, m, nil
	case tea.KeyCtrlC:
		m.quitting = true
		return true, m, tea.Quit
	case tea.KeyCtrlD:
		if m.input.Value() == "" {
			m.quitting = true
			return true, m, tea.Quit
		}
	case tea.KeyCtrlP:
		m.openModelPicker()
		return true, m, nil
	case tea.KeyCtrlB:
		m.openJobsModal()
		return true, m, nil
	case tea.KeyCtrlT:
		m.toggleSidebar()
		return true, m, nil
	}
	return false, m, nil
}

func (m TuiModel) handleKeyWhileBusy(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEscape {
		// Clear the pending-message queue in addition to cancelling the
		// current request (issue #88). The user almost always presses ESC
		// because they want to "stop and start over" — leaving queued
		// messages in place would trigger an unwanted follow-up on the
		// very next Enter.
		m.clearMessageQueue()
		select {
		case m.cancelCh <- struct{}{}:
		default:
		}
		return m, nil
	}
	// Enter submits the typed line to the pending-message queue
	// (issue #88). Without this branch, Enter would fall through to
	// the textarea below, which interprets Enter as a newline — so
	// the user would see a new line in the textarea and the message
	// would never be enqueued. The slash-command and Alt+Enter
	// branches mirror the idle handler so the keyboard semantics
	// stay consistent.
	switch msg.Type {
	case tea.KeyEnter:
		if msg.Alt {
			// Alt+Enter: insert a newline in the textarea.
			newH := m.input.LineCount() + 1
			if newH < 1 {
				newH = 1
			} else if newH > 10 {
				newH = 10
			}
			m.input.SetHeight(newH)
			m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m.capInputHeight()
			return m, nil
		}
		if handled, next := m.handleLocalSlashCommand(); handled {
			return next, nil
		}
		return m.submit(), nil
	case tea.KeyCtrlN, tea.KeyCtrlJ:
		// Ctrl+N / Ctrl+J: insert a newline in the textarea.
		newH := m.input.LineCount() + 1
		if newH < 1 {
			newH = 1
		} else if newH > 10 {
			newH = 10
		}
		m.input.SetHeight(newH)
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.capInputHeight()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.capInputHeight()
	// Opening, filtering and closing the "@" popup all follow from the text
	// that is now in the input, rather than from tracking which key did what.
	if scan := m.refreshFileComplete(); scan != nil {
		return m, tea.Batch(cmd, scan)
	}
	return m, cmd
}

// clearMessageQueue drops all pending user messages: both the rendering
// snapshot and the shared channel. Called on ESC (with cancel) and on
// /new (with conversation reset). Issue #88 acceptance criteria #5 and #6.
func (m *TuiModel) clearMessageQueue() {
	if len(m.queueItems) == 0 && m.queue == nil {
		return
	}
	m.queueItems = nil
	m.invalidateTotalLines()
	if m.queue != nil {
		// Non-blocking drain. Safe to call on the bubbletea event loop.
		for {
			select {
			case <-m.queue:
			default:
				return
			}
		}
	}
}

func (m TuiModel) historyOlder() TuiModel {
	if len(m.inputHistory) == 0 {
		return m
	}
	if m.historyIdx == -1 {
		m.stashedInput = m.input.Value()
		m.historyIdx = len(m.inputHistory) - 1
	} else if m.historyIdx > 0 {
		m.historyIdx--
	}
	m.input.SetValue(m.inputHistory[m.historyIdx])
	m.input.SetCursor(len(m.inputHistory[m.historyIdx]))
	m.capInputHeight()
	return m
}

func (m TuiModel) historyNewer() TuiModel {
	if m.historyIdx == -1 || len(m.inputHistory) == 0 {
		return m
	}
	m.historyIdx++
	if m.historyIdx >= len(m.inputHistory) {
		m.historyIdx = -1
		m.input.SetValue(m.stashedInput)
		m.input.SetCursor(len(m.stashedInput))
		m.stashedInput = ""
	} else {
		m.input.SetValue(m.inputHistory[m.historyIdx])
		m.input.SetCursor(len(m.inputHistory[m.historyIdx]))
	}
	m.capInputHeight()
	return m
}
