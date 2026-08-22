package display

// Follow-the-bottom: the invariant that scrollLine 0 and atBottom mean the same
// thing. When they disagree the transcript looks frozen — content anchored to
// the top of the region, blank space below it, new output never appearing.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newScrollTestModel(t *testing.T, lines int) TuiModel {
	t.Helper()
	m := newPickerTestModel(testProviders, nil, "")
	m.width = 80
	m.height = 20
	m.ready = true
	for i := 0; i < lines; i++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "line " + strings.Repeat("x", 5) + "\n"})
	}
	return m
}

// TestScrollDownToExactlyZeroRestoresFollow is the reported bug. The wheel
// moves three lines at a time, so landing exactly on line 0 is the common case
// — and the old `scrollLine < 0` test left atBottom false there, freezing the
// transcript with a blank half-screen below the last line.
func TestScrollDownToExactlyZeroRestoresFollow(t *testing.T) {
	m := newScrollTestModel(t, 40)

	m.atBottom = false
	m.scrollLine = 9 // an exact multiple of the wheel's step
	for i := 0; i < 3; i++ {
		m.scrollDown(3)
	}

	if m.scrollLine != 0 {
		t.Fatalf("scrollLine = %d, want 0", m.scrollLine)
	}
	if !m.atBottom {
		t.Fatal("landing exactly on line 0 must restore follow-the-bottom")
	}
}

func TestScrollDownPastZeroStillRestoresFollow(t *testing.T) {
	m := newScrollTestModel(t, 40)
	m.atBottom = false
	m.scrollLine = 2

	m.scrollDown(10)

	if m.scrollLine != 0 || !m.atBottom {
		t.Fatalf("scrollLine=%d atBottom=%v", m.scrollLine, m.atBottom)
	}
}

// TestWheelDownRestoresFollow drives the real mouse path, since that is where
// the three-line step comes from.
func TestWheelDownRestoresFollow(t *testing.T) {
	m := newScrollTestModel(t, 40)
	m.atBottom = false
	m.scrollLine = 3

	next, _ := m.handleMouseMsg(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = next.(TuiModel)

	if m.scrollLine != 0 || !m.atBottom {
		t.Fatalf("scrollLine=%d atBottom=%v — the wheel stopped following the transcript", m.scrollLine, m.atBottom)
	}
}

func TestPageDownRestoresFollow(t *testing.T) {
	m := newScrollTestModel(t, 40)
	m.atBottom = false
	m.scrollLine = m.messageRegionHeight() // exactly one page from the bottom

	handled, next, _ := m.handleGlobalKey(tea.KeyMsg{Type: tea.KeyPgDown})
	if !handled {
		t.Fatal("PgDn was not handled")
	}
	m = next.(TuiModel)

	if m.scrollLine != 0 || !m.atBottom {
		t.Fatalf("scrollLine=%d atBottom=%v", m.scrollLine, m.atBottom)
	}
}

// TestClampScrollKeepsTheInvariant: any future path that zeroes scrollLine
// without touching atBottom is corrected rather than left inconsistent.
func TestClampScrollKeepsTheInvariant(t *testing.T) {
	m := newScrollTestModel(t, 40)
	m.atBottom = false
	m.scrollLine = 0

	m.clampScroll()

	if !m.atBottom {
		t.Fatal("scrollLine 0 with atBottom false is the frozen-transcript state")
	}
}

// TestNewOutputIsFollowedAfterScrollingBackDown is the behaviour the user sees:
// scroll up, scroll back down, and the transcript must keep moving.
func TestNewOutputIsFollowedAfterScrollingBackDown(t *testing.T) {
	m := newScrollTestModel(t, 40)

	m.atBottom = false
	m.scrollLine = 6
	m.scrollDown(3)
	m.scrollDown(3)

	m.handleBlockMsg(tuiMsgBlock{kind: "text", content: "NEWEST-LINE\n"})

	if !m.atBottom {
		t.Fatal("not following, so new output would never come into view")
	}
	region := m.buildMessageRegion(m.messageRegionHeight())
	if !strings.Contains(region, "NEWEST-LINE") {
		t.Fatal("the newest line is not on screen")
	}
}
