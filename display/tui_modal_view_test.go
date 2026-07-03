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
	m.subagentModalContent.WriteString("line 1\nline 2\nline 3")

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
