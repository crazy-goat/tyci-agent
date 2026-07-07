package display

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/tools"
)

// closeTodoModal closes the todo list modal and restores scroll state.
func (m *TuiModel) closeTodoModal() {
	m.todoModalActive = false
	m.todoModalScroll = 0
	// Restore scroll state from before modal opened
	m.atBottom = m.savedAtBottom
	m.scrollLine = m.savedScrollLine
	// Clear any pending selection so it doesn't bleed into the main view.
	m.selectionVersion++
	m.selection = SelectionState{}
	m.selectionFlash = false
}

// openTodoModal opens the todo list modal.
func (m *TuiModel) openTodoModal() {
	if m.todoModalActive {
		return
	}
	m.savedScrollLine = m.scrollLine
	m.savedAtBottom = m.atBottom
	m.todoModalActive = true
	m.todoModalScroll = 0
}

// todoModalMaxScroll returns the max scroll offset for the todo modal.
func (m TuiModel) todoModalMaxScroll() int {
	items := tools.AllTodoItems()
	totalLines := len(items)
	if totalLines == 0 {
		totalLines = 1 // "No todo items." placeholder
	}
	popupHeight := int(float64(m.height) * 0.9)
	// Subtract title (1) + borders (2) + footer (1) + padding (2) = 6 lines
	avail := popupHeight - 6
	if avail < 1 {
		avail = 1
	}
	if totalLines <= avail {
		return 0
	}
	return totalLines - avail
}

// todoModalLayout returns the layout geometry for the todo modal.
// Reuses the same 90% sizing as the subagent modal.
func (m TuiModel) todoModalLayout() modalLayout {
	popupWidth := int(float64(m.width) * 0.9)
	if popupWidth < 50 {
		popupWidth = 50
	}
	if popupWidth > m.width-2 {
		popupWidth = m.width - 2
	}
	popupHeight := int(float64(m.height) * 0.9)
	if popupHeight < 10 {
		popupHeight = 10
	}
	if popupHeight > m.height-2 {
		popupHeight = m.height - 2
	}
	contentHeight := popupHeight - 6
	if contentHeight < 1 {
		contentHeight = 1
	}
	boxHeight := contentHeight + 4
	left := (m.width - popupWidth) / 2
	if left < 0 {
		left = 0
	}
	top := (m.height - boxHeight) / 2
	if top < 0 {
		top = 0
	}
	contentTop := top + 2
	return modalLayout{
		popupWidth:    popupWidth,
		popupHeight:   popupHeight,
		contentHeight: contentHeight,
		boxHeight:     boxHeight,
		left:          left,
		top:           top,
		contentTop:    contentTop,
		contentBottom: contentTop + contentHeight - 1,
	}
}

