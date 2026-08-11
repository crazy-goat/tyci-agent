package display

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// updateBtwMsg handles tuiBtwOpenMsg/tuiBtwJobIDMsg/tuiBtwStreamMsg. These
// are dispatched from the very top of Update, before any other modal's
// exclusivity check, so a background /btw job's streamed output is never
// dropped just because the user has some other popup (e.g. the model
// picker) open at the moment it arrives. Opening the modal itself
// (tuiBtwOpenMsg) is safe to do unconditionally because it can only be sent
// while the user is at the main input — see runTUI's slash-command dispatch
// in tui_mode.go, which only reads a submitted line between agent turns.
func (m TuiModel) updateBtwMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tuiBtwOpenMsg:
		m.openBtwEntry(msg.id, msg.question, msg.createdAt)
	case tuiBtwJobIDMsg:
		if e := m.findBtwEntry(msg.id); e != nil {
			e.JobID = msg.jobID
		}
	case tuiBtwStreamMsg:
		m.applyBtwStream(msg)
	}
	return m, nil
}

// findBtwEntry returns the entry with the given id, or nil.
func (m *TuiModel) findBtwEntry(id string) *BtwEntry {
	for _, e := range m.btwEntries {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// openBtwEntry records a new entry and opens its live modal.
func (m *TuiModel) openBtwEntry(id, question string, createdAt time.Time) {
	entry := &BtwEntry{
		ID:        id,
		Question:  question,
		CreatedAt: createdAt,
		content:   &strings.Builder{},
	}
	m.btwEntries = append(m.btwEntries, entry)
	m.btwModalEntry = entry
	m.btwModalActive = true
	m.btwModalScroll = 0
}

// applyBtwStream applies one streamed chunk to the matching entry. Content
// keeps accumulating even when that entry's modal isn't the one currently
// on screen (e.g. the user opened the /btw list or another popup while the
// job kept running) — same "never drop live output" rule the subagent
// modal follows for tool-progress.
func (m *TuiModel) applyBtwStream(msg tuiBtwStreamMsg) {
	entry := m.findBtwEntry(msg.id)
	if entry == nil {
		return
	}
	switch msg.kind {
	case "text", "thinking", "tool", "block":
		entry.content.WriteString(msg.content)
		capModalBuffer(entry.content, tuiMaxModalBuffer)
	case "error":
		entry.content.WriteString(msg.content)
		capModalBuffer(entry.content, tuiMaxModalBuffer)
		entry.errMsg = msg.content
	case "done":
		entry.done = true
		if msg.content != "" {
			entry.errMsg = msg.content
		}
	}
}

// ─── Live/preview modal ───────────────────────────────────────────────────

// closeBtwModal is the single source of truth for closing the /btw modal.
// The job (if still running) is unaffected — it keeps running in the
// background, exactly like closing the subagent modal.
func (m *TuiModel) closeBtwModal() {
	m.btwModalActive = false
	m.btwModalEntry = nil
	m.btwModalScroll = 0
}

func (m TuiModel) updateBtwModal(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape:
			m.closeBtwModal()
			return m, nil

		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit

		case tea.KeyEnter:
			if m.btwModalEntry != nil && m.btwModalEntry.done {
				m.closeBtwModal()
			}
			return m, nil

		case tea.KeyUp, tea.KeyCtrlUp:
			if m.btwModalScroll < m.btwModalMaxScroll() {
				m.btwModalScroll++
			}
			return m, nil

		case tea.KeyDown, tea.KeyCtrlDown:
			if m.btwModalScroll > 0 {
				m.btwModalScroll--
			}
			return m, nil

		case tea.KeyPgUp:
			page := m.btwModalPageSize()
			m.btwModalScroll += page
			if m.btwModalScroll > m.btwModalMaxScroll() {
				m.btwModalScroll = m.btwModalMaxScroll()
			}
			return m, nil

		case tea.KeyPgDown:
			page := m.btwModalPageSize()
			m.btwModalScroll -= page
			if m.btwModalScroll < 0 {
				m.btwModalScroll = 0
			}
			return m, nil

		case tea.KeyHome:
			m.btwModalScroll = m.btwModalMaxScroll()
			return m, nil

		case tea.KeyEnd:
			m.btwModalScroll = 0
			return m, nil
		}

	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonWheelUp {
			if m.btwModalScroll < m.btwModalMaxScroll() {
				m.btwModalScroll += 3
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			m.btwModalScroll -= 3
			if m.btwModalScroll < 0 {
				m.btwModalScroll = 0
			}
			return m, nil
		}
		return m, nil
	}

	return m, nil
}

// ─── List popup (bare "/btw") ──────────────────────────────────────────────

func (m *TuiModel) openBtwList() {
	m.btwListActive = true
	m.btwListCursor = 0
}

func (m *TuiModel) closeBtwList() {
	m.btwListActive = false
	m.btwListCursor = 0
}

// btwListEntries returns the recorded entries, newest first — matching the
// /resume popup's convention.
func (m TuiModel) btwListEntries() []*BtwEntry {
	out := make([]*BtwEntry, len(m.btwEntries))
	for i, e := range m.btwEntries {
		out[len(out)-1-i] = e
	}
	return out
}

func (m TuiModel) updateBtwList(msg tea.Msg) (tea.Model, tea.Cmd) {
	entries := m.btwListEntries()

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape:
			m.closeBtwList()
			return m, nil

		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit

		case tea.KeyEnter:
			if len(entries) == 0 {
				return m, nil
			}
			cursor := m.btwListCursor
			if cursor < 0 || cursor >= len(entries) {
				cursor = 0
			}
			m.closeBtwList()
			m.btwModalEntry = entries[cursor]
			m.btwModalActive = true
			m.btwModalScroll = 0
			return m, nil

		case tea.KeyUp:
			if m.btwListCursor > 0 {
				m.btwListCursor--
			}
			return m, nil

		case tea.KeyDown:
			if m.btwListCursor < len(entries)-1 {
				m.btwListCursor++
			}
			return m, nil

		case tea.KeyHome:
			m.btwListCursor = 0
			return m, nil

		case tea.KeyEnd:
			if n := len(entries); n > 0 {
				m.btwListCursor = n - 1
			}
			return m, nil
		}

	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonWheelUp {
			if m.btwListCursor > 0 {
				m.btwListCursor--
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			if m.btwListCursor < len(entries)-1 {
				m.btwListCursor++
			}
			return m, nil
		}
		return m, nil
	}

	return m, nil
}
