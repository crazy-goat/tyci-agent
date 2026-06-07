package display

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci-agent/stream"
)

const tuiMaxHistory = 500

// ─── Messages ─────────────────────────────────────────────────────────────

type tuiMsgBlock struct {
	kind     string // "thinking","text","tool-start","tool-delta","tool-end","tool-progress","usage","error","done","block","set-model","reset"
	content  string
	toolName string
	toolIdx  int // for tool-progress: index in toolQueue
	usage    stream.Usage
	stats    stream.Stats
}

type tuiInputSubmitted string

// ─── Model picker types ───────────────────────────────────────────────────

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

// ─── Block ────────────────────────────────────────────────────────────────

type block struct {
	kind      string
	content   string
	toolName  string
	toolState string // "running","done"
	collapsed bool
	maxLines  int
	output    string        // full tool output (for modal)
	startTime time.Time     // when the tool was started (for duration display)
	duration  time.Duration // frozen duration when tool finished (0 = still running)
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
	modelName     string // model name shown in status bar

	// Model switching (Tab/Shift+Tab)
	models        []string        // available models (format: "provider/model")
	modelIdx      int             // index of current model in models slice
	modelChanges  chan<- string   // channel to notify outer TUI of model changes

	// Model picker (/model command)
	pickerActive  bool
	pickerFilter  string
	pickerCursor  int              // index into pickerItems (only model entries)
	pickerItems   []pickerItem     // filtered list for display
	allProviders  []ProviderModels // grouped provider->models for the picker

	// Cancel signal: sent on when ESC pressed during agent run
	cancelCh chan<- struct{}

	// Scroll: offset in rendered lines from the bottom.
	// 0 = bottom (newest messages). Positive = scrolled up.
	scrollLine int

	// Subagent modal (live streaming output from child agents)
	subagentModalActive  bool
	subagentModalTitle   string          // task description (first ~60 chars)
	subagentModalContent *strings.Builder // accumulated output
	subagentModalScroll  int             // scroll offset within modal
	subagentModalToolIdx int             // tool queue index for this modal
	subagentModalDone    bool            // true when subagent finished (ESC to close)

	submitResult chan<- string
	toolQueue    []int // FIFO of block indices for ToolCallStart->ToolCallEnd matching

	// Input history for Up/Down navigation
	inputHistory  []string
	historyIdx    int    // -1 = current input, 0..len-1 = browsing history
	stashedInput  string // saved current input while browsing history
	historyPath   string // path to history file for persistence
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

	// Find index of current model
	modelIdx := 0
	for i, m := range models {
		if m == modelName {
			modelIdx = i
			break
		}
	}

	return TuiModel{
		blocks:        make([]block, 0, 1024),
		input:         ta,
		submitResult:  submitResult,
		ready:         true,
		reading:       true,
		toolQueue:     make([]int, 0, 16),
		modelName:     modelName,
		models:        models,
		modelIdx:      modelIdx,
		modelChanges:  modelChanges,
		allProviders:  allProviders,
		cancelCh:      cancelCh,
		inputHistory:  loadTuiHistory(historyPath),
		historyIdx:    -1,
		historyPath:   historyPath,
		subagentModalToolIdx: -1,
		subagentModalContent: &strings.Builder{},
	}
}

// ─── tea.Model ────────────────────────────────────────────────────────────

