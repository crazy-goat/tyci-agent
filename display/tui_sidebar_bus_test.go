package display

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// busModel returns a model whose Bus tab lists rows, newest first.
func busModel(rows ...TuiBusRow) TuiModel {
	m := newTestModelForSidebar()
	m.busLister = func() []TuiBusRow { return rows }
	m.openSidebar(sidebarTabBus)
	return m
}

// busSnapshotRows is the fixed input of the snapshot tests.
func busSnapshotRows() []TuiBusRow {
	return []TuiBusRow{
		{From: "orchestrator", To: "a1", Kind: "agent.message", Text: "check the build", State: "received"},
		{From: "human", To: "*", Kind: "notice", Text: "stop after this step", State: "sent"},
		{From: "a1", To: "orchestrator", Kind: "ask.request", Text: "need 界界界 a short answer, the text is long", State: "failed"},
	}
}

func TestCutDots_ByDisplayWidth(t *testing.T) {
	cases := []struct {
		name, in string
		width    int
		want     string
	}{
		{"fits", "short", 10, "short"},
		{"ascii", "abcdefghijklmnop", 10, "abcdefg..."},
		{"wide", "界界界界界界", 8, "界界..."},
		{"wide odd", "界界界界界界", 7, "界界..."},
		{"emoji", "📤📤📤📤📤", 9, "📤📤📤..."},
		{"tiny", "abcdef", 2, ".."},
		{"zero", "abcdef", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cutDots(c.in, c.width)
			if got != c.want {
				t.Fatalf("cutDots(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
			}
			if w := cellWidth(got); w > c.width {
				t.Fatalf("cutDots(%q, %d) is %d cells wide", c.in, c.width, w)
			}
		})
	}
}

func TestSidebarBusTab_LineIsCutWithDots(t *testing.T) {
	long := strings.Repeat("word ", 40)
	m := busModel(TuiBusRow{From: "orchestrator", To: "a1", Text: long, State: "sent"})
	lines := m.busListLines(40)
	if len(lines) != 1 {
		t.Fatalf("want one line, got %d: %q", len(lines), lines)
	}
	if !strings.HasSuffix(strings.TrimRight(lines[0], " "), "...") {
		t.Fatalf("line %q does not end with ...", lines[0])
	}
	if w := cellWidth(lines[0]); w != 40 {
		t.Fatalf("line is %d cells wide, want 40", w)
	}
}

func TestSidebarBusTab_RowFormat(t *testing.T) {
	m := busModel(
		TuiBusRow{From: "human", To: "*", Text: "line one\nline two", State: "received"},
		TuiBusRow{From: "orchestrator", To: "a1", Text: "", Kind: "job.status", State: "failed"},
	)
	lines := m.busListLines(60)
	if want := "📥 human -> *: line one line two"; !strings.HasPrefix(lines[0], want) {
		t.Errorf("row 0 = %q, want prefix %q", lines[0], want)
	}
	if want := "❌ orchestrator -> a1: "; !strings.HasPrefix(lines[1], want) {
		t.Errorf("row 1 = %q, want prefix %q", lines[1], want)
	}
}

