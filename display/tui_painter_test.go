package display

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// drainOutput returns everything written since the last read and resets the buf.
func drainOutput(buf *bytes.Buffer) string {
	s := buf.String()
	buf.Reset()
	return s
}

func TestPainterEnterOnFirstPaint(t *testing.T) {
	var out bytes.Buffer
	p := newPainter(&out, false)
	p.paint("hello\nworld", 80, 24)

	got := out.String()
	// First paint must enter the alternate screen, hide the cursor and clear.
	for _, want := range []string{
		ansi.SetMode(ansi.AltScreenBufferMode),
		ansi.HideCursor,
		ansi.EraseEntireScreen,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("first paint missing setup sequence %q\noutput: %q", want, got)
		}
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Fatalf("first paint missing content: %q", got)
	}
	if !p.started {
		t.Fatal("painter should be marked started after first paint")
	}
}

func TestPainterSkipsPaintBeforeSizeKnown(t *testing.T) {
	var out bytes.Buffer
	p := newPainter(&out, false)
	// Width 0 = size not yet known: nothing should be written and the terminal
	// must not be entered (no alt-screen/setup until we can paint correctly).
	p.paint("welcome", 0, 0)
	if out.Len() != 0 {
		t.Fatalf("paint before size known should write nothing, wrote: %q", out.String())
	}
	if p.started {
		t.Fatal("painter should not enter the terminal before a real size arrives")
	}
	// Once a real size arrives it paints and enters.
	p.paint("welcome", 80, 24)
	if out.Len() == 0 || !p.started {
		t.Fatal("paint with a real size should enter and render")
	}
}

func TestPainterEnablesMouseWhenConfigured(t *testing.T) {
	var out bytes.Buffer
	p := newPainter(&out, true)
	p.paint("x", 80, 24)
	if !strings.Contains(out.String(), ansi.SetMode(ansi.MouseCellMotionMode, ansi.MouseSgrExtMode)) {
		t.Fatalf("mouse-enabled painter should emit mouse enable sequence: %q", out.String())
	}

	var out2 bytes.Buffer
	p2 := newPainter(&out2, false)
	p2.paint("x", 80, 24)
	if strings.Contains(out2.String(), ansi.SetMode(ansi.MouseCellMotionMode, ansi.MouseSgrExtMode)) {
		t.Fatal("mouse-disabled painter should not emit mouse enable sequence")
	}
}

func TestPainterStopRestoresTerminal(t *testing.T) {
	var out bytes.Buffer
	p := newPainter(&out, true)
	p.paint("x", 80, 24)
	drainOutput(&out)

	p.stop()
	got := out.String()
	for _, want := range []string{
		ansi.ShowCursor,
		ansi.ResetMode(ansi.MouseCellMotionMode, ansi.MouseSgrExtMode),
		ansi.ResetMode(ansi.BracketedPasteMode),
		ansi.ResetMode(ansi.AltScreenBufferMode),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stop missing restore sequence %q\noutput: %q", want, got)
		}
	}
	if p.started {
		t.Fatal("painter should be marked stopped after stop()")
	}

	// stop() is idempotent: a second call writes nothing.
	drainOutput(&out)
	p.stop()
	if out.Len() != 0 {
		t.Fatalf("second stop() should be a no-op, wrote: %q", out.String())
	}
}

func TestPainterUnchangedFrameIsNoOp(t *testing.T) {
	var out bytes.Buffer
	p := newPainter(&out, false)
	p.paint("a\nb\nc", 80, 24)
	drainOutput(&out)

	p.paint("a\nb\nc", 80, 24)
	if out.Len() != 0 {
		t.Fatalf("re-painting an identical frame should write nothing, wrote: %q", out.String())
	}
}

func TestPainterSkipsUnchangedLines(t *testing.T) {
	var out bytes.Buffer
	p := newPainter(&out, false)
	p.paint("line0\nline1\nline2", 80, 24)
	drainOutput(&out)

	// Only the middle line changes.
	p.paint("line0\nCHANGED\nline2", 80, 24)
	got := out.String()
	if !strings.Contains(got, "CHANGED") {
		t.Fatalf("changed line not painted: %q", got)
	}
	// The unchanged surrounding lines must not be rewritten.
	if strings.Contains(got, "line0") || strings.Contains(got, "line2") {
		t.Fatalf("unchanged lines were rewritten: %q", got)
	}
}

func TestPainterClampsToHeight(t *testing.T) {
	var out bytes.Buffer
	p := newPainter(&out, false)
	// 5 lines into a 3-row screen: only the bottom 3 survive.
	p.paint("l0\nl1\nl2\nl3\nl4", 80, 3)
	got := out.String()
	if strings.Contains(got, "l0") || strings.Contains(got, "l1") {
		t.Fatalf("top lines should be dropped when frame exceeds height: %q", got)
	}
	for _, want := range []string{"l2", "l3", "l4"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bottom line %q should be kept: %q", want, got)
		}
	}
}

func TestPainterTruncatesToWidth(t *testing.T) {
	var out bytes.Buffer
	p := newPainter(&out, false)
	p.paint("abcdefghij", 4, 24) // width 4 → keep 4 cells
	got := ansi.Strip(out.String())
	if strings.Contains(got, "abcde") {
		t.Fatalf("line should be truncated to width 4: %q", got)
	}
	if !strings.Contains(got, "abcd") {
		t.Fatalf("truncated line should keep the first 4 cells: %q", got)
	}
}