func (m TuiModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m TuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// ── Model picker popup mode ──
	if m.pickerActive {
		return m.updatePicker(msg)
	}

	// ── Subagent modal mode ──
	if m.subagentModalActive {
		return m.updateSubagentModal(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(max(10, msg.Width-2))
		m.clampScroll()
		return m, nil

	case tea.KeyMsg:
		// Model switching with Tab/Shift+Tab
		switch msg.Type {
		case tea.KeyTab:
			if len(m.models) > 0 {
				m.modelIdx = (m.modelIdx + 1) % len(m.models)
				newModel := m.models[m.modelIdx]
				m.modelName = newModel
				if m.modelChanges != nil {
					select {
					case m.modelChanges <- newModel:
					default:
					}
				}
			}
			return m, nil

		case tea.KeyShiftTab:
			if len(m.models) > 0 {
				m.modelIdx = (m.modelIdx - 1 + len(m.models)) % len(m.models)
				newModel := m.models[m.modelIdx]
				m.modelName = newModel
				if m.modelChanges != nil {
					select {
					case m.modelChanges <- newModel:
					default:
					}
				}
			}
			return m, nil
		}

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
			// ESC during agent run → cancel the current operation
			if msg.Type == tea.KeyEscape {
				select {
				case m.cancelCh <- struct{}{}:
				default:
				}
				return m, nil
			}
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
				m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
				m.capInputHeight()
				return m, nil
			}
			// Check for /model command
			line := strings.TrimSpace(m.input.Value())
			if strings.EqualFold(line, "/model") {
				m.input.Reset()
				m.openModelPicker()
				return m, nil
			}
			return m.submit(), nil

		case tea.KeyCtrlN, tea.KeyCtrlJ:
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
		if msg.Shift {
			return m, nil
		}
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
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			if msg.Y >= 0 && msg.Y < m.visibleLines() {
				idx := m.blockAtVisibleLine(msg.Y)
				if idx >= 0 && m.blocks[idx].kind == "tool" {
					// Open tool detail modal on click for any tool
					if m.blocks[idx].toolName == "subagent" && !m.subagentModalActive {
						m.subagentModalActive = true
						m.subagentModalScroll = 0
						// subagentModalContent already has accumulated output from tool-progress
						for qi, bidx := range m.toolQueue {
							if bidx == idx {
								m.subagentModalToolIdx = qi
								break
							}
						}
					} else if m.blocks[idx].toolName != "subagent" && m.blocks[idx].toolState == "done" {
						// Generic tool detail modal for other tools
						m.subagentModalActive = true
						m.subagentModalContent.Reset()
						m.subagentModalScroll = 0
						m.subagentModalDone = true
						// Set title from the tool block's first line (summary)
						title := m.blocks[idx].toolName
						if m.blocks[idx].content != "" {
							firstLine := strings.SplitN(m.blocks[idx].content, "\n", 2)[0]
							if firstLine != "" {
								title = truncateString(firstLine, 80)
							}
						}
						m.subagentModalTitle = title
						content := m.blocks[idx].output
						if content == "" {
							content = m.blocks[idx].content
						}
						m.subagentModalContent.WriteString(content)
						m.subagentModalToolIdx = -1
					}
				}
			}
		}
		return m, nil
	}

	return m, nil
}

// updatePicker handles keyboard input when the model picker popup is active.
func (m TuiModel) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape:
			m.closeModelPicker()
			return m, nil

		case tea.KeyEnter:
			// Select the currently highlighted model
			selected := m.pickerSelectedModel()
			if selected != "" {
				m.selectModel(selected)
			}
			return m, nil

		case tea.KeyUp:
			if m.pickerCursor > 0 {
				m.pickerCursor--
			}
			return m, nil

		case tea.KeyDown:
			modelCount := m.pickerModelCount()
			if m.pickerCursor < modelCount-1 {
				m.pickerCursor++
			}
			return m, nil

		case tea.KeyHome:
			m.pickerCursor = 0
			return m, nil

		case tea.KeyEnd:
			m.pickerCursor = m.pickerModelCount() - 1
			if m.pickerCursor < 0 {
				m.pickerCursor = 0
			}
			return m, nil

		case tea.KeyBackspace:
			if len(m.pickerFilter) > 0 {
				m.pickerFilter = m.pickerFilter[:len(m.pickerFilter)-1]
				m.rebuildPickerItems()
			}
			return m, nil

		case tea.KeyTab, tea.KeyShiftTab:
			// Ignore tab in picker mode
			return m, nil

		default:
			// Add character to filter
			if msg.Type == tea.KeyRunes {
				m.pickerFilter += string(msg.Runes)
				m.rebuildPickerItems()
			}
			return m, nil
		}

	case tea.MouseMsg:
		return m, nil
	}

	return m, nil
}

