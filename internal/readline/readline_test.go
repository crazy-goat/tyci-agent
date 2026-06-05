package readline

import (
	"testing"
)

func TestParseCtrlKey(t *testing.T) {
	tests := []struct {
		name string
		b    byte
		want KeySpecial
	}{
		{"Ctrl+A", 0x01, KeyCtrlA},
		{"Ctrl+B", 0x02, KeyCtrlB},
		{"Ctrl+C", 0x03, KeyCtrlC},
		{"Ctrl+D", 0x04, KeyCtrlD},
		{"Ctrl+E", 0x05, KeyCtrlE},
		{"Ctrl+F", 0x06, KeyCtrlF},
		{"Ctrl+K", 0x0b, KeyCtrlK},
		{"Ctrl+L", 0x0c, KeyCtrlL},
		{"Ctrl+N", 0x0e, KeyCtrlN},
		{"Ctrl+P", 0x10, KeyCtrlP},
		{"Ctrl+R", 0x12, KeyCtrlR},
		{"Ctrl+U", 0x15, KeyCtrlU},
		{"Ctrl+W", 0x17, KeyCtrlW},
		{"Enter (LF)", 0x0a, KeyEnter},
		{"Enter (CR)", 0x0d, KeyEnter},
		{"Backspace (DEL)", 0x7f, KeyBackspace},
		{"Backspace (BS)", 0x08, KeyCtrlH},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCtrlKey(tt.b)
			if got != tt.want {
				t.Errorf("parseCtrlKey(0x%02x) = %v, want %v", tt.b, got, tt.want)
			}
		})
	}
}

func TestParseEscapeSequence(t *testing.T) {
	tests := []struct {
		name string
		seq  []byte
		want KeySpecial
	}{
		{"Up arrow", []byte{0x1b, '[', 'A'}, KeyUp},
		{"Down arrow", []byte{0x1b, '[', 'B'}, KeyDown},
		{"Right arrow", []byte{0x1b, '[', 'C'}, KeyRight},
		{"Left arrow", []byte{0x1b, '[', 'D'}, KeyLeft},
		{"Home", []byte{0x1b, '[', 'H'}, KeyHome},
		{"End", []byte{0x1b, '[', 'F'}, KeyEnd},
		{"Delete", []byte{0x1b, '[', '3', '~'}, KeyDeleteFwd},
		{"Home (1~)", []byte{0x1b, '[', '1', '~'}, KeyHome},
		{"Home (7~)", []byte{0x1b, '[', '7', '~'}, KeyHome},
		{"End (8~)", []byte{0x1b, '[', '8', '~'}, KeyEnd},
		{"Unknown", []byte{0x1b, '[', 'Z'}, KeyEsc},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseEscapeSequence(tt.seq)
			if got.special != tt.want {
				t.Errorf("parseEscapeSequence(%v) = %v, want %v", tt.seq, got.special, tt.want)
			}
		})
	}
}

func TestAltEnterInsertsNewline(t *testing.T) {
	e := &LineEditor{buffer: []rune("hello"), cursorPos: 3}
	done := e.handleKey(key{special: KeyAltEnter})
	if done {
		t.Error("expected not done")
	}
	if string(e.buffer) != "hel\nlo" {
		t.Errorf("buffer = %q, want %q", string(e.buffer), "hel\nlo")
	}
	if e.cursorPos != 4 {
		t.Errorf("cursorPos = %d, want 4", e.cursorPos)
	}
}