func TestPainterRepaintForcesFullRedraw(t *testing.T) {
	var out bytes.Buffer
	p := newPainter(&out, false)
	p.paint("keep\nsame", 80, 24)
	drainOutput(&out)

	// After repaint(), even an identical frame must be fully redrawn.
	p.repaint()
	p.paint("keep\nsame", 80, 24)
	got := out.String()
	if !strings.Contains(got, ansi.EraseEntireScreen) {
		t.Fatalf("repaint should clear the screen: %q", got)
	}
	if !strings.Contains(got, "keep") || !strings.Contains(got, "same") {
		t.Fatalf("repaint should rewrite all lines: %q", got)
	}
}

func TestPainterWrapsInSynchronizedOutput(t *testing.T) {
	var out bytes.Buffer
	p := newPainter(&out, false)
	p.paint("hello", 80, 24)

	got := out.String()
	bsu := ansi.SetMode(ansi.ModeSynchronizedOutput)
	esu := ansi.ResetMode(ansi.ModeSynchronizedOutput)
	bi, ei := strings.Index(got, bsu), strings.LastIndex(got, esu)
	if bi < 0 || ei < 0 {
		t.Fatalf("paint should wrap output in synchronized-output markers: %q", got)
	}
	if bi > ei {
		t.Fatalf("begin-sync must come before end-sync (bi=%d, ei=%d): %q", bi, ei, got)
	}
}

func TestDetectScrollUp(t *testing.T) {
	old := []string{"A", "B", "C", "D", "S", "I"}
	// Region [0,4) scrolled up by 1: A drops off, E appears at the bottom.
	if got := detectScrollUp(old, []string{"B", "C", "D", "E", "S", "I"}, 4); got != 1 {
		t.Errorf("shift by 1: got %d, want 1", got)
	}
	// Scrolled up by 2.
	if got := detectScrollUp(old, []string{"C", "D", "E", "F", "S", "I"}, 4); got != 2 {
		t.Errorf("shift by 2: got %d, want 2", got)
	}
	// Unrelated content: no clean shift.
	if got := detectScrollUp(old, []string{"X", "Y", "Z", "W", "S", "I"}, 4); got != 0 {
		t.Errorf("no shift: got %d, want 0", got)
	}
	// Static region (only lines outside it changed) must not report a scroll.
	if got := detectScrollUp(old, []string{"A", "B", "C", "D", "S2", "I"}, 4); got != 0 {
		t.Errorf("static region: got %d, want 0", got)
	}
	// Region larger than the slices → no scroll.
	if got := detectScrollUp([]string{"A"}, []string{"B"}, 4); got != 0 {
		t.Errorf("short slices: got %d, want 0", got)
	}
}

func TestShiftLinesUp(t *testing.T) {
	lines := []string{"A", "B", "C", "D", "S", "I"}
	shiftLinesUp(lines, 1, 4)
	// Region [0,4) moves up 1; the freed bottom row is cleared; rows outside stay.
	want := []string{"B", "C", "D", "", "S", "I"}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("shiftLinesUp: got %v, want %v", lines, want)
		}
	}
}

func TestPainterScrollsRegionInHardware(t *testing.T) {
	var out bytes.Buffer
	p := newPainter(&out, false)
	// Message region is rows [0,4); "S"/"I" below it are a fixed status/input.
	p.paintRegion("A\nB\nC\nD\nS\nI", 80, 24, 4)
	drainOutput(&out)

	// The log grows one line: A scrolls off, E appears at the bottom of region.
	p.paintRegion("B\nC\nD\nE\nS\nI", 80, 24, 4)
	got := out.String()

	if !strings.Contains(got, ansi.SetTopBottomMargins(1, 4)) {
		t.Errorf("expected scroll region set to the message region: %q", got)
	}
	if !strings.Contains(got, ansi.SU(1)) {
		t.Errorf("expected a hardware scroll-up by 1: %q", got)
	}
	if !strings.Contains(got, ansi.SetTopBottomMargins(1, 24)) {
		t.Errorf("expected the scroll region restored to full screen: %q", got)
	}
	stripped := ansi.Strip(got)
	if !strings.Contains(stripped, "E") {
		t.Errorf("newly revealed line E should be painted: %q", stripped)
	}
	// The scrolled-in-place lines must not be repainted — the terminal moved them.
	for _, gone := range []string{"A", "B", "C", "D"} {
		if strings.Contains(stripped, gone) {
			t.Errorf("line %q should not be repainted after a hardware scroll: %q", gone, stripped)
		}
	}
}

func TestPainterErasesBelowWhenShrinking(t *testing.T) {
	var out bytes.Buffer
	p := newPainter(&out, false)
	p.paint("a\nb\nc\nd", 80, 24)
	drainOutput(&out)

	// Fewer lines than before → leftover rows must be erased.
	p.paint("a\nb", 80, 24)
	if !strings.Contains(out.String(), ansi.EraseScreenBelow) {
		t.Fatalf("shrinking frame should erase leftover lines below: %q", out.String())
	}
}