// updateSubagentModal handles keyboard input when the subagent modal is active.
// It also forwards tuiMsgBlock messages to handleBlockMsg so streaming
// (tool-progress, tool-end, error, done, reset) continues to work.
func (m TuiModel) updateSubagentModal(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape:
			// Close modal on ESC — always (even if running).
			// The subagent keeps running in background; its output
			// goes to the inline tool block after modal closes.
			m.subagentModalActive = false
			m.subagentModalContent.Reset()
			m.subagentModalToolIdx = -1
			m.subagentModalDone = false
			return m, nil

		case tea.KeyCtrlC:
			// Quit the whole program
			m.quitting = true
			return m, tea.Quit

		case tea.KeyEnter:
			// Close modal on Enter when done
			if m.subagentModalDone {
				m.subagentModalActive = false
				m.subagentModalContent.Reset()
				m.subagentModalToolIdx = -1
				m.subagentModalDone = false
			}
			return m, nil

		case tea.KeyUp, tea.KeyCtrlUp:
			if m.subagentModalScroll < m.subagentModalMaxScroll() {
				m.subagentModalScroll++
			}
			return m, nil

		case tea.KeyDown, tea.KeyCtrlDown:
			if m.subagentModalScroll > 0 {
				m.subagentModalScroll--
			}
			return m, nil

		case tea.KeyPgUp:
			page := m.subagentModalPageSize()
			m.subagentModalScroll += page
			if m.subagentModalScroll > m.subagentModalMaxScroll() {
				m.subagentModalScroll = m.subagentModalMaxScroll()
			}
			return m, nil

		case tea.KeyPgDown:
			page := m.subagentModalPageSize()
			m.subagentModalScroll -= page
			if m.subagentModalScroll < 0 {
				m.subagentModalScroll = 0
			}
			return m, nil

		case tea.KeyHome:
			m.subagentModalScroll = m.subagentModalMaxScroll()
			return m, nil

		case tea.KeyEnd:
			m.subagentModalScroll = 0
			return m, nil
		}

	case tea.MouseMsg:
		return m, nil

	case tuiMsgBlock:
		// Forward block messages to the normal handler so streaming
		// (tool-progress, tool-end, error, done, reset) works while
		// the subagent modal is active.
		m.handleBlockMsg(msg)
		return m, nil
	}

	return m, nil
}

// openModelPicker activates the model picker popup.
func (m *TuiModel) openModelPicker() {
	m.pickerActive = true
	m.pickerFilter = ""
	m.pickerCursor = 0
	m.rebuildPickerItems()
}

// closeModelPicker deactivates the model picker popup without changing the model.
func (m *TuiModel) closeModelPicker() {
	m.pickerActive = false
	m.pickerFilter = ""
	m.pickerCursor = 0
	m.pickerItems = nil
}

// selectModel picks a model and closes the picker.
func (m *TuiModel) selectModel(model string) {
	m.modelName = model
	// Update modelIdx in flat list
	for i, mm := range m.models {
		if mm == model {
			m.modelIdx = i
			break
		}
	}
	if m.modelChanges != nil {
		select {
		case m.modelChanges <- model:
		default:
		}
	}
	m.closeModelPicker()
}

// rebuildPickerItems builds the filtered picker items list from allProviders.
func (m *TuiModel) rebuildPickerItems() {
	m.pickerItems = nil
	modelCount := 0
	filter := strings.ToLower(m.pickerFilter)

	for _, prov := range m.allProviders {
		// Collect matching models for this provider
		var matched []string
		for _, model := range prov.Models {
			label := prov.Name + "/" + model
			if filter == "" || strings.Contains(strings.ToLower(label), filter) {
				matched = append(matched, label)
			}
		}
		if len(matched) == 0 {
			continue
		}
		// Add header
		m.pickerItems = append(m.pickerItems, pickerItem{isHeader: true, label: prov.Name})
		for _, label := range matched {
			m.pickerItems = append(m.pickerItems, pickerItem{isHeader: false, label: label, value: label})
			modelCount++
		}
	}

	// Clamp cursor
	if m.pickerCursor >= modelCount && modelCount > 0 {
		m.pickerCursor = modelCount - 1
	} else if modelCount == 0 {
		m.pickerCursor = 0
	}
}

// pickerModelCount returns the number of model items (not headers) in the picker.
func (m *TuiModel) pickerModelCount() int {
	count := 0
	for _, item := range m.pickerItems {
		if !item.isHeader {
			count++
		}
	}
	return count
}

