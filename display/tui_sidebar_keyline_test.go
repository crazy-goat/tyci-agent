package display

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// sidebarTextRows renders the sidebar and returns each row without ANSI
// codes, the left border and the surrounding spaces.
func sidebarTextRows(m TuiModel) []string {
	rows := strings.Split(m.renderSidebarColumn(), "\n")
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = strings.TrimSpace(strings.TrimPrefix(ansi.Strip(row), "│"))
	}
	return out
}

// TestSidebarKeyLine_IsLastRow checks that the key line (sidebarFooter) is
// the last row of the sidebar on every tab, focused or not. Before the fix,
// the empty sidebarHint row was the last row on tabs without a hint. When a
// tab has a hint, the hint must sit directly above the key line.
func TestSidebarKeyLine_IsLastRow(t *testing.T) {
	for _, withLister := range []bool{false, true} {
		for tab := range sidebarTabNames {
			for _, focused := range []bool{false, true} {
				m := newTestModelForSidebar()
				m.sidebarActive = true
				m.sidebarTab = tab
				m.sidebarFocused = focused
				if withLister {
					m.sessionLister = func() []TuiResumeEntry { return nil }
				}
				name := fmt.Sprintf("tab=%s focused=%t lister=%t", sidebarTabNames[tab], focused, withLister)

				rows := sidebarTextRows(m)
				if len(rows) != m.height {
					t.Errorf("%s: sidebar has %d rows, want %d", name, len(rows), m.height)
					continue
				}
				width := m.sidebarLayout().contentWidth
				wantKey := strings.TrimSpace(truncateToWidth(m.sidebarFooter(), width))
				if got := rows[len(rows)-1]; got != wantKey {
					t.Errorf("%s: last row = %q, want the key line %q", name, got, wantKey)
				}
				if hint := m.sidebarHint(); hint != "" {
					wantHint := strings.TrimSpace(truncateToWidth(hint, width))
					if got := rows[len(rows)-2]; got != wantHint {
						t.Errorf("%s: row above the key line = %q, want the hint %q", name, got, wantHint)
					}
				}
			}
		}
	}
}