func TestLineEditorHandleKey(t *testing.T) {
	t.Run("insert character", func(t *testing.T) {
		e := &LineEditor{buffer: []rune{}}
		k := key{r: 'a'}
		done := e.handleKey(k)
		if done {
			t.Error("expected not done")
		}
		if string(e.buffer) != "a" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "a")
		}
		if e.cursorPos != 1 {
			t.Errorf("cursorPos = %d, want 1", e.cursorPos)
		}
	})

	t.Run("insert multiple characters", func(t *testing.T) {
		e := &LineEditor{buffer: []rune{}}
		for _, r := range "hello" {
			e.handleKey(key{r: r})
		}
		if string(e.buffer) != "hello" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "hello")
		}
		if e.cursorPos != 5 {
			t.Errorf("cursorPos = %d, want 5", e.cursorPos)
		}
	})

	t.Run("backspace", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello"), cursorPos: 5}
		e.handleKey(key{special: KeyBackspace})
		if string(e.buffer) != "hell" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "hell")
		}
		if e.cursorPos != 4 {
			t.Errorf("cursorPos = %d, want 4", e.cursorPos)
		}
	})

	t.Run("backspace at beginning", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello"), cursorPos: 0}
		e.handleKey(key{special: KeyBackspace})
		if string(e.buffer) != "hello" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "hello")
		}
		if e.cursorPos != 0 {
			t.Errorf("cursorPos = %d, want 0", e.cursorPos)
		}
	})

	t.Run("Ctrl+D on empty line returns true", func(t *testing.T) {
		e := &LineEditor{buffer: []rune{}}
		done := e.handleKey(key{special: KeyCtrlD})
		if !done {
			t.Error("expected done (EOF)")
		}
	})

	t.Run("Ctrl+D on non-empty line forward deletes", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello"), cursorPos: 2}
		e.handleKey(key{special: KeyCtrlD})
		if string(e.buffer) != "helo" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "helo")
		}
		if e.cursorPos != 2 {
			t.Errorf("cursorPos = %d, want 2", e.cursorPos)
		}
	})

	t.Run("Ctrl+A moves to start", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello"), cursorPos: 5}
		e.handleKey(key{special: KeyCtrlA})
		if e.cursorPos != 0 {
			t.Errorf("cursorPos = %d, want 0", e.cursorPos)
		}
	})

	t.Run("Ctrl+E moves to end", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello"), cursorPos: 0}
		e.handleKey(key{special: KeyCtrlE})
		if e.cursorPos != 5 {
			t.Errorf("cursorPos = %d, want 5", e.cursorPos)
		}
	})

	t.Run("Ctrl+U deletes to start", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello world"), cursorPos: 6}
		e.handleKey(key{special: KeyCtrlU})
		if string(e.buffer) != "world" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "world")
		}
		if e.cursorPos != 0 {
			t.Errorf("cursorPos = %d, want 0", e.cursorPos)
		}
	})

	t.Run("Ctrl+K deletes to end", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello world"), cursorPos: 5}
		e.handleKey(key{special: KeyCtrlK})
		if string(e.buffer) != "hello" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "hello")
		}
		if e.cursorPos != 5 {
			t.Errorf("cursorPos = %d, want 5", e.cursorPos)
		}
	})

	t.Run("Left arrow moves cursor left", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello"), cursorPos: 3}
		e.handleKey(key{special: KeyLeft})
		if e.cursorPos != 2 {
			t.Errorf("cursorPos = %d, want 2", e.cursorPos)
		}
	})

	t.Run("Right arrow moves cursor right", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello"), cursorPos: 3}
		e.handleKey(key{special: KeyRight})
		if e.cursorPos != 4 {
			t.Errorf("cursorPos = %d, want 4", e.cursorPos)
		}
	})

	t.Run("insert in middle", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("helo"), cursorPos: 2}
		e.handleKey(key{r: 'l'})
		if string(e.buffer) != "hello" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "hello")
		}
		if e.cursorPos != 3 {
			t.Errorf("cursorPos = %d, want 3", e.cursorPos)
		}
	})

	t.Run("UTF-8 character insertion", func(t *testing.T) {
		e := &LineEditor{buffer: []rune{}}
		e.handleKey(key{r: '日'})
		e.handleKey(key{r: '本'})
		if string(e.buffer) != "日本" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "日本")
		}
		if e.cursorPos != 2 {
			t.Errorf("cursorPos = %d, want 2", e.cursorPos)
		}
	})

	t.Run("UTF-8 backspace removes one rune", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("日本"), cursorPos: 2}
		e.handleKey(key{special: KeyBackspace})
		if string(e.buffer) != "日" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "日")
		}
		if e.cursorPos != 1 {
			t.Errorf("cursorPos = %d, want 1", e.cursorPos)
		}
	})

	t.Run("Enter returns true", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello")}
		done := e.handleKey(key{special: KeyEnter})
		if !done {
			t.Error("expected done")
		}
	})

	t.Run("Ctrl+C clears buffer and signals interrupt", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello"), cursorPos: 5, prompt: ">>> "}
		done := e.handleKey(key{special: KeyCtrlC})
		if !done {
			t.Error("expected done (should abort input)")
		}
		if !e.interrupted {
			t.Error("expected interrupted flag")
		}
		if len(e.buffer) != 0 {
			t.Errorf("buffer should be empty, got %q", string(e.buffer))
		}
		if e.cursorPos != 0 {
			t.Errorf("cursorPos = %d, want 0", e.cursorPos)
		}
	})

	t.Run("history navigation up", func(t *testing.T) {
		e := &LineEditor{
			history:    []string{"first", "second", "third"},
			historyPos: 3,
			buffer:     []rune{},
		}
		e.handleKey(key{special: KeyUp})
		if string(e.buffer) != "third" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "third")
		}
		if e.historyPos != 2 {
			t.Errorf("historyPos = %d, want 2", e.historyPos)
		}
	})

	t.Run("history navigation down", func(t *testing.T) {
		e := &LineEditor{
			history:    []string{"first", "second", "third"},
			historyPos: 1,
			buffer:     []rune("second"),
		}
		e.handleKey(key{special: KeyDown})
		if string(e.buffer) != "third" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "third")
		}
		if e.historyPos != 2 {
			t.Errorf("historyPos = %d, want 2", e.historyPos)
		}
	})

	t.Run("history navigation down past end restores draft", func(t *testing.T) {
		e := &LineEditor{
			history:    []string{"first", "second"},
			historyPos: 1,
			buffer:     []rune("second"),
			draft:      "my draft",
		}
		e.handleKey(key{special: KeyDown})
		if string(e.buffer) != "my draft" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "my draft")
		}
		if e.historyPos != 2 {
			t.Errorf("historyPos = %d, want 2", e.historyPos)
		}
	})

	t.Run("history up saves draft on first press", func(t *testing.T) {
		e := &LineEditor{
			history:    []string{"old command"},
			historyPos: 1,
			buffer:     []rune("new command"),
		}
		e.handleKey(key{special: KeyUp})
		if e.draft != "new command" {
			t.Errorf("draft = %q, want %q", e.draft, "new command")
		}
		if string(e.buffer) != "old command" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "old command")
		}
	})
}

