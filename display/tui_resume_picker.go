package display

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Resume picker lifecycle ─────────────────────────────────────────────

// openResumePicker activates the /resume popup. Called from Update on receipt
// of tuiResumeRequestMsg. Newest-first ordering is enforced by sorting
// ModTime desc — the caller is allowed to pass entries in any order so it can
// pull straight from session.ListEntries or any other source without a
// second pass. Cursor starts at 0 (newest session), like the model picker's
// convention.
func (m *TuiModel) openResumePicker(entries []TuiResumeEntry) {
	sorted := make([]TuiResumeEntry, len(entries))
	copy(sorted, entries)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].ModTime.After(sorted[j-1].ModTime); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	m.resumePickerEntries = sorted
	m.resumePickerCursor = 0
	m.resumePickerActive = true
}

// closeResumePicker is the EQ-only escape path: Esc calls this with selected
// = "" (cancel), Enter calls it with the chosen path. After writing to the
// channel it always clears the picker state so the next /resume cycle starts
// clean. The unbuffered channel write synchronizes with the reader, which
// gives us a natural handshake: the outer goroutine resumes an active
// iteration only after the bubbletea event loop finished the modal frame.
func (m *TuiModel) closeResumePicker(selected string) {
	m.resumePickerActive = false
	m.resumePickerEntries = nil
	m.resumePickerCursor = 0
	if m.resumeCh != nil {
		m.resumeCh <- selected
	}
}

// ─── Update handler ──────────────────────────────────────────────────────

// updateResumePicker routes key events while the /resume modal is open. Any
// other message (mouse, resize, status tick) is forwarded so streaming and
// resize continue to work — the modal is full-screen on a blank background
// so we don't expect to interact with the underlying transcript, but the
// model still needs the resize tick to invalidate caches if a SIGWINCH fires.
func (m TuiModel) updateResumePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		// Match the model picker keymap: Up/Down (1 step), PgUp/PgDn (10),
		// Home/End (jump), Enter (select), Esc (cancel). Filter is NOT
		// supported on the resume picker — sessions don't have a useful
		// short-name filtering property from the caller's POV (the file
		// name is a timestamp + random id), so we skip that branch.
		switch msg.Type {
		case tea.KeyEscape:
			m.closeResumePicker("")
			return m, nil

		case tea.KeyEnter:
			m = m.pickCurrentResumeEntry()
			return m, nil

		case tea.KeyUp:
			if m.resumePickerCursor > 0 {
				m.resumePickerCursor--
			}
			return m, nil

		case tea.KeyDown:
			if m.resumePickerCursor < len(m.resumePickerEntries)-1 {
				m.resumePickerCursor++
			}
			return m, nil

		case tea.KeyHome:
			m.resumePickerCursor = 0
			return m, nil

		case tea.KeyEnd:
			if n := len(m.resumePickerEntries); n > 0 {
				m.resumePickerCursor = n - 1
			}
			return m, nil

		case tea.KeyPgUp:
			m.resumePickerCursor -= 10
			if m.resumePickerCursor < 0 {
				m.resumePickerCursor = 0
			}
			return m, nil

		case tea.KeyPgDown:
			m.resumePickerCursor += 10
			if max := len(m.resumePickerEntries) - 1; m.resumePickerCursor > max {
				m.resumePickerCursor = max
			}
			if m.resumePickerCursor < 0 {
				m.resumePickerCursor = 0
			}
			return m, nil

		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit
		}

	case tea.MouseMsg:
		// Mouse wheel only — left-click is intentionally a no-op so chrome
		// like clicks outside the modal don't pick a session automatically.
		// (There's no explicit click-to-select here to avoid surprise
		// resumes from accidental clicks; arrow keys are the documented
		// way to navigate.)
		if msg.Button == tea.MouseButtonWheelUp {
			if m.resumePickerCursor > 0 {
				m.resumePickerCursor--
			}
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			if m.resumePickerCursor < len(m.resumePickerEntries)-1 {
				m.resumePickerCursor++
			}
			return m, nil
		}
		return m, nil
	}

	return m, nil
}

// pickCurrentResumeEntry calls closeResumePicker with the path of whichever
// row is currently highlighted. No-op if the picker has no entries — keeps
// the bubbletea key handler symmetric with the model picker (which also
// early-returns when nothing is selectable after a filter narrows to zero).
func (m TuiModel) pickCurrentResumeEntry() TuiModel {
	if len(m.resumePickerEntries) == 0 {
		return m
	}
	cursor := m.resumePickerCursor
	if cursor < 0 || cursor >= len(m.resumePickerEntries) {
		cursor = 0
	}
	path := m.resumePickerEntries[cursor].Path
	m.closeResumePicker(path)
	return m
}

// ─── Rendering ───────────────────────────────────────────────────────────

// renderResumePickerView is the only View() while the /resume popup is open.
// It's a centered rounded-border box styled like the model picker but with
// two columns: column 1 = date+time of last modification (newest top), column
// 2 = first user prompt preview with a one-line ellipsis-truncated preview.
// Wrapping is necessary because last prompts can be longer than the box;
// hiding them entirely would defeat the picker's purpose.
func (m TuiModel) renderResumePickerView() string {
	popup := m.renderResumePickerContent()
	return lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		popup,
		lipgloss.WithWhitespaceBackground(lipgloss.Color("235")),
		lipgloss.WithWhitespaceChars(" "),
	)
}

