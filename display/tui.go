package display

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci-agent/stream"
)

// ─── Messages ─────────────────────────────────────────────────────────────

type tuiMsgBlock struct {
	kind     string // "thinking","text","tool-start","tool-delta","tool-end","usage","error","done","block"
	content  string
	toolName string
	usage    stream.Usage
	stats    stream.Stats
}

type tuiInputSubmitted string

// ─── Block ────────────────────────────────────────────────────────────────

type block struct {
	kind      string
	content   string
	toolName  string
	toolState string // "running","done"
	collapsed bool
	maxLines  int
}

func defaultMaxLines(toolName string) int {
	switch toolName {
	case "read", "write", "edit":
		return 1
	case "bash":
		return 10
	case "subagent":
		return 1
	default:
		return 1
	}
}

// ─── Model ────────────────────────────────────────────────────────────────

type TuiModel struct {
	width, height int
	blocks        []block
	input         textarea.Model
	ready         bool
	quitting      bool
	lastUsage     stream.Usage
	lastStats     stream.Stats
	reading       bool

	// Scroll: offset in rendered lines from the bottom.
	// 0 = bottom (newest messages). Positive = scrolled up.
	scrollLine int

	submitResult chan<- string
}

func newModel(submitResult chan<- string) TuiModel {
	ta := textarea.New()
	ta.Placeholder = "Type message (Enter send, Alt+Enter newline)"
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

	return TuiModel{
		blocks:       make([]block, 0, 1024),
		input:        ta,
		submitResult: submitResult,
		ready:        true,
		reading:      true,
	}
}

// ─── tea.Model ────────────────────────────────────────────────────────────

func (m TuiModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m TuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(max(10, msg.Width-2))
		ih := msg.Height / 6
		if ih < 3 {
			ih = 3
		}
		if ih > 8 {
			ih = 8
		}
		m.input.SetHeight(ih)
		// Clamp scroll
		m.clampScroll()
		return m, nil

	case tea.KeyMsg:
		// Always-available keys: scroll, tool toggle, quit
		switch msg.Type {
		case tea.KeyPgUp:
			page := max(1, m.visibleLines())
			m.scrollLine += page
			m.clampScroll()
			return m, nil

		case tea.KeyPgDown:
			page := max(1, m.visibleLines())
			m.scrollLine -= page
			if m.scrollLine < 0 {
				m.scrollLine = 0
			}
			return m, nil

		case tea.KeyUp:
			m.scrollLine++
			m.clampScroll()
			return m, nil

		case tea.KeyDown:
			m.scrollLine--
			if m.scrollLine < 0 {
				m.scrollLine = 0
			}
			return m, nil

		case tea.KeyHome:
			m.scrollLine = m.totalRenderedLines()
			return m, nil

		case tea.KeyEnd:
			m.scrollLine = 0
			return m, nil

		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit

		case tea.KeyCtrlD:
			if m.input.Value() == "" {
				m.quitting = true
				return m, tea.Quit
			}
		}

		// Other keys need reading mode
		if !m.reading {
			return m, nil
		}

		switch msg.Type {
		case tea.KeyEscape:
			m.input.Reset()
			return m, nil

		case tea.KeyEnter:
			if msg.Alt {
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				return m, cmd
			}
			return m.submit(), nil
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case tuiMsgBlock:
		m.handleBlockMsg(msg)
		return m, nil

	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			// Y is 0-indexed terminal row from bubbletea
			if msg.Y >= 0 && msg.Y < m.visibleLines() {
				idx := m.blockAtVisibleLine(msg.Y)
				if idx >= 0 && m.blocks[idx].kind == "tool" && m.blocks[idx].toolState == "done" {
					m.blocks[idx].collapsed = !m.blocks[idx].collapsed
				}
			}
		}
		return m, nil
	}

	return m, nil
}

// ─── Scroll helpers ───────────────────────────────────────────────────────

// totalRenderedLines returns the total number of terminal lines all blocks would
// occupy when rendered, including separator blank lines between blocks.
func (m *TuiModel) totalRenderedLines() int {
	total := 0
	for _, b := range m.blocks {
		rendered := m.renderBlock(b)
		if rendered == "" {
			continue
		}
		lines := strings.Split(rendered, "\n")
		// Each block ends with a blank separator line
		total += len(lines) + 1
	}
	// Remove trailing blank if there are blocks (last block's separator)
	// We'll keep it for simplicity
	return total
}

