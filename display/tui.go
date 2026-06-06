package display

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci-agent/stream"
)

const tuiMaxHistory = 500

// ─── Messages ─────────────────────────────────────────────────────────────

type tuiMsgBlock struct {
	kind     string // "thinking","text","tool-start","tool-delta","tool-end","tool-progress","usage","error","done","block"
	content  string
	toolName string
	toolIdx  int // for tool-progress: index in toolQueue
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
		return 3
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
	status        string // "idle", "thinking", "responding", "tool"
	modelName    string // model name shown in status bar

	// Scroll: offset in rendered lines from the bottom.
	// 0 = bottom (newest messages). Positive = scrolled up.
	scrollLine int

	submitResult chan<- string
	toolQueue    []int // FIFO of block indices for ToolCallStart->ToolCallEnd matching

	// Input history for Up/Down navigation
	inputHistory  []string
	historyIdx    int    // -1 = current input, 0..len-1 = browsing history
	stashedInput  string // saved current input while browsing history
	historyPath   string // path to history file for persistence
}

func newModel(submitResult chan<- string, modelName string, historyPath string) TuiModel {
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

	return TuiModel{
		blocks:        make([]block, 0, 1024),
		input:         ta,
		submitResult:  submitResult,
		ready:         true,
		reading:       true,
		toolQueue:     make([]int, 0, 16),
		modelName:     modelName,
		inputHistory:  loadTuiHistory(historyPath),
		historyIdx:    -1,
		historyPath:   historyPath,
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

		case tea.KeyCtrlUp:
			m.scrollLine++
			m.clampScroll()
			return m, nil

		case tea.KeyCtrlDown:
			m.scrollLine--
			if m.scrollLine < 0 {
				m.scrollLine = 0
			}
			return m, nil

		case tea.KeyUp:
			// Navigate input history (older)
			if len(m.inputHistory) == 0 {
				return m, nil
			}
			if m.historyIdx == -1 {
				// Save current input before browsing
				m.stashedInput = m.input.Value()
				m.historyIdx = len(m.inputHistory) - 1
			} else if m.historyIdx > 0 {
				m.historyIdx--
			}
			m.input.SetValue(m.inputHistory[m.historyIdx])
			m.input.SetCursor(len(m.inputHistory[m.historyIdx]))
			m.capInputHeight()
			return m, nil

		case tea.KeyDown:
			// Navigate input history (newer)
			if m.historyIdx == -1 || len(m.inputHistory) == 0 {
				return m, nil
			}
			m.historyIdx++
			if m.historyIdx >= len(m.inputHistory) {
				// Back to current input
				m.historyIdx = -1
				m.input.SetValue(m.stashedInput)
				m.input.SetCursor(len(m.stashedInput))
				m.stashedInput = ""
			} else {
				m.input.SetValue(m.inputHistory[m.historyIdx])
				m.input.SetCursor(len(m.inputHistory[m.historyIdx]))
			}
			m.capInputHeight()
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

		// Allow typing while agent thinks, but don't submit until reading=true
		if !m.reading {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.capInputHeight()
			return m, cmd
		}

		switch msg.Type {
		case tea.KeyEscape:
			m.input.Reset()
			return m, nil

		case tea.KeyEnter:
			if msg.Alt {
				// Strip Alt so textarea recognizes it as "enter" for newline
				m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
				m.capInputHeight()
				return m, nil
			}
			return m.submit(), nil

		case tea.KeyCtrlN, tea.KeyCtrlJ:
			// Insert newline (fallback when Alt+Enter is captured by terminal)
			m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m.capInputHeight()
			return m, nil
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.capInputHeight()
		return m, cmd

	case tuiMsgBlock:
		m.handleBlockMsg(msg)
		return m, nil

	case tea.MouseMsg:
		// If Shift is held, let terminal handle selection natively
		if msg.Shift {
			return m, nil
		}
		// Wheel scrolling
		if msg.Button == tea.MouseButtonWheelUp {
			m.scrollLine += 3
			m.clampScroll()
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			m.scrollLine -= 3
			if m.scrollLine < 0 {
				m.scrollLine = 0
			}
			return m, nil
		}
		// Left click on tool block to toggle
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
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
	m.input.SetHeight(1)
	if line == "" {
		return m
	}
	// Save to input history (avoid duplicating last entry)
	if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != line {
		m.inputHistory = append(m.inputHistory, line)
		// Persist to history file
		if m.historyPath != "" {
			_ = appendTuiHistory(m.historyPath, line)
		}
	}
	m.historyIdx = -1
	m.reading = false
	m.blocks = append(m.blocks, block{kind: "text", content: "You: " + line})
	if m.submitResult != nil {
		m.submitResult <- line
	}
	return m
}

func (m *TuiModel) capInputHeight() {
	lines := strings.Count(m.input.Value(), "\n") + 1
	if lines > 10 {
		lines = 10
	}
	m.input.SetHeight(lines)
}

// ─── Block handling ───────────────────────────────────────────────────────

func (m *TuiModel) handleBlockMsg(msg tuiMsgBlock) {
	switch msg.kind {
	case "thinking":
		m.status = "thinking"
		m.appendOrAppend("thinking", msg.content)
	case "text":
		if m.status != "responding" {
			m.status = "responding"
		}
		m.appendOrAppend("text", msg.content)
	case "tool-start":
		m.status = "tool"
		idx := len(m.blocks)
		m.blocks = append(m.blocks, block{
			kind: "tool", toolName: msg.toolName,
			toolState: "running", collapsed: true,
			maxLines: defaultMaxLines(msg.toolName),
		})
		m.toolQueue = append(m.toolQueue, idx)
	case "tool-delta":
		m.appendToLastTool(msg.content)
	case "tool-end":
		m.finishToolAt(msg.content)
	case "tool-progress":
		m.appendTool(msg.toolIdx, msg.content)
	case "usage":
		m.lastUsage = msg.usage
		m.lastStats = msg.stats
	case "error":
		m.blocks = append(m.blocks, block{kind: "error", content: msg.content})
	case "done":
		m.status = "idle"
		if msg.usage.Output > 0 || msg.usage.Input > 0 {
			m.appendOrAppend("usage", buildUsageLine(msg.usage, msg.stats))
		}
		m.reading = true
	case "block":
		m.blocks = append(m.blocks, block{kind: "block", content: msg.content})
	case "reset":
		m.blocks = nil
		m.scrollLine = 0
		m.reading = true
		m.status = "idle"
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

func (m *TuiModel) appendTool(queueIdx int, content string) {
	if queueIdx < 0 || queueIdx >= len(m.toolQueue) {
		return
	}
	blockIdx := m.toolQueue[queueIdx]
	if blockIdx >= 0 && blockIdx < len(m.blocks) && m.blocks[blockIdx].kind == "tool" {
		m.blocks[blockIdx].content += content
	}
}

func (m *TuiModel) finishToolAt(result string) {
	if len(m.toolQueue) == 0 {
		return
	}
	idx := m.toolQueue[0]
	m.toolQueue = m.toolQueue[1:]
	if idx >= 0 && idx < len(m.blocks) && m.blocks[idx].kind == "tool" {
		if result != "" {
			// Ensure separator newline if content exists
			if m.blocks[idx].content != "" && !strings.HasSuffix(m.blocks[idx].content, "\n") {
				m.blocks[idx].content += "\n"
			}
			m.blocks[idx].content += result
		}
		m.blocks[idx].toolState = "done"
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
			Render("tyci-agent TUI\nType a message, Enter to send\nCtrl+C to quit\nClick tool block to expand/collapse\nShift+click/drag to select text")
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
		b.WriteString(statusStyle.Render(status))
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
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("69")).Render("│")
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)

	lines := strings.Split(b.content, "\n")
	totalLines := len(lines)

	// Determine if content-hiding tool
	isFileTool := b.toolName == "read" || b.toolName == "write" || b.toolName == "edit"

	// Extract path from first line (JSON args)
	path := extractPath(lines[0])

	var out strings.Builder

	// First line: tool name + path/args
	var firstLine string
	if isFileTool && path != "" {
		firstLine = b.toolName + " " + path
	} else if totalLines > 0 && lines[0] != "" {
		firstLine = b.toolName + " " + lines[0]
	} else {
		firstLine = b.toolName
	}
	if b.toolState == "running" {
		firstLine += " ⟳"
	}

	// Remaining content lines (output)
	remainingLines := totalLines - 1

	// File tools (read/write/edit): always one line when collapsed, show "· N lines (click to expand)" inline
	if isFileTool && b.collapsed {
		if remainingLines > 0 {
			firstLine += fmt.Sprintf(" · %d lines (click to expand)", remainingLines)
		}
		out.WriteString(bar)
		out.WriteString(" ")
		out.WriteString(textStyle.Render(firstLine))
		return strings.TrimRight(out.String(), "\n")
	}

	out.WriteString(bar)
	out.WriteString(" ")
	out.WriteString(textStyle.Render(firstLine))
	out.WriteString("\n")

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
			// Show LAST b.maxLines lines instead of first
			start := totalLines - b.maxLines - 1
			if start < 1 {
				start = 1
			}
			for i := start; i < totalLines; i++ {
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

// extractPath tries to parse JSON and get "path" value.
func extractPath(s string) string {
	if s == "" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return ""
	}
	if p, ok := obj["path"].(string); ok {
		return p
	}
	return ""
}

func (m TuiModel) buildStatus() string {
	leftParts := []string{}
	rightParts := []string{}

	if m.modelName != "" {
		leftParts = append(leftParts, m.modelName)
	}

	if m.scrollLine > 0 {
		leftParts = append(leftParts, fmt.Sprintf("↑%d lines", m.scrollLine))
	}

	if !m.reading {
		switch m.status {
		case "thinking":
			leftParts = append(leftParts, "⟳ thinking...")
		case "responding":
			leftParts = append(leftParts, "⟳ responding...")
		case "tool":
			leftParts = append(leftParts, "⟳ tool...")
		default:
			leftParts = append(leftParts, "⟳ working...")
		}
	}

	if m.lastUsage.Input > 0 || m.lastUsage.Output > 0 {
		inNew := m.lastUsage.Input - m.lastUsage.CacheRead
		if inNew < 0 {
			inNew = 0
		}
		u := fmt.Sprintf("in=%d", inNew)
		if m.lastUsage.CacheRead > 0 {
			u += fmt.Sprintf(" (+%d cache)", m.lastUsage.CacheRead)
		}
		u += fmt.Sprintf(" out=%d", m.lastUsage.Output)
		if m.lastUsage.Reasoning > 0 {
			u += fmt.Sprintf(" r=%d", m.lastUsage.Reasoning)
		}
		if m.lastUsage.CacheWrite > 0 {
			u += fmt.Sprintf(" cache_w=%d", m.lastUsage.CacheWrite)
		}
		genDur := m.lastStats.Duration - m.lastStats.FirstToken
		if genDur < 0 {
			genDur = 0
		}
		u += fmt.Sprintf(" t=%.1fs ttft=%.2fs tok/s=%s",
			m.lastStats.Duration.Seconds(),
			m.lastStats.FirstToken.Seconds(),
			fmtRate(m.lastUsage.Output, genDur),
		)
		rightParts = append(rightParts, u)
	}

	if len(leftParts) == 0 && len(rightParts) == 0 {
		return ""
	}

	left := strings.Join(leftParts, " │ ")
	right := strings.Join(rightParts, " │ ")

	// Right-align the right part
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	padding := m.width - leftW - rightW
	if padding < 1 {
		padding = 1
	}
	return " " + left + strings.Repeat(" ", padding-1) + right
}

// ─── Public API ───────────────────────────────────────────────────────────

type TUI struct {
	prog    *tea.Program
	results chan string
	done    chan struct{}
}

func NewTUI(modelName string, historyPath string) *TUI {
	results := make(chan string, 8)
	m := newModel(results, modelName, historyPath)
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
func (t *TUI) ToolBlock(msg string) {
	// In TUI, "⏳ waiting for tools..." is noise; tools are rendered live via ToolCallStart/Delta/End.
	// Skip it.
	if strings.HasPrefix(msg, "⏳") {
		return
	}
	t.post(tuiMsgBlock{kind: "block", content: msg})
}
func (t *TUI) Summary(usage stream.Usage, stats stream.Stats) {
	t.post(tuiMsgBlock{kind: "usage", usage: usage, stats: stats})
}
func (t *TUI) Error(err error)    { t.post(tuiMsgBlock{kind: "error", content: err.Error()}) }
func (t *TUI) End()               {}
func (t *TUI) Done(usage stream.Usage, stats stream.Stats) {
	t.post(tuiMsgBlock{kind: "done", usage: usage, stats: stats})
}

func (t *TUI) Reset() {
	t.post(tuiMsgBlock{kind: "reset"})
}

// StreamProgress sends incremental tool output to the TUI.
// toolIdx is the index of the tool in the current tool batch (0-based).
func (t *TUI) StreamProgress(toolIdx int, line string) {
	t.post(tuiMsgBlock{kind: "tool-progress", toolIdx: toolIdx, content: line + "\n"})
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

// ─── History persistence ──────────────────────────────────────────────────

// loadTuiHistory reads history lines from a file.
func loadTuiHistory(path string) []string {
	if path == "" {
		return make([]string, 0, 128)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return make([]string, 0, 128)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// Filter printable lines and trim
	clean := make([]string, 0, len(lines))
	for _, l := range lines {
		if l != "" && isPrintable(l) {
			clean = append(clean, l)
		}
	}
	// Cap at max
	if len(clean) > tuiMaxHistory {
		clean = clean[len(clean)-tuiMaxHistory:]
	}
	return clean
}

// appendTuiHistory appends a single line to the history file.
func appendTuiHistory(path, line string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}

// isPrintable checks if a string contains only printable characters.
func isPrintable(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\t' {
			return false
		}
	}
	return true
}