// pickerSelectedModel returns the currently selected model (full "provider/model").
func (m *TuiModel) pickerSelectedModel() string {
	idx := 0
	for _, item := range m.pickerItems {
		if item.isHeader {
			continue
		}
		if idx == m.pickerCursor {
			return item.value
		}
		idx++
	}
	return ""
}

// ─── Scroll helpers ───────────────────────────────────────────────────────

// truncateString shortens a string to maxLen with "..." if needed.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

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
	// Use the textarea's own line count, which accounts for both hard newlines
	// and soft-wraps. Manual newline+width math was off whenever a logical
	// line was long enough to wrap on its own, causing lines to disappear.
	lines := m.input.LineCount()
	if lines < 1 {
		lines = 1
	}
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
			startTime: time.Now(),
		})
		m.toolQueue = append(m.toolQueue, idx)

		// Track subagent tool index for modal (but don't auto-open).
		// Modal opens on user click on the subagent block.
		if msg.toolName == "subagent" {
			m.subagentModalToolIdx = len(m.toolQueue) - 1
			m.subagentModalContent.Reset()
			m.subagentModalScroll = 0
			m.subagentModalDone = false
			m.subagentModalTitle = "subagent"
		}
	case "tool-delta":
		// For subagent: extract task description, don't append raw delta to inline block
		if m.subagentModalToolIdx >= 0 && m.subagentModalTitle == "subagent" && msg.content != "" {
			var args map[string]any
			if json.Unmarshal([]byte(msg.content), &args) == nil {
				if task, ok := args["task"].(string); ok && task != "" {
					m.subagentModalTitle = truncateString(task, 80)
				}
			}
			// Set inline block to "subagent (task...)" format
			if len(m.toolQueue) > m.subagentModalToolIdx {
				bidx := m.toolQueue[m.subagentModalToolIdx]
				if bidx >= 0 && bidx < len(m.blocks) && m.blocks[bidx].kind == "tool" {
					m.blocks[bidx].content = "subagent (" + m.subagentModalTitle + ")"
				}
			}
		} else {
			m.appendToLastTool(msg.content)
		}
	case "tool-end":
		// Determine if this tool-end is for the subagent
		isSubagentEnd := m.subagentModalToolIdx == 0 && m.subagentModalToolIdx >= 0 && len(m.toolQueue) > 0

		if isSubagentEnd {
			// For subagent: pop queue entry without appending result to block content
			if len(m.toolQueue) > 0 {
				idx := m.toolQueue[0]
				m.toolQueue = m.toolQueue[1:]
				if idx >= 0 && idx < len(m.blocks) && m.blocks[idx].kind == "tool" {
					m.blocks[idx].toolState = "done"
					m.blocks[idx].duration = time.Since(m.blocks[idx].startTime)
				}
			}
			m.subagentModalDone = true
			m.subagentModalToolIdx = -1
		} else {
			m.finishToolAt(msg.content)
			// If subagent is deeper in queue, decrement its index
			if m.subagentModalToolIdx > 0 {
				m.subagentModalToolIdx--
			}
		}
	case "tool-progress":
		// Subagent progress captured for modal (even if not active), never to inline block
		if msg.toolIdx == m.subagentModalToolIdx {
			m.subagentModalContent.WriteString(msg.content)
		} else {
			m.appendTool(msg.toolIdx, msg.content)
		}
	case "usage":
		m.lastUsage = msg.usage
		m.lastStats = msg.stats
	case "error":
		m.blocks = append(m.blocks, block{kind: "error", content: msg.content})
	case "done":
		m.status = "idle"
		m.reading = true
	case "block":
		m.blocks = append(m.blocks, block{kind: "block", content: msg.content})
	case "set-model":
		m.modelName = msg.content
	case "reset":
		m.blocks = nil
		m.scrollLine = 0
		m.reading = true
		m.status = "idle"
		m.lastUsage = stream.Usage{}
		m.lastStats = stream.Stats{}
		m.subagentModalActive = false
		m.subagentModalContent.Reset()
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
		m.blocks[blockIdx].output += content
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
			if m.blocks[idx].output != "" && !strings.HasSuffix(m.blocks[idx].output, "\n") {
				m.blocks[idx].output += "\n"
			}
			m.blocks[idx].output += result
		}
		m.blocks[idx].toolState = "done"
		m.blocks[idx].duration = time.Since(m.blocks[idx].startTime)
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
		// Separator blank line (skip if next block is also a tool)
		if blkIdx+1 < len(m.blocks) && m.blocks[blkIdx+1].kind == "tool" && blk.kind == "tool" {
			continue
		}
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

	// ── Subagent modal overlay mode ──
	if m.subagentModalActive {
		return m.renderSubagentModalView()
	}

	// ── Model picker popup mode ──
	if m.pickerActive {
		return m.renderModelPickerView()
	}

	var b strings.Builder
	msgHeight := m.visibleLines()

	// Welcome message
	hasContent := len(m.blocks) > 0
	if !hasContent {
		w := max(10, m.width-2)
		msg := lipgloss.NewStyle().Width(w).Align(lipgloss.Center).
			Foreground(lipgloss.Color("240")).
			Render("tyci-agent TUI\nType a message, Enter to send\nCtrl+C to quit\nTab/Shift+Tab: switch model\n/model: pick model\nClick tool block to expand/collapse\nShift+click/drag to select text")
		b.WriteString(msg)
		b.WriteString("\n")
		msgHeight--
	}

	// Build all rendered block lines into a flat slice
	type lineInfo struct {
		text string
	}
	var allLines []lineInfo

	for i, blk := range m.blocks {
		rendered := m.renderBlock(blk)
		if rendered == "" {
			continue
		}
		lines := strings.Split(rendered, "\n")
		for _, l := range lines {
			allLines = append(allLines, lineInfo{text: l})
		}
		// Blank line separator between blocks (skip if next block is also a tool)
		if i+1 < len(m.blocks) && m.blocks[i+1].kind == "tool" && blk.kind == "tool" {
			continue
		}
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
		if m.width > 0 && lipgloss.Width(line) > m.width {
			// Wrap long lines instead of truncating with "..."
			wrapped := wrapText(line, m.width, 0)
			wrappedLines := strings.Split(wrapped, "\n")
			for _, wl := range wrappedLines {
				if rendered >= msgHeight {
					break
				}
				wl = strings.TrimSuffix(wl, clearLine)
				b.WriteString(wl)
				b.WriteString("\n")
				rendered++
			}
		} else {
			b.WriteString(line)
			b.WriteString("\n")
			rendered++
		}
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

// renderModelPickerView renders just the model picker popup on a blank background.
func (m TuiModel) renderModelPickerView() string {
	popup := m.renderModelPickerContent()

	// Use Place to center the popup in the background
	placed := lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		popup,
		lipgloss.WithWhitespaceBackground(lipgloss.Color("235")),
		lipgloss.WithWhitespaceChars(" "),
	)
	return placed
}

