package api

import (
	"strings"
	"testing"
)

// feedAll runs a delta sequence through one filter and returns everything it
// let through, including the final flush.
func feedAll(deltas ...string) string {
	var f controlTokenFilter
	var out strings.Builder
	for _, d := range deltas {
		out.WriteString(f.Feed(d))
	}
	out.WriteString(f.Flush())
	return out.String()
}

func TestFilterLeavesOrdinaryTextAlone(t *testing.T) {
	cases := [][]string{
		{"hello world"},
		{"hel", "lo ", "world"},
		{"func main() {\n\tx := a < b\n}"},
		{"a < b && c > d"},
		{"</div>"},
		{""},
	}
	for _, deltas := range cases {
		want := strings.Join(deltas, "")
		if got := feedAll(deltas...); got != want {
			t.Errorf("%q -> %q", want, got)
		}
	}
}

// TestFilterDropsABareMarker is the shape seen in a real stream: the whole
// marker arrives as one delta of reasoning_content.
func TestFilterDropsABareMarker(t *testing.T) {
	if got := feedAll("thinking ", "｜DSML｜", " more"); got != "thinking  more" {
		t.Fatalf("got %q", got)
	}
}

// TestFilterDropsAMarkerSplitAcrossDeltas is why this is a state machine and
// not a regex: one real stream delivered "<", "｜DSML｜" and "parameter>" as
// three separate deltas, so anything stateless leaves "</parameter>" behind.
func TestFilterDropsAMarkerSplitAcrossDeltas(t *testing.T) {
	got := feedAll("done. ", "<", "｜DSML｜", "parameter>", " next")
	if got != "done.  next" {
		t.Fatalf("got %q", got)
	}
}

func TestFilterDropsAClosingMarker(t *testing.T) {
	got := feedAll("x", "</", "｜DSML｜", "reasoning>", "y")
	if got != "xy" {
		t.Fatalf("got %q", got)
	}
}

func TestFilterDropsSeveralMarkersInOneDelta(t *testing.T) {
	got := feedAll("a<｜DSML｜parameter>b<｜DSML｜reasoning>c")
	if got != "abc" {
		t.Fatalf("got %q", got)
	}
}

// TestFilterReleasesAnUnterminatedMarker: the bias is towards showing text.
// A delimiter that never closes was ordinary content, and swallowing the rest
// of an answer is far worse than printing a stray character.
func TestFilterReleasesAnUnterminatedMarker(t *testing.T) {
	got := feedAll("answer: ", "｜", "this never closes")
	if !strings.Contains(got, "this never closes") {
		t.Fatalf("real text was swallowed: %q", got)
	}
}

func TestFilterReleasesAnOverlongCandidateMidStream(t *testing.T) {
	long := strings.Repeat("x", maxHeldRunes+10)
	got := feedAll("｜", long, " and the rest")
	if !strings.Contains(got, long) {
		t.Fatalf("a candidate past the length cap must be released: %q", got)
	}
	if !strings.Contains(got, "and the rest") {
		t.Fatalf("the stream did not continue: %q", got)
	}
}

// TestFilterHoldsATrailingAngleBracketOnlyBriefly: "<" at the end of a delta
// might begin a marker, so it waits — but it must not be lost if the stream
// ends there.
func TestFilterHoldsATrailingAngleBracketOnlyBriefly(t *testing.T) {
	var f controlTokenFilter
	if got := f.Feed("a <"); got != "a " {
		t.Fatalf("expected the bracket to be held, got %q", got)
	}
	if got := f.Feed("b"); got != "<b" {
		t.Fatalf("expected the held bracket to be released, got %q", got)
	}

	var g controlTokenFilter
	g.Feed("ends with <")
	if got := g.Flush(); got != "<" {
		t.Fatalf("flush should release the held bracket, got %q", got)
	}
}

// TestFilterIsPerStream: text and reasoning have independent delta sequences,
// so a marker forming in one must not consume the other's characters.
func TestFilterIsPerStream(t *testing.T) {
	var text, reasoning controlTokenFilter

	reasoning.Feed("<")
	if got := text.Feed("hello"); got != "hello" {
		t.Fatalf("the text stream was affected by the reasoning stream: %q", got)
	}
	if got := reasoning.Feed("｜DSML｜x>"); got != "" {
		t.Fatalf("the reasoning marker was not dropped: %q", got)
	}
}

// ---------------------------------------------------------------------------
// ChatML-family markers
// ---------------------------------------------------------------------------

// TestFilterDropsChatMLMarkers covers the other half of the field: ChatML and
// its descendants delimit with ASCII "<|...|>" rather than U+FF5C, and those
// models (Qwen, Yi, Llama 3, and anything served through a ChatML template)
// are at least as common as DeepSeek.
func TestFilterDropsChatMLMarkers(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"<|im_start|>assistant", "assistant"},
		{"done<|im_end|>", "done"},
		{"a<|eot_id|>b", "ab"},
		{"<|endoftext|>", ""},
		{"x<|start_header_id|>y<|end_header_id|>z", "xyz"},
	}
	for _, tc := range cases {
		if got := feedAll(tc.in); got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFilterDropsChatMLMarkersSplitAcrossDeltas(t *testing.T) {
	got := feedAll("thinking ", "<", "|im_", "end|", ">", " done")
	if got != "thinking  done" {
		t.Fatalf("got %q", got)
	}
}

// TestFilterKeepsOrdinaryPipes is the reason the ASCII shape requires both
// delimiters: a bare "|" is everywhere — shell pipes, Go operators, markdown
// tables — and eating those would destroy real answers.
func TestFilterKeepsOrdinaryPipes(t *testing.T) {
	cases := []string{
		"ls | grep foo",
		"flags := a | b | c",
		"| col | col |",
		"if x < y | z > w",
		"a <| b",          // an opener with no closer: released verbatim
		"cat a.txt|wc -l", //
	}
	for _, in := range cases {
		if got := feedAll(in); got != in {
			t.Errorf("%q was rewritten to %q", in, got)
		}
	}
}

// TestFilterHandlesBothShapesInOneStream: nothing says a broken gateway leaks
// only one family, and the scan has to pick the earliest opener rather than
// whichever it looks for first.
func TestFilterHandlesBothShapesInOneStream(t *testing.T) {
	got := feedAll("a<|im_end|>b<｜DSML｜x>c｜DSML｜d")
	if got != "abcd" {
		t.Fatalf("got %q", got)
	}
}