func (m TuiModel) renderResumePickerContent() string {
	var b strings.Builder

	popupWidth := m.width - 16
	if popupWidth < 50 {
		popupWidth = 50
	}
	if popupWidth > 110 {
		popupWidth = 110
	}
	maxPopupHeight := m.height - 10
	if maxPopupHeight < 10 {
		maxPopupHeight = 10
	}
	if maxPopupHeight > 30 {
		maxPopupHeight = 30
	}

	// Title
	title := " Resume Session "
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("60")).
		Width(popupWidth - 2).
		Align(lipgloss.Center)
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n")

	// Subtitle / column header
	dateCol := "Date (UTC)"
	promptCol := "First prompt"
	if popupWidth < 70 {
		// Small terminals: drop explicit header to save rows — the columns
		// are still distinguishable because Date is short and the prompt
		// is human text.
		dateCol = ""
		promptCol = ""
	}
	if dateCol != "" {
		headerStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")).
			Background(lipgloss.Color("238")).
			Bold(true).
			Width(popupWidth - 2)
		// Reserve space for a 2-char gutter and date column (22 chars fixed).
		// The remainder goes to the prompt column.
		hdr := fmt.Sprintf("  %-21s  %s", dateCol, promptCol)
		b.WriteString(headerStyle.Render(hdr))
		b.WriteString("\n")
	}

	sep := strings.Repeat("─", popupWidth-2)
	sepStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Width(popupWidth - 2)
	b.WriteString(sepStyle.Render(sep))
	b.WriteString("\n")

	// Available lines for rows. Account for title, optional header, 2x sep,
	// hint, and 1 row of popup padding.
	reservedRows := 5
	if dateCol != "" {
		reservedRows++
	}
	availableLines := maxPopupHeight - reservedRows
	if availableLines < 1 {
		availableLines = 1
	}

	selectedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("0")).
		Background(lipgloss.Color("45"))
	normalStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))

	total := len(m.resumePickerEntries)

	// Center the cursor in the visible window for long lists.
	visibleStart := 0
	if total > availableLines {
		visibleStart = m.resumePickerCursor - availableLines/2
		if visibleStart < 0 {
			visibleStart = 0
		}
		if visibleStart+availableLines > total {
			visibleStart = total - availableLines
		}
	}

	// Fixed layout for the date column at 22 chars wide (YYYY-MM-DD HH:MM:SS).
	dateWidth := 21                           // external width; with the gutter that follows it totals 23 visible cols
	innerWidth := popupWidth - 4              // 2 chars of side margin inside the rounded border
	promptWidth := innerWidth - dateWidth - 2 // 1 space before date + 1 space between cols

	rendered := 0
	for i := visibleStart; i < total && rendered < availableLines; i++ {
		entry := m.resumePickerEntries[i]
		isSelected := i == m.resumePickerCursor

		dateStr := formatResumeDate(entry.ModTime)
		prompt := truncateResumePrompt(entry.FirstPrompt, promptWidth)

		// Row content: " [prefix]  YYYY-MM-DD HH:MM:SS  prompt…"
		prefix := "  "
		if isSelected {
			prefix = "▸ "
		}
		row := fmt.Sprintf("%s%-*s  %s", prefix, dateWidth, dateStr, prompt)
		if isSelected {
			b.WriteString(selectedStyle.Render(row))
		} else {
			b.WriteString(normalStyle.Render(row))
		}
		b.WriteString("\n")
		rendered++
	}

	// Pad short lists so the popup keeps a stable height.
	for rendered < availableLines {
		b.WriteString("\n")
		rendered++
	}

	b.WriteString(sepStyle.Render(sep))
	b.WriteString("\n")

	var hint string
	if total == 0 {
		hint = "  No sessions recorded for this directory yet"
	} else {
		hint = fmt.Sprintf("  %d session(s) — ↑↓/PgUp/PgDn navigate, Enter resume, Esc cancel", total)
	}
	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Width(popupWidth - 2)
	b.WriteString(hintStyle.Render(hint))

	content := b.String()
	boxStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63"))
	return boxStyle.Render(content)
}

// formatResumeDate renders a modtime in a fixed-width, locale-stable ISO-ish
// format. UTC matches session storage (all session files carry time.Now().UTC()
// stamps in their header line) — keeping the picker in UTC means the date
// column never shows a different day than the underlying file events.
func formatResumeDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

// truncateResumePrompt squares a prompt down to maxWidth with a Unicode-safe
// single-line ellipsis. Newlines are collapsed to spaces so a multi-paragraph
// prompt ends up as a single visible line, matching the picker's one-row-per-
// session invariant. If maxWidth is <= 3, the ellipsis alone is returned.
// Empty string yields a stable placeholder so the row never goes blank.
func truncateResumePrompt(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	// Collapse all whitespace (incl. \n, \t) to single spaces so multi-line
	// prompts stay on the single row the popup expects.
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "(no first prompt parsed)"
	}
	if maxWidth <= 3 {
		// Truncate path: ellipsis literally when we have no room for content.
		return strings.Repeat(".", maxWidth)
	}
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s
	}
	return string(runes[:maxWidth-3]) + "..."
}