// ─── Subagent modal ─────────────────────────────────────────────────────

// subagentModalMaxScroll returns the maximum scroll offset (lines from bottom).
func (m TuiModel) subagentModalMaxScroll() int {
	content := m.subagentModalContent.String()
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	popupHeight := int(float64(m.height) * 0.9)
	// Subtract title (2) + footer (2) + borders (2) = ~6 lines
	avail := popupHeight - 6
	if avail < 1 {
		avail = 1
	}
	if totalLines <= avail {
		return 0
	}
	return totalLines - avail
}

// subagentModalPageSize returns the number of lines per page scroll.
func (m TuiModel) subagentModalPageSize() int {
	popupHeight := int(float64(m.height) * 0.9)
	avail := popupHeight - 6
	if avail < 1 {
		avail = 1
	}
	return avail
}

// renderSubagentModalView renders the subagent live output as a centered modal (90% w/h).
func (m TuiModel) renderSubagentModalView() string {
	popupWidth := int(float64(m.width) * 0.9)
	if popupWidth < 60 {
		popupWidth = 60
	}
	if popupWidth > m.width-2 {
		popupWidth = m.width - 2
	}
	popupHeight := int(float64(m.height) * 0.9)
	if popupHeight < 15 {
		popupHeight = 15
	}
	if popupHeight > m.height-2 {
		popupHeight = m.height - 2
	}

	// Content area height (minus title, footer, borders)
	contentHeight := popupHeight - 6
	if contentHeight < 1 {
		contentHeight = 1
	}

	// Build title line
	status := "⟳ running..."
	if m.subagentModalDone {
		status = "✓ done"
	}
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("60")).
		Width(popupWidth - 2).
		Padding(0, 1)
	title := titleStyle.Render(fmt.Sprintf(" %s — %s ", m.subagentModalTitle, status))

	// Build content with scroll
	allLines := strings.Split(m.subagentModalContent.String(), "\n")
	totalLines := len(allLines)

	var visibleStart int
	if totalLines <= contentHeight {
		visibleStart = 0
	} else {
		visibleStart = totalLines - contentHeight - m.subagentModalScroll
		if visibleStart < 0 {
			visibleStart = 0
		}
	}
	visibleEnd := visibleStart + contentHeight
	if visibleEnd > totalLines {
		visibleEnd = totalLines
	}

	// Render visible lines
	lineStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	contentLines := make([]string, 0, contentHeight)

	for i := visibleStart; i < visibleEnd; i++ {
		line := allLines[i]
		// Truncate long lines (no "...", just cut)
		if len(line) > popupWidth-4 {
			line = line[:popupWidth-4]
		}
		contentLines = append(contentLines, lineStyle.Render(line))
	}

	// Fill remaining empty lines
	for len(contentLines) < contentHeight {
		contentLines = append(contentLines, "")
	}
	contentStr := strings.Join(contentLines, "\n")

	// Build footer
	var footerText string
	if m.subagentModalScroll > 0 {
		pct := int(float64(m.subagentModalScroll) / float64(max(1, m.subagentModalMaxScroll())) * 100)
		footerText = fmt.Sprintf(" ↑ scrolled %d%%  ↑↓ scroll  PgUp/Dn  Home/End  ESC/Enter close ", pct)
	} else if totalLines > contentHeight {
		footerText = fmt.Sprintf(" ↓ %d more lines  ↑↓ scroll  ESC/Enter close ", totalLines-contentHeight)
	} else {
		footerText = " ↑↓ scroll  ESC/Enter close "
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
		Width(popupWidth).
		MaxWidth(popupWidth).
		Render(box)

	// Place it centered
	placed := lipgloss.Place(
		m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		bordered,
		lipgloss.WithWhitespaceBackground(lipgloss.Color("235")),
		lipgloss.WithWhitespaceChars(" "),
	)
	return placed
}