func TestMultilineCursorLine(t *testing.T) {
	t.Run("cursorLine on single line", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello")}
		if e.cursorLine() != 0 {
			t.Errorf("cursorLine = %d, want 0", e.cursorLine())
		}
	})

	t.Run("cursorLine on second line", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef"), cursorPos: 6}
		if e.cursorLine() != 1 {
			t.Errorf("cursorLine = %d, want 1", e.cursorLine())
		}
	})

	t.Run("cursorCol on first line", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef"), cursorPos: 2}
		if e.cursorCol() != 2 {
			t.Errorf("cursorCol = %d, want 2", e.cursorCol())
		}
	})

	t.Run("cursorCol on second line", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef"), cursorPos: 5}
		if e.cursorCol() != 1 {
			t.Errorf("cursorCol = %d, want 1", e.cursorCol())
		}
	})

	t.Run("lineCount", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef\nghi")}
		if e.lineCount() != 3 {
			t.Errorf("lineCount = %d, want 3", e.lineCount())
		}
	})

	t.Run("lineCount single", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello")}
		if e.lineCount() != 1 {
			t.Errorf("lineCount = %d, want 1", e.lineCount())
		}
	})

	t.Run("lineStart first line", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef")}
		if e.lineStart(0) != 0 {
			t.Errorf("lineStart(0) = %d, want 0", e.lineStart(0))
		}
	})

	t.Run("lineStart second line", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef")}
		if e.lineStart(1) != 4 {
			t.Errorf("lineStart(1) = %d, want 4", e.lineStart(1))
		}
	})

	t.Run("lineEnd first line", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef")}
		if e.lineEnd(0) != 3 {
			t.Errorf("lineEnd(0) = %d, want 3", e.lineEnd(0))
		}
	})

	t.Run("lineEnd last line", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef")}
		if e.lineEnd(1) != 7 {
			t.Errorf("lineEnd(1) = %d, want 7", e.lineEnd(1))
		}
	})

	t.Run("lineEnd with no trailing newline", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("hello")}
		if e.lineEnd(0) != 5 {
			t.Errorf("lineEnd(0) = %d, want 5", e.lineEnd(0))
		}
	})
}