// clampScroll ensures scrollLine is within valid range.
func (m *TuiModel) clampScroll() {
	maxLine := m.totalRenderedLines()
	if m.scrollLine > maxLine {
		m.scrollLine = maxLine
	}
}

// visibleLines returns the number of terminal lines available for message display.
func (m TuiModel) visibleLines() int {
	return max(1, m.height-m.input.Height()-2)
}

// submit sends user input to the channel and stops reading.
func (m TuiModel) submit() tea.Model {
	line := strings.TrimSpace(m.input.Value())
	m.input.Reset()
	if line == "" {
		return m
	}
	m.reading = false
	m.blocks = append(m.blocks, block{kind: "text", content: "You: " + line})
	if m.submitResult != nil {
		m.submitResult <- line
	}
	return m
}

// ─── Block handling ───────────────────────────────────────────────────────

func (m *TuiModel) handleBlockMsg(msg tuiMsgBlock) {
	switch msg.kind {
	case "thinking":
		m.appendOrAppend("thinking", msg.content)
	case "text":
		m.appendOrAppend("text", msg.content)
	case "tool-start":
		m.blocks = append(m.blocks, block{
			kind: "tool", toolName: msg.toolName,
			toolState: "running", collapsed: true,
			maxLines: defaultMaxLines(msg.toolName),
		})
	case "tool-delta":
		m.appendToLastTool(msg.content)
	case "tool-end":
		m.finishLastTool(msg.content)
	case "usage":
		m.lastUsage = msg.usage
		m.lastStats = msg.stats
	case "error":
		m.blocks = append(m.blocks, block{kind: "error", content: msg.content})
	case "done":
		if msg.usage.Output > 0 || msg.usage.Input > 0 {
			m.appendOrAppend("usage", buildUsageLine(msg.usage, msg.stats))
		}
		m.reading = true
	case "block":
		m.blocks = append(m.blocks, block{kind: "block", content: msg.content})
	}
	m.clampScroll()
}

func (m *TuiModel) appendOrAppend(kind, content string) {
	if len(m.blocks) == 0 {
		m.blocks = append(m.blocks, block{kind: kind, content: content})
		return
	}
	last := &m.blocks[len(m.blocks)-1]
	if last.kind == kind {
		last.content += content
		return
	}
	m.blocks = append(m.blocks, block{kind: kind, content: content})
}

func (m *TuiModel) appendToLastTool(delta string) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == "tool" {
			m.blocks[i].content += delta
			return
		}
	}
}

func (m *TuiModel) finishLastTool(result string) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == "tool" {
			if result != "" {
				m.blocks[i].content += result
			}
			m.blocks[i].toolState = "done"
			return
		}
	}
}

func (m *TuiModel) toggleNextTool() {
	for i := range m.blocks {
		if m.blocks[i].kind == "tool" && m.blocks[i].toolState == "done" {
			m.blocks[i].collapsed = !m.blocks[i].collapsed
			return
		}
	}
}

// blockAtVisibleLine returns the block index at the given visible Y (0-indexed within message area).
// Returns -1 if no block is at that position (welcome, separator, etc).
func (m *TuiModel) blockAtVisibleLine(visY int) int {
	// Build flat lines with block index tracking
	type lineInfo struct {
		blockIdx int
	}
	var allLines []lineInfo

	for blkIdx, blk := range m.blocks {
		rendered := m.renderBlock(blk)
		if rendered == "" {
			continue
		}
		lines := strings.Split(rendered, "\n")
		for range lines {
			allLines = append(allLines, lineInfo{blockIdx: blkIdx})
		}
		// Separator blank line (no block)
		allLines = append(allLines, lineInfo{blockIdx: -1})
	}
	if len(allLines) > 0 && allLines[len(allLines)-1].blockIdx == -1 {
		allLines = allLines[:len(allLines)-1]
	}

	totalLines := len(allLines)
	msgHeight := m.visibleLines()

	var startIdx int
	if totalLines <= msgHeight {
		startIdx = 0
	} else {
		startIdx = totalLines - msgHeight - m.scrollLine
		if startIdx < 0 {
			startIdx = 0
		}
	}

	idx := startIdx + visY
	if idx >= 0 && idx < totalLines {
		return allLines[idx].blockIdx
	}
	return -1
}

