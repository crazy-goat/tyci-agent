package display

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/jobs"
)

// openJobsModal opens the background-jobs list modal (Ctrl+B).
func (m *TuiModel) openJobsModal() {
	if m.jobsModalActive {
		return
	}
	m.savedScrollLine = m.scrollLine
	m.savedAtBottom = m.atBottom
	m.jobsModalActive = true
	m.jobsModalCursor = 0
}

// closeJobsModal closes the background-jobs list modal and restores scroll
// state, mirroring closeTodoModal.
func (m *TuiModel) closeJobsModal() {
	m.jobsModalActive = false
	m.jobsModalCursor = 0
	m.atBottom = m.savedAtBottom
	m.scrollLine = m.savedScrollLine
	m.selectionVersion++
	m.selection = SelectionState{}
	m.selectionFlash = false
}

// openJobResultModal shows a job's Result/Err as a static popup, reusing the
// subagent modal's rendering (renderSubagentModalView / tui_modal_buffer.go)
// exactly the way openGenericToolModal (tui_mouse.go) reuses it for a
// finished tool block's output: toolIdx=-1 and Done=true mean "static
// content, nothing streams into this buffer" — there is no live toolIdx to
// bind to, because by the time a job is visible here its parent "subagent"
// tool call has already returned (see runAsync's doc comment in
// tools/subagent.go). This is deliberately not live-streamed; Job.Result is
// already the complete, finished output for done/failed/truncated jobs.
func (m *TuiModel) openJobResultModal(j jobs.Job) {
	m.savedScrollLine = m.scrollLine
	m.savedAtBottom = m.atBottom
	m.subagentModalActive = true
	m.subagentModalContent.Reset()
	m.subagentModalScroll = 0
	m.subagentModalDone = true
	m.subagentModalToolIdx = -1
	m.subagentModalTitle = truncateString(j.Description, 80)

	switch j.Status {
	case jobs.StatusRunning:
		m.subagentModalContent.WriteString("(still running)")
	case jobs.StatusFailed:
		m.subagentModalContent.WriteString("error: " + j.Err)
	case jobs.StatusTruncated:
		m.subagentModalContent.WriteString(j.Result + "\n\n[truncated: hit its iteration cap]")
	default: // StatusDone
		m.subagentModalContent.WriteString(j.Result)
	}
}

// jobsModalMaxScroll is unused directly (the modal scrolls via cursor, not a
// raw line offset) but jobsModalLayout reuses todoModalLayout's geometry.
func (m TuiModel) jobsModalLayout() modalLayout {
	return m.todoModalLayout()
}

// renderJobsModalView renders the background-jobs list as a centered popup,
// selectable with Up/Down and opened with Enter — same shape as the model
// picker's cursor + windowing (tui_picker_view.go), applied to jobs instead
// of models.
func (m TuiModel) renderJobsModalView() string {
	layout := m.jobsModalLayout()
	popupWidth := layout.popupWidth
	contentHeight := layout.contentHeight

	list := m.sortedBackgroundJobs()

	titleText := fmt.Sprintf(" Background Jobs — %d ", len(list))
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("60")).
		Width(popupWidth-2).
		Padding(0, 1)
	title := titleStyle.Render(titleText)

	normalStyle := lipgloss.NewStyle().Width(popupWidth - 4)
	selectedStyle := lipgloss.NewStyle().Width(popupWidth - 4).
		Foreground(lipgloss.Color("0")).
		Background(lipgloss.Color("45"))

	var allLines []string
	if len(list) == 0 {
		allLines = append(allLines, "  No background jobs.")
	} else {
		for i, j := range list {
			line := " " + formatJobLine(j, popupWidth-6)
			if i == m.jobsModalCursor {
				allLines = append(allLines, selectedStyle.Render("▸"+line))
			} else {
				allLines = append(allLines, normalStyle.Render(" "+line))
			}
		}
	}
	totalLines := len(allLines)

	visibleStart := 0
	if totalLines > contentHeight {
		visibleStart = m.jobsModalCursor - contentHeight/2
		if visibleStart < 0 {
			visibleStart = 0
		}
		if visibleStart+contentHeight > totalLines {
			visibleStart = totalLines - contentHeight
		}
	}
	visibleEnd := visibleStart + contentHeight
	if visibleEnd > totalLines {
		visibleEnd = totalLines
	}

	m.modalRenderBuffer = newRenderBuffer(contentHeight)
	contentLines := make([]string, 0, contentHeight)
	for i := visibleStart; i < visibleEnd; i++ {
		y := layout.contentTop + len(contentLines)
		m.modalRenderBuffer.Add(allLines[i], "jobs-modal", -1, i, y)
		contentLines = append(contentLines, m.renderSelectableLine(allLines[i], y))
	}
	for len(contentLines) < contentHeight {
		y := layout.contentTop + len(contentLines)
		m.modalRenderBuffer.Add("", "jobs-modal-empty", -1, -1, y)
		contentLines = append(contentLines, m.renderSelectableLine("", y))
	}
	contentStr := strings.Join(contentLines, "\n")

	var footerText string
	if len(list) == 0 {
		footerText = " ESC close "
	} else {
		footerText = " ↑↓ select  Enter view result  ESC close "
	}
	footerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Width(popupWidth-2).
		Padding(0, 1)
	footer := footerStyle.Render(footerText)

	box := lipgloss.JoinVertical(lipgloss.Top, title, contentStr, footer)
	bordered := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Render(box)

	return lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		bordered,
		lipgloss.WithWhitespaceBackground(lipgloss.Color("235")),
		lipgloss.WithWhitespaceChars(" "),
	)
}

// updateJobsModal handles key/mouse events for the background-jobs modal.
func (m TuiModel) updateJobsModal(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape:
			m.closeJobsModal()
			return m, nil

		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit

		case tea.KeyEnter:
			list := m.sortedBackgroundJobs()
			if m.jobsModalCursor >= 0 && m.jobsModalCursor < len(list) {
				j := list[m.jobsModalCursor]
				m.closeJobsModal()
				m.openJobResultModal(j)
			}
			return m, nil

		case tea.KeyUp, tea.KeyCtrlUp:
			if m.jobsModalCursor > 0 {
				m.jobsModalCursor--
			}
			return m, nil

		case tea.KeyDown, tea.KeyCtrlDown:
			if last := len(m.sortedBackgroundJobs()) - 1; m.jobsModalCursor < last {
				m.jobsModalCursor++
			}
			return m, nil

		case tea.KeyHome:
			m.jobsModalCursor = 0
			return m, nil

		case tea.KeyEnd:
			m.jobsModalCursor = max(0, len(m.sortedBackgroundJobs())-1)
			return m, nil
		}

	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonWheelUp {
			if m.jobsModalCursor > 0 {
				m.jobsModalCursor--
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			if last := len(m.sortedBackgroundJobs()) - 1; m.jobsModalCursor < last {
				m.jobsModalCursor++
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			layout := m.jobsModalLayout()
			inModal := msg.X >= layout.left && msg.X < layout.left+layout.popupWidth &&
				msg.Y >= layout.top && msg.Y < layout.top+layout.boxHeight
			if !inModal {
				m.closeJobsModal()
				return m, nil
			}
		}
		return m, nil

	case statusMessageClearMsg:
		if m.statusMessage == msg.message {
			m.statusMessage = ""
		}
		return m, nil

	case selectionFlashDoneMsg:
		m.selectionFlash = false
		return m, nil
	}

	return m, nil
}