// renderTodoModalView renders the todo list modal as a centered popup.
func (m TuiModel) renderTodoModalView() string {
	layout := m.todoModalLayout()
	popupWidth := layout.popupWidth
	contentHeight := layout.contentHeight

	// Build title line
	todoDone, todoTotal := tools.TodoCounts()
	titleText := fmt.Sprintf(" Todo List — %d/%d done ", todoDone, todoTotal)
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("60")).
		Width(popupWidth - 2).
		Padding(0, 1)
	title := titleStyle.Render(titleText)

	// Build content lines
	items := tools.AllTodoItems()
	allLines := make([]string, 0, max(len(items), 1))
	if len(items) == 0 {
		allLines = append(allLines, "  No todo items.")
	} else {
		for _, it := range items {
			allLines = append(allLines, formatTodoModalLine(it, popupWidth-4))
		}
	}
	totalLines := len(allLines)

	// Apply scroll offset
	var visibleStart int
	if totalLines <= contentHeight {
		visibleStart = 0
	} else {
		visibleStart = totalLines - contentHeight - m.todoModalScroll
		if visibleStart < 0 {
			visibleStart = 0
		}
	}
	visibleEnd := visibleStart + contentHeight
	if visibleEnd > totalLines {
		visibleEnd = totalLines
	}

	// Render visible lines
	m.modalRenderBuffer = newRenderBuffer(contentHeight)
	contentLines := make([]string, 0, contentHeight)
	for i := visibleStart; i < visibleEnd; i++ {
		y := layout.contentTop + len(contentLines)
		renderedLine := allLines[i]
		m.modalRenderBuffer.Add(renderedLine, "todo-modal", -1, i, y)
		contentLines = append(contentLines, m.renderSelectableLine(renderedLine, y))
	}
	// Fill remaining empty lines
	for len(contentLines) < contentHeight {
		y := layout.contentTop + len(contentLines)
		m.modalRenderBuffer.Add("", "todo-modal-empty", -1, -1, y)
		contentLines = append(contentLines, m.renderSelectableLine("", y))
	}
	contentStr := strings.Join(contentLines, "\n")

	// Build footer
	var footerText string
	if m.todoModalScroll > 0 {
		pct := int(float64(m.todoModalScroll) / float64(max(1, m.todoModalMaxScroll())) * 100)
		footerText = fmt.Sprintf(" ↑ scrolled %d%%  ↑↓ scroll  ESC close ", pct)
	} else if totalLines > contentHeight {
		footerText = fmt.Sprintf(" ↓ %d more lines  ↑↓ scroll  ESC close ", totalLines-contentHeight)
	} else {
		footerText = " ESC close "
	}
	footerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Width(popupWidth - 2).
		Padding(0, 1)
	footer := footerStyle.Render(footerText)

	// Combine into a bordered box
	box := lipgloss.JoinVertical(lipgloss.Top,
		title,
		contentStr,
		footer,
	)

	bordered := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Render(box)

	// Place centered
	placed := lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		bordered,
		lipgloss.WithWhitespaceBackground(lipgloss.Color("235")),
		lipgloss.WithWhitespaceChars(" "),
	)

	return placed
}

// formatTodoModalLine renders a single todo item for the modal.
// Format: "  [status]  content" with status icon and color.
func formatTodoModalLine(it tools.TodoItem, maxWidth int) string {
	var icon string
	var statusColor lipgloss.TerminalColor
	switch it.Status {
	case "done":
		icon = "✓"
		statusColor = lipgloss.Color("114") // green
	case "doing":
		icon = "⟳"
		statusColor = lipgloss.Color("214") // orange
	case "blocked":
		icon = "⊘"
		statusColor = lipgloss.Color("203") // red
	default: // "todo"
		icon = "○"
		statusColor = lipgloss.Color("245") // dim
	}

	iconStyled := lipgloss.NewStyle().Foreground(statusColor).Render(icon)
	statusStyled := lipgloss.NewStyle().Foreground(statusColor).Render(fmt.Sprintf("[%s]", it.Status))

	line := fmt.Sprintf(" %s %s  %s", iconStyled, statusStyled, it.Content)
	if len(line) > maxWidth {
		line = line[:maxWidth]
	}
	return line
}