func TestMultilineKeyNavigation(t *testing.T) {
	t.Run("up arrow moves to previous line", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef"), cursorPos: 6}
		e.handleKey(key{special: KeyUp})
		if e.cursorPos != 2 {
			t.Errorf("cursorPos = %d, want 2", e.cursorPos)
		}
	})

	t.Run("up arrow at first line navigates history", func(t *testing.T) {
		e := &LineEditor{
			buffer: []rune("current"), cursorPos: 7,
			history: []string{"previous"}, historyPos: 1,
		}
		e.handleKey(key{special: KeyUp})
		if string(e.buffer) != "previous" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "previous")
		}
	})

	t.Run("down arrow keeps column position", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef"), cursorPos: 2}
		e.handleKey(key{special: KeyDown})
		if e.cursorPos != 6 {
			t.Errorf("cursorPos = %d, want 6 (col 2 on line 1)", e.cursorPos)
		}
	})

	t.Run("down arrow at last line navigates history", func(t *testing.T) {
		e := &LineEditor{
			buffer: []rune("current"), cursorPos: 7,
			history: []string{"prev"}, draft: "draft", historyPos: 0,
		}
		e.handleKey(key{special: KeyDown})
		if e.draft != "" || string(e.buffer) != "draft" {
			t.Errorf("should restore draft, got buffer=%q draft=%q", string(e.buffer), e.draft)
		}
	})

	t.Run("up clamps to end of shorter line", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("a\nbcd"), cursorPos: 5}
		e.handleKey(key{special: KeyUp})
		if e.cursorPos != 1 {
			t.Errorf("cursorPos = %d, want 1", e.cursorPos)
		}
	})

	t.Run("down clamps to end of shorter line", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\nd"), cursorPos: 2}
		e.handleKey(key{special: KeyDown})
		if e.cursorPos != 5 {
			t.Errorf("cursorPos = %d, want 5 (end of line 1)", e.cursorPos)
		}
	})

	t.Run("Home goes to line start", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef"), cursorPos: 6}
		e.handleKey(key{special: KeyHome})
		if e.cursorPos != 4 {
			t.Errorf("cursorPos = %d, want 4", e.cursorPos)
		}
	})

	t.Run("End goes to line end", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef"), cursorPos: 4}
		e.handleKey(key{special: KeyEnd})
		if e.cursorPos != 7 {
			t.Errorf("cursorPos = %d, want 7", e.cursorPos)
		}
	})

	t.Run("Ctrl+U deletes to line start", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef"), cursorPos: 6}
		e.handleKey(key{special: KeyCtrlU})
		if string(e.buffer) != "abc\nf" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "abc\nf")
		}
		if e.cursorPos != 4 {
			t.Errorf("cursorPos = %d, want 4", e.cursorPos)
		}
	})

	t.Run("Ctrl+K deletes to line end", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef"), cursorPos: 1}
		e.handleKey(key{special: KeyCtrlK})
		if string(e.buffer) != "a\ndef" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "a\ndef")
		}
		if e.cursorPos != 1 {
			t.Errorf("cursorPos = %d, want 1", e.cursorPos)
		}
	})

	t.Run("Ctrl+K at line end does nothing", func(t *testing.T) {
		e := &LineEditor{buffer: []rune("abc\ndef"), cursorPos: 3}
		e.handleKey(key{special: KeyCtrlK})
		if string(e.buffer) != "abc\ndef" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "abc\ndef")
		}
	})
}