// ─── View ─────────────────────────────────────────────────────────────────

func (m TuiModel) View() string {
	if !m.ready {
		return ""
	}
	if m.quitting {
		return ""
	}

	var b strings.Builder
	msgHeight := m.visibleLines()

	// Welcome message
	hasContent := len(m.blocks) > 0
	if !hasContent {
		w := max(10, m.width-2)
		msg := lipgloss.NewStyle().Width(w).Align(lipgloss.Center).
			Foreground(lipgloss.Color("240")).
			Render("tyci-agent TUI\nType a message, Enter to send\nCtrl+C to quit\nClick on a tool block to expand/collapse")
		b.WriteString(msg)
		b.WriteString("\n")
		msgHeight--
	}

	// Build all rendered block lines into a flat slice
	type lineInfo struct {
		text string
	}
	var allLines []lineInfo

	for _, blk := range m.blocks {
		rendered := m.renderBlock(blk)
		if rendered == "" {
			continue
		}
		lines := strings.Split(rendered, "\n")
		for _, l := range lines {
			allLines = append(allLines, lineInfo{text: l})
		}
		// Blank line separator between blocks
		allLines = append(allLines, lineInfo{text: ""})
	}
	// Remove trailing empty line
	if len(allLines) > 0 && allLines[len(allLines)-1].text == "" {
		allLines = allLines[:len(allLines)-1]
	}

	totalLines := len(allLines)

	// Calculate visible range (from bottom)
	var startIdx int
	if totalLines <= msgHeight {
		startIdx = 0
	} else {
		// scrollLine = lines scrolled up from bottom
		startIdx = totalLines - msgHeight - m.scrollLine
		if startIdx < 0 {
			startIdx = 0
		}
	}

	rendered := 0
	for i := startIdx; i < totalLines && rendered < msgHeight; i++ {
		line := allLines[i].text
		if m.width > 0 && len(line) > m.width-1 {
			line = line[:m.width-4] + "..."
		}
		b.WriteString(line)
		b.WriteString("\n")
		rendered++
	}
	for rendered < msgHeight {
		b.WriteString("\n")
		rendered++
	}

	// Status bar
	status := m.buildStatus()
	statusStyle := lipgloss.NewStyle().
		Width(m.width).MaxWidth(m.width).
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("250"))
	if status != "" {
		b.WriteString(statusStyle.Render(" " + status))
	} else {
		b.WriteString(statusStyle.Render(""))
	}
	b.WriteString("\n")

	b.WriteString(m.input.View())
	return b.String()
}

func (m TuiModel) renderBlock(b block) string {
	switch b.kind {
	case "thinking":
		bar := lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Render("│")
		textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Italic(true)
		lines := strings.Split(b.content, "\n")
		var out strings.Builder
		for _, line := range lines {
			out.WriteString(bar)
			out.WriteString(" ")
			out.WriteString(textStyle.Render(line))
			out.WriteString("\n")
		}
		return strings.TrimRight(out.String(), "\n")
	case "text":
		return b.content
	case "tool":
		return m.renderToolBlock(b)
	case "usage":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("247")).Render("📊 " + b.content)
	case "error":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("✖ " + b.content)
	case "block":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("242")).Render(b.content)
	default:
		return b.content
	}
}