// updateTodoModal handles key/mouse events for the todo modal.
func (m TuiModel) updateTodoModal(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape, tea.KeyEnter:
			m.closeTodoModal()
			return m, nil

		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit

		case tea.KeyUp, tea.KeyCtrlUp:
			if m.todoModalScroll < m.todoModalMaxScroll() {
				m.todoModalScroll++
			}
			return m, nil

		case tea.KeyDown, tea.KeyCtrlDown:
			if m.todoModalScroll > 0 {
				m.todoModalScroll--
			}
			return m, nil

		case tea.KeyPgUp:
			page := m.subagentModalPageSize()
			m.todoModalScroll += page
			if m.todoModalScroll > m.todoModalMaxScroll() {
				m.todoModalScroll = m.todoModalMaxScroll()
			}
			return m, nil

		case tea.KeyPgDown:
			page := m.subagentModalPageSize()
			m.todoModalScroll -= page
			if m.todoModalScroll < 0 {
				m.todoModalScroll = 0
			}
			return m, nil

		case tea.KeyHome:
			m.todoModalScroll = m.todoModalMaxScroll()
			return m, nil

		case tea.KeyEnd:
			m.todoModalScroll = 0
			return m, nil
		}

	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonWheelUp {
			if m.todoModalScroll < m.todoModalMaxScroll() {
				m.todoModalScroll += 3
			}
			if m.todoModalScroll > m.todoModalMaxScroll() {
				m.todoModalScroll = m.todoModalMaxScroll()
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			m.todoModalScroll -= 3
			if m.todoModalScroll < 0 {
				m.todoModalScroll = 0
			}
			return m, nil
		}
		// Left click outside modal body → close
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			layout := m.todoModalLayout()
			inModal := msg.X >= layout.left && msg.X < layout.left+layout.popupWidth &&
				msg.Y >= layout.top && msg.Y < layout.top+layout.boxHeight
			if !inModal {
				m.closeTodoModal()
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

// topBarCounterHit returns the label of the counter that was clicked at
// screen position x on the top bar (row 0), or "" if no counter was hit.
//
// It recomputes the counter layout identically to buildTopBar (same logic,
// same truncation + drop loop) so that it stays in sync without needing to
// store side-effect positions during rendering.
func (m TuiModel) topBarCounterHit(x int) string {
	type counterDef struct {
		label     string
		value     string
		dropOrder int
	}

	todoDone, todoTotal := tools.TodoCounts()
	todoStr := fmt.Sprintf("%d/%d", todoDone, todoTotal)
	if todoTotal == 0 {
		todoStr = "-"
	}

	counters := []counterDef{
		{label: "todos:", value: todoStr, dropOrder: 3},
		{label: "skills:", value: fmt.Sprintf("%d", m.skillCount), dropOrder: 4},
		{label: "tools:", value: fmt.Sprintf("%d", m.toolCount), dropOrder: 2},
		{label: "mcp:", value: fmt.Sprintf("%d", m.mcpCount), dropOrder: 1},
	}

	renderCounter := func(c counterDef) string {
		return fmt.Sprintf("%s %s", c.label, c.value)
	}

	type activeCounter struct {
		def      counterDef
		rendered string
	}
	active := make([]activeCounter, len(counters))
	for i, c := range counters {
		active[i] = activeCounter{def: c, rendered: renderCounter(c)}
	}

	path := displayPath(m.cwd, m.home)
	sep := " "
	sepW := lipgloss.Width(sep)
	const sidePad = 1
	sidePadW := sidePad * 2

	for {
		counterStrs := make([]string, len(active))
		for i, a := range active {
			counterStrs[i] = a.rendered
		}
		counterGroup := strings.Join(counterStrs, sep)
		counterW := lipgloss.Width(counterGroup)

		availableForPath := m.width - sidePadW - counterW - sepW
		if availableForPath < 1 {
			availableForPath = 1
		}

		truncatedPath := path
		if lipgloss.Width(truncatedPath) > availableForPath {
			runes := []rune(truncatedPath)
			for len(runes) > 1 {
				candidate := "…" + string(runes[1:])
				if lipgloss.Width(candidate) <= availableForPath {
					truncatedPath = candidate
					break
				}
				runes = runes[1:]
			}
			if lipgloss.Width(truncatedPath) > availableForPath {
				truncatedPath = "…"
			}
		}

		pathW := lipgloss.Width(truncatedPath)
		total := sidePadW + pathW + sepW + counterW
		if total <= m.width {
			// Everything fits. Compute X positions of each counter.
			padding := m.width - total
			if padding < 0 {
				padding = 0
			}
			counterGroupStart := sidePad + pathW + padding + sepW
			offset := counterGroupStart
			for _, a := range active {
				w := lipgloss.Width(a.rendered)
				if a.def.label == "todos:" {
					if x >= offset && x < offset+w {
						return "todos"
					}
				}
				offset += w + sepW
			}
			return ""
		}

		// Drop the counter with the lowest dropOrder.
		if len(active) == 0 {
			break
		}
		minIdx := 0
		for i := 1; i < len(active); i++ {
			if active[i].def.dropOrder < active[minIdx].def.dropOrder {
				minIdx = i
			}
		}
		active = append(active[:minIdx], active[minIdx+1:]...)
	}

	return ""
}