func TestLineEditorSearch(t *testing.T) {
	t.Run("enter search mode", func(t *testing.T) {
		e := &LineEditor{
			history:    []string{"hello world", "foo bar"},
			historyPos: 2,
			buffer:     []rune{},
		}
		e.enterSearch()
		if !e.searching {
			t.Error("expected searching to be true")
		}
	})

	t.Run("search finds match (case-insensitive)", func(t *testing.T) {
		e := &LineEditor{
			history:    []string{"Hello World", "foo bar"},
			historyPos: 2,
			buffer:     []rune{},
		}
		e.enterSearch()
		e.searchQuery = []rune("hello")
		e.searchUpdateQuery(e.searchQuery)
		if string(e.buffer) != "Hello World" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "Hello World")
		}
	})

	t.Run("search cycles to older match", func(t *testing.T) {
		e := &LineEditor{
			history:    []string{"hello", "hello world", "foo hello"},
			historyPos: 3,
			buffer:     []rune{},
		}
		e.enterSearch()
		e.searchQuery = []rune("hello")
		e.searchUpdateQuery(e.searchQuery)
		if string(e.buffer) != "foo hello" {
			t.Errorf("first match: buffer = %q, want %q", string(e.buffer), "foo hello")
		}
		e.searchNextOlder()
		if string(e.buffer) != "hello world" {
			t.Errorf("second match: buffer = %q, want %q", string(e.buffer), "hello world")
		}
	})

	t.Run("search exit with accept keeps match", func(t *testing.T) {
		e := &LineEditor{
			history:    []string{"hello world"},
			historyPos: 1,
			buffer:     []rune{},
		}
		e.enterSearch()
		e.searchQuery = []rune("hello")
		e.searchUpdateQuery(e.searchQuery)
		e.exitSearch(true)
		if string(e.buffer) != "hello world" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "hello world")
		}
		if e.searching {
			t.Error("expected searching to be false")
		}
	})

	t.Run("search exit with cancel restores pre-search buffer", func(t *testing.T) {
		e := &LineEditor{
			history:    []string{"hello world"},
			historyPos: 1,
			buffer:     []rune("my draft"),
		}
		e.enterSearch()
		e.searchBuf = []rune("my draft")
		e.searchQuery = []rune("hello")
		e.searchUpdateQuery(e.searchQuery)
		e.exitSearch(false)
		if string(e.buffer) != "my draft" {
			t.Errorf("buffer = %q, want %q", string(e.buffer), "my draft")
		}
		if e.searching {
			t.Error("expected searching to be false")
		}
	})
}

func TestLineEditorAddHistory(t *testing.T) {
	e := &LineEditor{
		history:    []string{},
		maxHistory: 3,
		historyPos: 0,
	}

	e.AddHistory("first")
	if len(e.history) != 1 || e.history[0] != "first" {
		t.Errorf("history = %v, want [first]", e.history)
	}

	e.AddHistory("second")
	if len(e.history) != 2 {
		t.Errorf("expected 2 entries, got %d", len(e.history))
	}

	e.AddHistory("second")
	if len(e.history) != 2 {
		t.Errorf("duplicate should not be added, got %d entries", len(e.history))
	}

	e.AddHistory("third")
	e.AddHistory("fourth")
	if len(e.history) != 3 {
		t.Errorf("expected max 3 entries, got %d", len(e.history))
	}
	if e.history[0] != "second" || e.history[2] != "fourth" {
		t.Errorf("history rotation wrong: %v", e.history)
	}

	e.AddHistory("")
	if len(e.history) != 3 {
		t.Errorf("empty line should not be added")
	}
}
