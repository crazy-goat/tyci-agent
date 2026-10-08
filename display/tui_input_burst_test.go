package display

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// wheelBurst returns n mouse wheel events. With esc the events keep their
// leading ESC byte, so bubbletea parses them as MouseMsg. Without it they are
// the stray form that reached the input box as text.
func wheelBurst(n int, esc bool) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if esc {
			b.WriteByte(0x1b)
		}
		fmt.Fprintf(&b, "[<%d;%d;%dM", 64+i%2, 70+i%5, 30+i%7)
	}
	return b.String()
}

// wholeEvents matches output that holds only complete SGR mouse events. A
// Read output that ends inside an event is the bug: bubbletea cannot parse it.
var wholeEvents = regexp.MustCompile(`^(?:\x1b\[<\d+;\d+;\d+[Mm])*$`)

func TestSanitizeReader_BurstSplitAtEveryOffset(t *testing.T) {
	for _, esc := range []bool{true, false} {
		burst := wheelBurst(50, esc)
		want := burst
		if !esc {
			want = ""
		}
		for k := 0; k <= len(burst); k++ {
			src := newConcatReader(strings.NewReader(burst[:k]), strings.NewReader(burst[k:]))
			if got := readSanitized(t, sanitizeInput(src), 256); got != want {
				t.Fatalf("esc=%v split at %d: got %q, want %q", esc, k, got, want)
			}
		}
	}
}

func TestSanitizeReader_ReadsNeverSplitAnEvent(t *testing.T) {
	burst := wheelBurst(50, true)
	r := sanitizeInput(strings.NewReader(burst))
	var out strings.Builder
	buf := make([]byte, 256)
	for {
		n, err := r.Read(buf)
		if !wholeEvents.MatchString(string(buf[:n])) {
			t.Fatalf("a Read of %d bytes ends inside an event: %q", n, buf[:n])
		}
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read error: %v", err)
		}
	}
	if out.String() != burst {
		t.Errorf("burst changed: got %d bytes, want %d", out.Len(), len(burst))
	}
}

func TestSanitizeReader_LoneEscIsReleased(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	r := sanitizeInput(pr)

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 256)
		n, _ := r.Read(buf)
		got <- string(buf[:n])
	}()
	go func() { _, _ = pw.Write([]byte("\x1b")) }()

	select {
	case s := <-got:
		if s != "\x1b" {
			t.Fatalf("got %q, want the lone Esc", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a lone Esc stayed in the reader: no key reached bubbletea")
	}
}

func TestSanitizeReader_PasteKeepsEscapeText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "pasted stray escape is kept",
			in:   "\x1b[200~[<1;2;3M\x1b[201~",
			want: "\x1b[200~[<1;2;3M\x1b[201~",
		},
		{
			name: "stray escape after the paste is dropped",
			in:   "\x1b[200~x\x1b[201~[<1;2;3Mh",
			want: "\x1b[200~x\x1b[201~h",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := readSanitized(t, sanitizeInput(strings.NewReader(c.in)), 256); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// inputRecorder records the input messages bubbletea delivers.
type inputRecorder struct {
	mouse *int
	keys  *[]tea.KeyMsg
}

func (r inputRecorder) Init() tea.Cmd { return nil }

func (r inputRecorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		*r.mouse++
	case tea.KeyMsg:
		*r.keys = append(*r.keys, msg)
	}
	return r, nil
}

func (r inputRecorder) View() string { return "" }

// quitAtEOF quits the program when the sanitized input ends. bubbletea keeps
// running at EOF, so the test needs to stop it.
type quitAtEOF struct {
	r    io.Reader
	quit func()
}

func (q quitAtEOF) Read(p []byte) (int, error) {
	n, err := q.r.Read(p)
	if err == io.EOF {
		q.quit()
	}
	return n, err
}

// runInput feeds input through the sanitizer into bubbletea, as the TUI does,
// and returns the mouse events and keys bubbletea parsed.
func runInput(t *testing.T, src io.Reader) (int, []tea.KeyMsg) {
	t.Helper()
	var mouse int
	var keys []tea.KeyMsg
	var p *tea.Program
	in := quitAtEOF{r: sanitizeInput(src), quit: func() { p.Quit() }}
	p = tea.NewProgram(inputRecorder{mouse: &mouse, keys: &keys},
		tea.WithInput(in),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer())
	if _, err := p.Run(); err != nil {
		t.Fatalf("program: %v", err)
	}
	return mouse, keys
}

func TestInput_BurstBecomesMouseMsgsAtEveryOffset(t *testing.T) {
	burst := wheelBurst(50, true)
	for k := 0; k <= len(burst); k++ {
		src := newConcatReader(strings.NewReader(burst[:k]), strings.NewReader(burst[k:]))
		mouse, keys := runInput(t, src)
		if mouse != 50 || len(keys) != 0 {
			t.Fatalf("split at %d: %d mouse events and %d keys, want 50 and 0 (keys: %v)", k, mouse, len(keys), keys)
		}
	}
}

func TestInput_BurstReadOneByteAtATime(t *testing.T) {
	burst := wheelBurst(50, true)
	mouse, keys := runInput(t, iotest.OneByteReader(strings.NewReader(burst)))
	if mouse != 50 || len(keys) != 0 {
		t.Fatalf("%d mouse events and %d keys, want 50 and 0 (keys: %v)", mouse, len(keys), keys)
	}
}

func TestInput_StrayBurstIsDropped(t *testing.T) {
	mouse, keys := runInput(t, strings.NewReader(wheelBurst(50, false)))
	if mouse != 0 || len(keys) != 0 {
		t.Fatalf("%d mouse events and %d keys, want 0 and 0 (keys: %v)", mouse, len(keys), keys)
	}
}

func TestInput_PastedEscapeTextIsInserted(t *testing.T) {
	_, keys := runInput(t, strings.NewReader("\x1b[200~[<1;2;3M\x1b[201~"))
	if len(keys) != 1 || !keys[0].Paste || string(keys[0].Runes) != "[<1;2;3M" {
		t.Fatalf("pasted text: got keys %v, want one pasted key [<1;2;3M", keys)
	}
}

func TestUpdate_DropsStrayMouseText(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyMsg
		want string
	}{
		{"whole escapes", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<65;71;35M[<65;71;35M")}, ""},
		{"cut tail and whole escapes", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1;35M[<65;71;35M[<65;71;35M")}, ""},
		{"cut tail of three fields", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5;71;35M")}, ""},
		{"pasted escape text is inserted", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("[<1;2;3M"), Paste: true}, "[<1;2;3M"},
		{"typed text is inserted", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")}, "hello"},
		{"a number with a mark is inserted", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("35M")}, "35M"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
			next, _ := m.Update(c.msg)
			if got := next.(TuiModel).input.Value(); got != c.want {
				t.Errorf("input = %q, want %q", got, c.want)
			}
		})
	}
}
