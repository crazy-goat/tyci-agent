package display

import (
	"strings"
	"testing"
)

func TestCollapseLeavesOrdinaryTextAlone(t *testing.T) {
	for _, in := range []string{
		"",
		"one line",
		"a\nb\nc",
		"a\na\nb\nb\nb", // short runs are normal output
		"x\n\ny\n\nz",   // single blank separators
	} {
		if got := collapseRepeatedLines(in); got != in {
			t.Errorf("%q -> %q", in, got)
		}
	}
}

// TestCollapseSummarisesARun is the case from a real session: hundreds of
// copies of one line, telling you nothing that one copy and a count do not,
// while costing the whole viewport.
func TestCollapseSummarisesARun(t *testing.T) {
	in := strings.TrimRight(strings.Repeat("</invoke>\n", 40), "\n")

	got := collapseRepeatedLines(in)
	lines := strings.Split(got, "\n")

	if len(lines) != repeatKeep+1 {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), repeatKeep+1, got)
	}
	for i := 0; i < repeatKeep; i++ {
		if lines[i] != "</invoke>" {
			t.Errorf("line %d = %q, want the repeated line", i, lines[i])
		}
	}
	if !strings.Contains(lines[repeatKeep], "38 more identical lines") {
		t.Errorf("summary line = %q", lines[repeatKeep])
	}
}

// TestCollapseKeepsTheSurroundingText: a run in the middle must not take its
// neighbours with it.
func TestCollapseKeepsTheSurroundingText(t *testing.T) {
	in := "before\n" + strings.Repeat("dup\n", 20) + "after"

	got := collapseRepeatedLines(in)
	if !strings.HasPrefix(got, "before\n") {
		t.Errorf("lost the text before the run:\n%s", got)
	}
	if !strings.HasSuffix(got, "\nafter") {
		t.Errorf("lost the text after the run:\n%s", got)
	}
	if strings.Count(got, "dup") != repeatKeep {
		t.Errorf("expected %d copies kept:\n%s", repeatKeep, got)
	}
}

// TestCollapseCapsBlankRuns: one real session opened a thinking block with
// eighteen blank lines, which pushed everything else off the screen.
func TestCollapseCapsBlankRuns(t *testing.T) {
	in := "start" + strings.Repeat("\n", 19) + "end"

	got := collapseRepeatedLines(in)
	if strings.Count(got, "\n") > blankRunKeep+1 {
		t.Fatalf("blank run was not capped: %q", got)
	}
	if !strings.Contains(got, "start") || !strings.Contains(got, "end") {
		t.Fatalf("content was lost: %q", got)
	}
}

// TestCollapseLeavesLongLinesAlone: a long identical line repeated many times
// is more likely to be real content — a generated fixture, a data table — than
// noise.
func TestCollapseLeavesLongLinesAlone(t *testing.T) {
	long := strings.Repeat("x", repeatMaxLineLen+1)
	in := strings.TrimRight(strings.Repeat(long+"\n", 20), "\n")

	if got := collapseRepeatedLines(in); got != in {
		t.Fatal("long repeated content was collapsed")
	}
}

// TestCollapseFastPathAgreesWithTheSlowPath: hasCollapsibleRun exists only to
// avoid rebuilding clean text, so it must answer exactly the question the
// rewrite answers. A disagreement would either cost an allocation on every
// clean block or skip a collapse that was needed.
func TestCollapseFastPathAgreesWithTheSlowPath(t *testing.T) {
	cases := []string{
		"",
		"a",
		"a\nb",
		"a\na",
		strings.Repeat("a\n", repeatRunThreshold-1),
		strings.Repeat("a\n", repeatRunThreshold),
		strings.Repeat("a\n", repeatRunThreshold+5),
		"x\n\n\ny",
		"x\n\n\n\n\n\ny",
		strings.Repeat(strings.Repeat("z", repeatMaxLineLen+1)+"\n", 20),
		"a\nb\na\nb\na\nb\na\nb\na\nb\na\nb",
	}
	for _, in := range cases {
		fast := hasCollapsibleRun(in)
		changed := collapseRepeatedLines(in) != in
		if fast != changed {
			t.Errorf("%q: fast path says %v, rewrite changed=%v", in, fast, changed)
		}
	}
}

// TestCollapseIsTheSameOnEveryRenderPath used to compare a "thinking"
// block's two full-render paths (renderBlock's non-streaming branch and
// forceRenderDirtyBlocks). A thinking block no longer renders its full text
// inline at all — it always collapses to one summary line (see
// tui_thinking_collapsed_test.go) — so repeated-line collapsing on a
// thinking block's *display* no longer applies; collapseRepeatedLines is
// still exercised for "text" blocks by the tests above. This is now a
// narrower check: a "text" block (which still goes through the same two
// render paths) must collapse identically on both.
func TestCollapseIsTheSameOnEveryRenderPath(t *testing.T) {
	content := strings.Repeat("</invoke>\n", 30) + "tail"

	viaRenderBlock := func() string {
		m := newPickerTestModel(testProviders, nil, "")
		m.width = 80
		m.appendOrAppend("text", content)
		m.status = "idle"
		return m.renderBlock(0, m.blocks[0])
	}()

	viaForce := func() string {
		m := newPickerTestModel(testProviders, nil, "")
		m.width = 80
		m.appendOrAppend("text", content)
		m.forceRenderDirtyBlocks()
		return m.mdCacheRendered[0]
	}()

	if viaRenderBlock != viaForce {
		t.Fatalf("the two render paths disagree:\n--- renderBlock ---\n%q\n--- force ---\n%q", viaRenderBlock, viaForce)
	}
	if !strings.Contains(viaForce, "more identical lines") {
		t.Errorf("the run was not collapsed: %q", viaForce)
	}
}
