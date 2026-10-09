package display

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The "/" popup opens when the input starts with "/" and has no space yet. It
// lists the builtin commands and the workflows, filtered by what is typed. It
// works like the "@" popup (tui_filecomplete.go): Up and Down move, Tab and
// Enter accept, Esc closes.

// tuiWorkflowStarterMsg hands the starter to the model. NewTUI copies the model
// into the program, so the starter is sent as a message.
type tuiWorkflowStarterMsg struct{ starter WorkflowStarter }

// slashListMsg carries the workflow list that a popup opening asked for.
type slashListMsg struct{ entries []WorkflowEntry }

// slashItem is one row of the "/" popup.
type slashItem struct {
	name  string // without the slash
	label string // params and source, or the description, or the reason it does not load
	err   string // set for a workflow that cannot start
}

// loadSlashEntries returns the command that reads the workflow list.
func loadSlashEntries(s WorkflowStarter) tea.Cmd {
	if s == nil {
		return nil
	}
	return func() tea.Msg { return slashListMsg{entries: s.List()} }
}

// slashQuery is the text after the slash, when the popup is open.
func (m TuiModel) slashQuery() string {
	return strings.TrimPrefix(m.input.Value(), "/")
}

// refreshSlashComplete opens, filters or closes the popup from the input text.
// It returns the command that loads the workflow list when the popup opens.
func (m *TuiModel) refreshSlashComplete() tea.Cmd {
	v := m.input.Value()
	if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, " \t\n") {
		m.closeSlashComplete()
		return nil
	}
	var cmd tea.Cmd
	if !m.slashActive {
		m.slashActive = true
		m.slashEntries = nil
		cmd = loadSlashEntries(m.workflows)
	}
	m.filterSlashItems()
	return cmd
}

// slashCommandRows lists the builtin commands, then the workflows.
func (m TuiModel) slashCommandRows() []slashItem {
	rows := make([]slashItem, 0, len(builtinCommands)+len(m.slashEntries))
	for _, c := range builtinCommands {
		rows = append(rows, slashItem{name: c.name, label: c.desc})
	}
	for _, e := range m.slashEntries {
		rows = append(rows, slashItem{name: e.Name, label: entryLabel(e), err: e.Err})
	}
	return rows
}

// entryLabel is the text after a workflow name: its params, then its source.
// A workflow that does not load shows the reason instead.
func entryLabel(e WorkflowEntry) string {
	if e.Err != "" {
		return "error: " + e.Err
	}
	parts := make([]string, 0, len(e.Params))
	for _, p := range e.Params {
		if p.Required {
			parts = append(parts, "<"+p.Name+">")
		} else {
			parts = append(parts, "["+p.Name+"]")
		}
	}
	label := strings.Join(parts, " ")
	if e.Source != "" {
		if label != "" {
			label += "   "
		}
		label += e.Source
	}
	return label
}

// filterSlashItems keeps the rows whose name contains the query. A prefix match
// comes first. The highlight stays on the same row when that row is still listed.
func (m *TuiModel) filterSlashItems() {
	prev := ""
	if m.slashCursor < len(m.slashItems) {
		prev = m.slashItems[m.slashCursor].name
	}
	query := m.slashQuery()
	var prefix, middle []slashItem
	for _, it := range m.slashCommandRows() {
		switch {
		case strings.HasPrefix(it.name, query):
			prefix = append(prefix, it)
		case strings.Contains(it.name, query):
			middle = append(middle, it)
		}
	}
	m.slashItems = append(prefix, middle...)
	m.slashCursor = 0
	// An exact name takes the highlight, so Enter runs the typed name, not a
	// longer name that the query also matches.
	for i, it := range m.slashItems {
		if it.name == query {
			m.slashCursor = i
			return
		}
	}
	for i, it := range m.slashItems {
		if it.name == prev {
			m.slashCursor = i
			break
		}
	}
}

func (m *TuiModel) closeSlashComplete() {
	m.slashActive = false
	m.slashEntries = nil
	m.slashItems = nil
	m.slashCursor = 0
}

func (m *TuiModel) moveSlashCursor(delta int) {
	if len(m.slashItems) == 0 {
		return
	}
	m.slashCursor = (m.slashCursor + delta + len(m.slashItems)) % len(m.slashItems)
}

// acceptSlashComplete puts the highlighted command into the input, followed by a
// space, so the person can type params. A workflow that does not load is not
// inserted: its reason is shown instead. A reserved name is inserted as
// "/workflow <name>", the way to reach it.
func (m *TuiModel) acceptSlashComplete() {
	if len(m.slashItems) == 0 {
		return
	}
	it := m.slashItems[m.slashCursor]
	text := "/" + it.name + " "
	if it.err != "" {
		if !IsReservedWorkflowName(it.name) {
			m.statusMessage = "/" + it.name + ": " + it.err
			return
		}
		text = "/workflow " + it.name + " "
	}
	m.setInputValueWithCursor(text, len(text))
	m.closeSlashComplete()
}

// handleSlashCompleteKey intercepts the keys the popup owns while it is open.
// It runs before the global key handler, which binds Up and Down to history.
func (m *TuiModel) handleSlashCompleteKey(msg tea.KeyMsg) bool {
	if !m.slashActive {
		return false
	}
	switch msg.Type {
	case tea.KeyUp:
		m.moveSlashCursor(-1)
		return true
	case tea.KeyDown:
		m.moveSlashCursor(1)
		return true
	case tea.KeyTab:
		if len(m.slashItems) == 0 {
			return false
		}
		m.acceptSlashComplete()
		return true
	case tea.KeyEnter:
		// When the typed name already is the highlighted command, Enter runs it.
		// The popup closes and the line goes on to submit.
		if msg.Alt || len(m.slashItems) == 0 || m.slashItems[m.slashCursor].name == m.slashQuery() {
			m.closeSlashComplete()
			return false
		}
		m.acceptSlashComplete()
		return true
	case tea.KeyEscape:
		m.closeSlashComplete()
		return true
	}
	return false
}

// slashWindow is the range of rows the popup shows. The window follows the
// highlight.
func (m TuiModel) slashWindow() (start, end int) {
	start = 0
	if m.slashCursor >= fileCompleteMaxVisible {
		start = m.slashCursor - fileCompleteMaxVisible + 1
	}
	end = min(len(m.slashItems), start+fileCompleteMaxVisible)
	return start, end
}

// renderSlashComplete draws the command list above the input. It returns ""
// when the popup is closed.
func (m TuiModel) renderSlashComplete(width int) string {
	if !m.slashActive {
		return ""
	}
	if len(m.slashItems) == 0 {
		return fileCompleteHintStyle.Render("  no matching command") + "\n"
	}
	var b strings.Builder
	start, end := m.slashWindow()
	for i := start; i < end; i++ {
		it := m.slashItems[i]
		raw := "  /" + it.name
		if it.label != "" {
			raw += " " + it.label
		}
		line := truncateForWidthRight(raw, width)
		if i == m.slashCursor {
			b.WriteString(fileCompleteSelectedStyle.Render(line))
		} else {
			b.WriteString(fileCompleteItemStyle.Render(line))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// slashCompleteHeight is how many terminal rows the popup occupies.
func (m TuiModel) slashCompleteHeight() int {
	if !m.slashActive {
		return 0
	}
	if len(m.slashItems) == 0 {
		return 1
	}
	start, end := m.slashWindow()
	return end - start
}