func (m TuiModel) renderToolBlock(b block) string {
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("69")).Render("│") // blue
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)

	lines := strings.Split(b.content, "\n")
	totalLines := len(lines)

	var out strings.Builder

	// First line: tool name + first content line (args) inline
	firstLine := b.toolName
	if totalLines > 0 && lines[0] != "" {
		firstLine += " " + lines[0]
	}
	if b.toolState == "running" {
		firstLine += " ⟳"
	}
	out.WriteString(bar)
	out.WriteString(" ")
	out.WriteString(textStyle.Render(firstLine))
	out.WriteString("\n")

	// Remaining content lines (output)
	remainingLines := totalLines - 1

	if remainingLines > 0 {
		if b.toolState == "running" {
			// Show last line only
			last := lines[totalLines-1]
			out.WriteString(bar)
			out.WriteString(" ")
			out.WriteString(textStyle.Render(last))
			if remainingLines > 1 {
				out.WriteString(dimStyle.Render(fmt.Sprintf(" … (+%d more)", remainingLines-1)))
			}
		} else if b.collapsed && b.maxLines > 0 && remainingLines > b.maxLines {
			for i := 1; i < b.maxLines+1; i++ {
				out.WriteString(bar)
				out.WriteString(" ")
				out.WriteString(textStyle.Render(lines[i]))
				out.WriteString("\n")
			}
			out.WriteString(dimStyle.Render(fmt.Sprintf("├── %d more lines (click to expand)", remainingLines-b.maxLines)))
		} else {
			for i := 1; i < totalLines; i++ {
				out.WriteString(bar)
				out.WriteString(" ")
				out.WriteString(textStyle.Render(lines[i]))
				out.WriteString("\n")
			}
			if b.maxLines > 0 && remainingLines > b.maxLines {
				out.WriteString(dimStyle.Render("├── expanded (click to collapse)"))
			}
		}
	}

	return strings.TrimRight(out.String(), "\n")
}

func (m TuiModel) buildStatus() string {
	parts := []string{}
	if m.scrollLine > 0 {
		parts = append(parts, fmt.Sprintf("↑%d lines", m.scrollLine))
	}
	if !m.reading {
		parts = append(parts, "⟳ thinking...")
	}
	if m.lastUsage.Input > 0 || m.lastUsage.Output > 0 {
		u := fmt.Sprintf("in=%d out=%d", m.lastUsage.Input, m.lastUsage.Output)
		if m.lastUsage.Reasoning > 0 {
			u += fmt.Sprintf(" r=%d", m.lastUsage.Reasoning)
		}
		parts = append(parts, u)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " │ ")
}

// ─── Public API ───────────────────────────────────────────────────────────

type TUI struct {
	prog    *tea.Program
	results chan string
	done    chan struct{}
}

func NewTUI() *TUI {
	results := make(chan string, 8)
	m := newModel(results)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	t := &TUI{
		prog:    p,
		results: results,
		done:    make(chan struct{}),
	}

	go func() {
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		}
		close(t.done)
	}()

	return t
}

func (t *TUI) post(msg tuiMsgBlock) { t.prog.Send(msg) }

func (t *TUI) Thinking(text string)           { t.post(tuiMsgBlock{kind: "thinking", content: text}) }
func (t *TUI) Text(text string)                { t.post(tuiMsgBlock{kind: "text", content: text}) }
func (t *TUI) ToolCallStart(name string)       { t.post(tuiMsgBlock{kind: "tool-start", toolName: name}) }
func (t *TUI) ToolCallDelta(delta string)      { t.post(tuiMsgBlock{kind: "tool-delta", content: delta}) }
func (t *TUI) ToolCallEnd(name, result string) { t.post(tuiMsgBlock{kind: "tool-end", content: result}) }
func (t *TUI) ToolBlock(msg string)            { t.post(tuiMsgBlock{kind: "block", content: msg}) }
func (t *TUI) Summary(usage stream.Usage, stats stream.Stats) {
	t.post(tuiMsgBlock{kind: "usage", usage: usage, stats: stats})
}
func (t *TUI) Error(err error)    { t.post(tuiMsgBlock{kind: "error", content: err.Error()}) }
func (t *TUI) End()               {}
func (t *TUI) Done(usage stream.Usage, stats stream.Stats) {
	t.post(tuiMsgBlock{kind: "done", usage: usage, stats: stats})
}

func (t *TUI) ReadInput(_ context.Context, _ string) (string, error) {
	select {
	case line, ok := <-t.results:
		if !ok {
			return "", context.Canceled
		}
		return line, nil
	case <-t.done:
		return "", fmt.Errorf("TUI closed")
	}
}

func (t *TUI) Wait() { <-t.done }
func (t *TUI) Close() { t.prog.Quit() }

func max(a, b int) int {
	if a > b { return a }
	return b
}