// renderModelPickerContent renders the model picker content without outer positioning.
func (m TuiModel) renderModelPickerContent() string {
	var b strings.Builder

	// Popup dimensions
	popupWidth := m.width - 8
	if popupWidth < 40 {
		popupWidth = 40
	}
	// Cap max height
	maxPopupHeight := m.height - 4
	if maxPopupHeight < 10 {
		maxPopupHeight = 10
	}

	// Title
	title := " Select Model (type to filter, Enter to select, Esc to cancel) "
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("60")).
		Width(popupWidth - 2).
		Align(lipgloss.Center)
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n")

	// Filter input line - using a simulated input
	filterPrefix := " Filter: "
	filterVal := m.pickerFilter
	if filterVal == "" {
		filterVal = " " // empty but visible cursor
	}
	filterLine := filterPrefix + filterVal
	filterStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("15")).
		Background(lipgloss.Color("236")).
		Width(popupWidth - 2)
	b.WriteString(filterStyle.Render(filterLine))
	b.WriteString("\n")

	// Separator
	sep := strings.Repeat("─", popupWidth-2)
	sepStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Width(popupWidth - 2)
	b.WriteString(sepStyle.Render(sep))
	b.WriteString("\n")

	// Available lines for items
	availableLines := maxPopupHeight - 5 // title + filter + sep + hint + bottom margin
	if availableLines < 1 {
		availableLines = 1
	}

	// Header style
	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("238")).
		Width(popupWidth - 2)
	// Selected item style
	selectedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("0")).
		Background(lipgloss.Color("45")).
		Width(popupWidth - 2)
	// Normal item style
	normalStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Width(popupWidth - 2)

	// Build filtered items with scrolling
	modelIdx := 0
	totalModels := m.pickerModelCount()
	visibleStart := 0
	if totalModels > availableLines {
		visibleStart = m.pickerCursor - availableLines/2
		if visibleStart < 0 {
			visibleStart = 0
		}
		if visibleStart+availableLines > totalModels {
			visibleStart = totalModels - availableLines
		}
	}

	renderedModels := 0
	headerRendered := 0 // tracks total rendered lines (headers + items)

	for _, item := range m.pickerItems {
		if item.isHeader {
			if renderedModels >= visibleStart && renderedModels < visibleStart+availableLines {
				b.WriteString(headerStyle.Render("  " + item.label))
				b.WriteString("\n")
				headerRendered++
			}
			// Headers before visibleStart also count as rendered to push content up
			if renderedModels < visibleStart {
				// This header is before visible range; we need to account for its space
				// but we don't render it
			}
			continue
		}
		isSelected := modelIdx == m.pickerCursor
		isVisible := renderedModels >= visibleStart && renderedModels < visibleStart+availableLines

		if isVisible {
			var line string
			if isSelected {
				line = selectedStyle.Render("▸ " + item.label)
			} else {
				line = normalStyle.Render("  " + item.label)
			}
			b.WriteString(line)
			b.WriteString("\n")
			headerRendered++
		}
		modelIdx++
		renderedModels++
	}

	// Fill remaining lines to maintain popup height
	for headerRendered < availableLines {
		b.WriteString("\n")
		headerRendered++
	}

	// Hint at bottom
	if totalModels == 0 {
		b.WriteString(normalStyle.Render("  No matching models"))
		b.WriteString("\n")
	} else {
		hint := fmt.Sprintf("  %d model(s) — ↑/↓ to navigate, Enter to select, Esc to cancel", totalModels)
		hintStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Width(popupWidth - 2)
		b.WriteString(hintStyle.Render(hint))
		b.WriteString("\n")
	}

	// Wrap in a bordered box
	content := b.String()
	boxStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Width(popupWidth).
		MaxWidth(popupWidth)
	return boxStyle.Render(content)
}