func TestSidebarBusTab_PaneOpenAndScroll(t *testing.T) {
	text := "alpha " + strings.Repeat("0123456789", 60) + " omega"
	m := busModel(
		TuiBusRow{From: "human", To: "orchestrator", Kind: "agent.message", Text: text, State: "sent"},
		TuiBusRow{From: "a1", To: "orchestrator", Text: "second", State: "sent"},
	)
	if m.sidebarBusPane {
		t.Fatal("pane is open before Enter")
	}
	if got := len(m.sidebarBodyLines(m.sidebarLayout(), 40)); got < 2 {
		t.Fatalf("body has %d lines, want the list", got)
	}

	next, _ := m.sidebarActivateRow()
	m = next.(TuiModel)
	if !m.sidebarBusPane {
		t.Fatal("Enter did not open the pane")
	}
	width := m.sidebarLayout().contentWidth
	pane := m.busPaneLines(width)
	if len(pane) != busPaneHeight {
		t.Fatalf("pane has %d lines, want %d", len(pane), busPaneHeight)
	}
	if !strings.Contains(strings.Join(pane, "\n"), "alpha") {
		t.Fatalf("pane does not show the start of the text: %q", pane)
	}

	m = m.sidebarScrollBusPane(busPaneTextLines)
	if m.sidebarBusScroll == 0 {
		t.Fatal("PgDown did not scroll the pane")
	}
	if strings.Contains(strings.Join(m.busPaneLines(width), "\n"), "alpha") {
		t.Fatal("pane still shows the first line after scrolling")
	}

	// The scroll stops at the last text line, and never goes past it.
	m = m.sidebarScrollBusPane(1000)
	if m.sidebarBusScroll != m.busPaneScrollMax(width) {
		t.Fatalf("scroll = %d, want the maximum %d", m.sidebarBusScroll, m.busPaneScrollMax(width))
	}
	if last := m.busPaneLines(width); !strings.Contains(last[len(last)-1], "mega") {
		t.Fatalf("last pane line %q does not show the end of the text", last[len(last)-1])
	}

	// Moving the cursor starts the new message at the top of its text.
	m.sidebarMoveCursor(1)
	if m.sidebarBusScroll != 0 {
		t.Fatalf("scroll after cursor move = %d, want 0", m.sidebarBusScroll)
	}

	// Enter closes the pane again. PgDown does nothing with a closed pane.
	next, _ = m.sidebarActivateRow()
	m = next.(TuiModel)
	if m.sidebarBusPane {
		t.Fatal("second Enter did not close the pane")
	}
	if got := m.sidebarScrollBusPane(1); got.sidebarBusScroll != 0 {
		t.Fatal("PgDown scrolled a closed pane")
	}
}

func TestSidebarBusTab_PaneKeepsListAtTop(t *testing.T) {
	m := busModel(TuiBusRow{From: "human", To: "a1", Text: "hi", State: "sent"})
	next, _ := m.sidebarActivateRow()
	m = next.(TuiModel)
	layout := m.sidebarLayout()
	lines := m.sidebarBodyLines(layout, layout.contentWidth)
	if len(lines) != layout.contentHeight {
		t.Fatalf("body has %d lines, want contentHeight %d", len(lines), layout.contentHeight)
	}
	if !strings.Contains(lines[0], "human -> a1") {
		t.Fatalf("first body line = %q, want the message row", lines[0])
	}
}

func TestSidebarBusTab_PgKeysThroughUpdate(t *testing.T) {
	text := strings.Repeat("abcdefghij", 60)
	m := busModel(TuiBusRow{From: "human", To: "a1", Text: text, State: "sent"})
	m.sidebarBusPane = true
	next, _ := m.updateSidebar(tea.KeyMsg{Type: tea.KeyPgDown})
	if got := next.(TuiModel).sidebarBusScroll; got != busPaneTextLines {
		t.Fatalf("PgDown through Update: scroll = %d, want %d", got, busPaneTextLines)
	}
}

// TestSidebarBusTab_Snapshot pins the tab at two widths. The lines do not
// depend on the terminal: tests run without a color profile.
func TestSidebarBusTab_Snapshot(t *testing.T) {
	m := busModel(busSnapshotRows()...)
	cases := []struct {
		width int
		want  []string
	}{
		{40, []string{
			"📥 orchestrator -> a1: check the build",
			"📤 human -> *: stop after this step",
			"❌ a1 -> orchestrator: need 界界界 a ...",
		}},
		{60, []string{
			"📥 orchestrator -> a1: check the build",
			"📤 human -> *: stop after this step",
			"❌ a1 -> orchestrator: need 界界界 a short answer, the te...",
		}},
	}
	for _, c := range cases {
		lines := m.busListLines(c.width)
		if len(lines) != len(c.want) {
			t.Fatalf("width %d: %d lines, want %d: %q", c.width, len(lines), len(c.want), lines)
		}
		for i, want := range c.want {
			got := strings.TrimRight(lines[i], " ")
			if got != want {
				t.Errorf("width %d line %d:\n got %q\nwant %q", c.width, i, got, want)
			}
		}
	}
}
