package display

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestFilterStrayMouse_DropsNoLeadEscape(t *testing.T) {
	cases := []struct {
		name string
		in   string
		out  string
	}{
		{
			name: "single stray mouse escape",
			in:   "\x1b[<64;72;56M", // has leading ESC — bubbletea parses as MouseMsg; we MUST pass through
			out:  "\x1b[<64;72;56M",
		},
		{
			name: "stray mouse without leading ESC",
			in:   "[<64;72;56M",
			out:  "",
		},
		{
			name: "multiple stray mouse escapes",
			in:   "[<64;72;56M[<65;72;56M[<65;73;56M[<64;74;55M",
			out:  "",
		},
		{
			name: "user's reported string",
			in:   "[<64;72;56M[<65;72;56M[<65;73;56M[<64;74;55M[<65;74;55M[<65;74;55M",
			out:  "",
		},
		{
			name: "legit text passes through",
			in:   "Hello, world!",
			out:  "Hello, world!",
		},
		{
			name: "legit text with brackets and digits passes through",
			in:   "[1;2;3] foo",
			out:  "[1;2;3] foo",
		},
		{
			name: "real SGR mouse doesn't get stripped on its own",
			in:   "\x1b[<0;33;17M",
			out:  "\x1b[<0;33;17M",
		},
		{
			name: "real SGR with user text mixed in",
			in:   "hello\x1b[<64;72;56Mworld",
			out:  "hello\x1b[<64;72;56Mworld",
		},
		{
			name: "stray mouse surrounded by text",
			in:   "abc[<64;72;56Mdef",
			out:  "abcdef",
		},
		{
			name: "stray mouse followed by lower-case 'm' (release)",
			in:   "abc[<65;72;56mdef",
			out:  "abcdef",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := filterStrayMouse([]byte(c.in))
			if !bytes.Equal(got, []byte(c.out)) {
				t.Errorf("got %q, want %q", string(got), c.out)
			}
		})
	}
}

func TestFilterStrayMouse_PartialPattern(t *testing.T) {
	// Pattern split: "[<10;20" — no terminator yet. filterStrayMouse alone
	// does not see the pattern as complete and passes it through, but
	// filterStrayMouseWithDefer should detect the spill risk.
	in := "[<10;20"
	cleaned := filterStrayMouse([]byte(in))
	if string(cleaned) != in {
		t.Fatalf("filterStrayMouse should pass partial pattern through, got %q", string(cleaned))
	}
	_, deferTail := filterStrayMouseWithDefer([]byte(in), sgrMouseMaxLen)
	if string(deferTail) != in {
		t.Fatalf("filterStrayMouseWithDefer should defer trailing partial pattern, got %q", string(deferTail))
	}
}

func TestSanitizeReader_MultiRead_SplitsPattern(t *testing.T) {
	// Simulate a terminal that delivers the SGR mouse payload in two Reads:
	//  - Read 1: "[<64;72;"
	//  - Read 2: "56Mhi"
	// Sanitizer should drop "[<64;72;56M" and emit only "hi".
	src := newConcatReader(
		strings.NewReader("[<64;72;"),
		strings.NewReader("56Mhi"),
	)
	r := sanitizeInput(src)
	out := bytes.Buffer{}
	buf := make([]byte, 16)
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read error: %v", err)
		}
	}
	if out.String() != "hi" {
		t.Errorf("expected 'hi', got %q", out.String())
	}
}

func TestSanitizeReader_PassesRealMouse(t *testing.T) {
	src := strings.NewReader("hello\x1b[<64;72;56Mworld")
	r := sanitizeInput(src)
	out := bytes.Buffer{}
	buf := make([]byte, 16)
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
	}
	if out.String() != "hello\x1b[<64;72;56Mworld" {
		t.Errorf("real SGR mouse should pass through, got %q", out.String())
	}
}

func TestSanitizeReader_LegitTextBrackets(t *testing.T) {
	// User typing [1;2;3] — must not be eaten.
	src := strings.NewReader("if x [1;2;3] then y")
	r := sanitizeInput(src)
	out := bytes.Buffer{}
	buf := make([]byte, 64)
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
	}
	if out.String() != "if x [1;2;3] then y" {
		t.Errorf("legit text with brackets mis-handled, got %q", out.String())
	}
}

func TestSanitizeReader_BackToBackStrayMice(t *testing.T) {
	src := strings.NewReader("[<64;72;56M[<65;72;56Mhello")
	r := sanitizeInput(src)
	out := bytes.Buffer{}
	buf := make([]byte, 16)
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
	}
	if out.String() != "hello" {
		t.Errorf("back-to-back stray mice should be dropped, got %q", out.String())
	}
}

func TestSanitizeReader_RealMouseSplitAcrossReads(t *testing.T) {
	const mouse = "\x1b[<64;72;56M"
	cases := []struct {
		name  string
		parts []string
	}{
		{name: "after ESC", parts: []string{"\x1b", "[<64;72;56M"}},
		{name: "after ESC bracket", parts: []string{"\x1b[", "<64;72;56M"}},
		{name: "after ESC bracket less-than", parts: []string{"\x1b[<", "64;72;56M"}},
		{name: "in parameters", parts: []string{"\x1b[<64;72;", "56M"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := sanitizeInput(newConcatReader(strings.NewReader(tc.parts[0]), strings.NewReader(tc.parts[1])))
			got := readSanitized(t, r, 2)
			if got != mouse {
				t.Errorf("split real SGR mouse was changed: got %q, want %q", got, mouse)
			}
		})
	}
}

func TestSanitizeReader_BackToBackRealMouseAndText(t *testing.T) {
	const want = "\x1b[<64;72;56M\x1b[<65;72;56mhello"
	r := sanitizeInput(newConcatReader(
		strings.NewReader("\x1b"),
		strings.NewReader("[<64;72;56M\x1b[<65;72;56m"),
		strings.NewReader("hello"),
	))
	if got := readSanitized(t, r, 2); got != want {
		t.Errorf("back-to-back real SGR and text changed: got %q, want %q", got, want)
	}
}

func readSanitized(t *testing.T, r io.Reader, bufferSize int) string {
	t.Helper()
	var out bytes.Buffer
	buf := make([]byte, bufferSize)
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read error: %v", err)
		}
	}
	return out.String()
}

// concatReader concatenates multiple io.Readers end-to-end.
type concatReader struct {
	rds []io.Reader
	i   int
}

func newConcatReader(rds ...io.Reader) *concatReader {
	return &concatReader{rds: rds}
}

func (c *concatReader) Read(p []byte) (int, error) {
	for c.i < len(c.rds) {
		n, err := c.rds[c.i].Read(p)
		if n > 0 {
			return n, err
		}
		if err == io.EOF {
			c.i++
			continue
		}
		return n, err
	}
	return 0, io.EOF
}