func (m TuiModel) renderBlock(b block) string {
	switch b.kind {
	case "thinking":
		bar := lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Render("│")
		textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("150")).Italic(true)
		maxW := m.width - 2
		if maxW < 10 {
			maxW = 10
		}
		var out strings.Builder
		for _, line := range strings.Split(b.content, "\n") {
			wrapped := wrapText(line, maxW, 0)
			for _, wl := range strings.Split(wrapped, "\n") {
				wl = strings.TrimSuffix(wl, clearLine)
				out.WriteString(bar)
				out.WriteString(" ")
				out.WriteString(textStyle.Render(wl))
				out.WriteString("\n")
			}
		}
		return strings.TrimRight(out.String(), "\n")
	case "text":
		if m.width > 0 {
			maxW := m.width
			if maxW < 10 {
				maxW = 10
			}
			var out strings.Builder
			for _, line := range strings.Split(b.content, "\n") {
				wrapped := wrapText(line, maxW, 0)
				for _, wl := range strings.Split(wrapped, "\n") {
					wl = strings.TrimSuffix(wl, clearLine)
					out.WriteString(wl)
					out.WriteString("\n")
				}
			}
			return strings.TrimRight(out.String(), "\n")
		}
		return b.content
	case "tool":
		return m.renderToolBlock(b)
	case "usage":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("247")).Render("📊 " + b.content)
	case "error":
		bar := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("│")
		textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Italic(true)
		maxW := m.width - 2
		if maxW < 10 {
			maxW = 10
		}
		var out strings.Builder
		for _, line := range strings.Split(b.content, "\n") {
			wrapped := wrapText(line, maxW, 0)
			for _, wl := range strings.Split(wrapped, "\n") {
				wl = strings.TrimSuffix(wl, clearLine)
				out.WriteString(bar)
				out.WriteString(" ")
				out.WriteString(textStyle.Render(wl))
				out.WriteString("\n")
			}
		}
		return strings.TrimRight(out.String(), "\n")
	case "block":
		bar := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("│")
		textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Italic(true)
		maxW := m.width - 2
		if maxW < 10 {
			maxW = 10
		}
		var out strings.Builder
		for _, line := range strings.Split(b.content, "\n") {
			wrapped := wrapText(line, maxW, 0)
			for _, wl := range strings.Split(wrapped, "\n") {
				wl = strings.TrimSuffix(wl, clearLine)
				out.WriteString(bar)
				out.WriteString(" ")
				out.WriteString(textStyle.Render(wl))
				out.WriteString("\n")
			}
		}
		return strings.TrimRight(out.String(), "\n")
	default:
		return b.content
	}
}

