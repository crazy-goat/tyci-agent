package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestSubagentModalView_RightBorderVisible(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 120
	m.height = 40
	m.subagentModalActive = true
	m.subagentModalTitle = "subagent"
	seedModalBlock(&m, "bash", "line 1\nline 2\nline 3")

	view := stripANSI(m.renderSubagentModalView())
	lines := strings.Split(view, "\n")

	var modalLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "╭") || strings.HasPrefix(trimmed, "│") || strings.HasPrefix(trimmed, "╰") {
			modalLines = append(modalLines, line)
		}
	}
	if len(modalLines) < 3 {
		t.Fatalf("expected modal frame lines, got %d", len(modalLines))
	}

	for i, line := range modalLines {
		if lipgloss.Width(line) != m.width {
			t.Fatalf("modal line %d width = %d, want %d: %q", i, lipgloss.Width(line), m.width, line)
		}
		trimmed := strings.TrimRight(line, " ")
		if strings.HasPrefix(strings.TrimSpace(trimmed), "│") && !strings.HasSuffix(trimmed, "│") {
			t.Fatalf("modal line %d should end with right border: %q", i, line)
		}
	}
}

func TestSubagentModal_SmallTerminalUsesLayoutContentHeight(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.width, m.height = 80, 10
	seedModalBlock(&m, "bash", "one\ntwo\nthree\nfour")

	layout := m.subagentModalLayout()
	if layout.contentHeight != 2 {
		t.Fatalf("small terminal content height = %d, want 2", layout.contentHeight)
	}
	if got := m.subagentModalPageSize(); got != layout.contentHeight {
		t.Fatalf("page size = %d, want layout content height %d", got, layout.contentHeight)
	}
	if got := m.subagentModalMaxScroll(); got != 2 {
		t.Fatalf("max scroll = %d, want 2 for four lines and two visible rows", got)
	}
}