func (m TuiModel) renderToolBlock(b block) string {
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("69")).Render("┃")
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Build display line: toolname(arg)
	line := formatToolCall(b.toolName, b.content)

	if b.toolState == "running" {
		line += " ⟳"
	} else if b.toolState == "done" {
		dur := b.duration
		if dur == 0 {
			dur = time.Since(b.startTime) // fallback, shouldn't happen
		}
		line += " " + formatDuration(dur)
		line += " " + hintStyle.Render("- click to display")
	}

	return bar + " " + textStyle.Render(line)
}

// formatToolCall parses the raw JSON tool arguments and returns a human-readable
// summary like "read(main.go)" or "bash(Build display package)".
func formatToolCall(toolName, rawJSON string) string {
	if rawJSON == "" {
		return toolName + "(...)"
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &args); err != nil {
		return toolName + "(...)"
	}

	switch toolName {
	case "read", "write", "edit":
		if path, ok := args["path"].(string); ok && path != "" {
			return toolName + "(" + path + ")"
		}
	case "bash":
		if desc, ok := args["description"].(string); ok && desc != "" {
			return "bash(" + truncateString(desc, 60) + ")"
		}
		if cmd, ok := args["command"].(string); ok && cmd != "" {
			return "bash(" + truncateString(cmd, 60) + ")"
		}
	case "subagent":
		if task, ok := args["task"].(string); ok && task != "" {
			return "subagent(" + truncateString(task, 60) + ")"
		}
	}

	return toolName + "(...)"
}

// formatDuration returns a human-readable duration string.
// Milliseconds under 1s: "23ms". Seconds with 2 decimals: "1.23s".
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "0ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < 10*time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	default:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
}

// extractPath tries to parse JSON and get "path" value.
// Deprecated: use formatToolCall for rendering tool blocks.
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
	prog          *tea.Program
	results       chan string
	modelChanges  chan string
	cancel        chan struct{}    // sent on when ESC pressed during agent run
	done          chan struct{}
}

func NewTUI(modelName string, historyPath string, models []string, allProviders []ProviderModels) *TUI {
	results := make(chan string, 8)
	modelChanges := make(chan string, 8)
	cancel := make(chan struct{}, 1)
	m := newModel(results, modelName, historyPath, models, modelChanges, allProviders, cancel)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	t := &TUI{
		prog:          p,
		results:       results,
		modelChanges:  modelChanges,
		cancel:        cancel,
		done:          make(chan struct{}),
	}

	go func() {
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		}
		close(t.done)
	}()

	return t
}

// ModelChanges returns a channel that receives new model names when the user
// switches model via Tab/Shift+Tab.
func (t *TUI) ModelChanges() <-chan string {
	return t.modelChanges
}

// SetModel updates the model name displayed in the status bar.
func (t *TUI) SetModel(name string) {
	t.prog.Send(tuiMsgBlock{kind: "set-model", content: name})
}

// Results returns the channel that receives submitted lines from the TUI.
func (t *TUI) Results() <-chan string {
	return t.results
}

// DoneCh returns a channel that is closed when the TUI program exits.
func (t *TUI) DoneCh() <-chan struct{} {
	return t.done
}

// CancelCh returns a channel that is sent on when the user presses ESC during
// an agent run, requesting cancellation of the current operation.
func (t *TUI) CancelCh() <-chan struct{} {
	return t.cancel
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

// ResetStatus resets the TUI state to idle/reading after an interruption.
func (t *TUI) ResetStatus() {
	t.post(tuiMsgBlock{kind: "done"})
}

func (t *TUI) Reset() {
	t.post(tuiMsgBlock{kind: "reset"})
}

// ShowTotalUsage displays accumulated total usage after a reset (/new).
// Timing stats (t=, ttft=, tok/s) are per-request and not meaningful for
// session totals, so we build the line manually without them.
func (t *TUI) ShowTotalUsage(usage stream.Usage) {
	line := buildUsageLineNoTiming(usage)
	t.post(tuiMsgBlock{kind: "block", content: "───── new conversation ─────"})
	t.post(tuiMsgBlock{kind: "block", content: "📊 Session total: " + line})
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
