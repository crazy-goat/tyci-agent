package display

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ---------------------------------------------------------------------------
// Token detection
// ---------------------------------------------------------------------------

func TestFileCompleteTokenDetection(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{"bare @", "@", "", true},
		{"partial path", "look at @display/tui", "display/tui", true},
		{"@ after newline", "first line\n@tui", "tui", true},
		{"no @ at all", "just a sentence", "", false},
		// The whole point of requiring word-start: e-mail addresses and Go
		// struct tags in a pasted snippet must not open a file popup.
		{"email address", "mail piotr@example.com", "", false},
		// A completed token is followed by a space, and the person has moved
		// on; keeping the popup open would steal their next Up/Down.
		{"already finished", "@display/tui.go and then", "", false},
		{"space right after @", "@ tui", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at, query, ok := fileCompleteToken(tc.input, len(tc.input))
			if ok != tc.ok {
				t.Fatalf("ok=%v want %v (query=%q)", ok, tc.ok, query)
			}
			if !ok {
				return
			}
			if query != tc.want {
				t.Fatalf("query=%q want %q", query, tc.want)
			}
			if tc.input[at] != '@' {
				t.Fatalf("offset %d does not point at the @: %q", at, tc.input)
			}
		})
	}
}

// TestFileCompleteTokenUsesTheLastAt: with two paths on one line, the popup
// belongs to the one being typed.
func TestFileCompleteTokenUsesTheLastAt(t *testing.T) {
	_, query, ok := fileCompleteToken("compare @a/first.go with @b/sec", len("compare @a/first.go with @b/sec"))
	if !ok || query != "b/sec" {
		t.Fatalf("got %q ok=%v", query, ok)
	}
}

// ---------------------------------------------------------------------------
// Ranking
// ---------------------------------------------------------------------------

func TestRankFileMatchesPrefersTheFilename(t *testing.T) {
	files := []string{
		"internal/tui/helpers/other.go",
		"display/tui.go",
		"docs/tui-architecture.md",
	}
	got := rankFileMatches(files, "tui.go")
	if len(got) == 0 || got[0] != "display/tui.go" {
		t.Fatalf("a filename match should win over a directory match, got %v", got)
	}
}

func TestRankFileMatchesPrefersPrefixOverMiddle(t *testing.T) {
	files := []string{"display/xxtui_view.go", "display/tui_view.go"}
	got := rankFileMatches(files, "tui")
	if got[0] != "display/tui_view.go" {
		t.Fatalf("prefix of the filename should rank first, got %v", got)
	}
}

func TestRankFileMatchesIsCaseInsensitive(t *testing.T) {
	got := rankFileMatches([]string{"cmd/Main.go"}, "main")
	if len(got) != 1 {
		t.Fatalf("expected a case-insensitive match, got %v", got)
	}
}

func TestRankFileMatchesEmptyQueryListsSomething(t *testing.T) {
	files := []string{"a.go", "b.go", "c.go"}
	got := rankFileMatches(files, "")
	if len(got) == 0 {
		t.Fatal("typing just @ should show candidates, not an empty list")
	}
}

func TestRankFileMatchesIsCapped(t *testing.T) {
	var files []string
	for i := 0; i < 500; i++ {
		files = append(files, "pkg/thing.go")
	}
	if got := rankFileMatches(files, "thing"); len(got) > fileCompleteMaxVisible {
		t.Fatalf("returned %d candidates; the popup shows at most %d", len(got), fileCompleteMaxVisible)
	}
}

func TestRankFileMatchesNoMatch(t *testing.T) {
	if got := rankFileMatches([]string{"a.go"}, "zzzz"); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

// TestRankFileMatchesOrderIsStable matters for usability: if the order
// shuffled between keystrokes the highlighted entry would move under the
// person's fingers.
func TestRankFileMatchesOrderIsStable(t *testing.T) {
	files := []string{"a/x.go", "b/x.go", "c/x.go"}
	first := rankFileMatches(files, "x")
	for i := 0; i < 5; i++ {
		if got := rankFileMatches(files, "x"); strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("order changed: %v then %v", first, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Scanning
// ---------------------------------------------------------------------------

func TestScanProjectFilesSkipsNoise(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		t.Helper()
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mk("main.go")
	mk("pkg/util.go")
	mk(".git/config")
	mk("node_modules/dep/index.js")
	mk(".hidden-file")

	files, truncated := scanProjectFiles(root)
	if truncated {
		t.Fatal("a five-file tree should not be truncated")
	}

	set := map[string]bool{}
	for _, f := range files {
		set[f] = true
	}
	for _, want := range []string{"main.go", "pkg/util.go"} {
		if !set[want] {
			t.Errorf("%q missing from %v", want, files)
		}
	}
	for _, unwanted := range []string{".git/config", "node_modules/dep/index.js", ".hidden-file"} {
		if set[unwanted] {
			t.Errorf("%q should have been skipped", unwanted)
		}
	}
}

func TestScanProjectFilesReturnsRelativeSlashPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "b", "c.go"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	files, _ := scanProjectFiles(root)
	if len(files) != 1 || files[0] != "a/b/c.go" {
		t.Fatalf("got %v", files)
	}
}

// ---------------------------------------------------------------------------
// Model behaviour
// ---------------------------------------------------------------------------

// newFileCompleteTestModel builds a bare TUI model the way the other display
// tests do.
func newFileCompleteTestModel() TuiModel {
	return newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
}

func newFileCompleteModel(files []string) TuiModel {
	m := newFileCompleteTestModel()
	m.width = 80
	m.height = 24
	m.fileIndex = files
	m.fileIndexBuiltAt = time.Now()
	return m
}

func TestRefreshFileCompleteOpensAndFilters(t *testing.T) {
	m := newFileCompleteModel([]string{"display/tui.go", "main.go", "tools/bash.go"})

	m.input.SetValue("open @tui")
	if cmd := m.refreshFileComplete(); cmd != nil {
		t.Fatal("a fresh index should not trigger another scan")
	}
	if !m.fileCompleteActive {
		t.Fatal("popup should be open")
	}
	if len(m.fileCompleteItems) != 1 || m.fileCompleteItems[0] != "display/tui.go" {
		t.Fatalf("got %v", m.fileCompleteItems)
	}
}

func TestRefreshFileCompleteClosesWhenTokenEnds(t *testing.T) {
	m := newFileCompleteModel([]string{"main.go"})
	m.input.SetValue("@main")
	m.refreshFileComplete()
	if !m.fileCompleteActive {
		t.Fatal("expected open")
	}

	m.input.SetValue("@main.go now do something")
	m.refreshFileComplete()
	if m.fileCompleteActive {
		t.Fatal("popup should close once the person moved past the path")
	}
}

func TestRefreshFileCompleteRequestsAScanWhenIndexIsMissing(t *testing.T) {
	m := newFileCompleteTestModel()
	m.input.SetValue("@x")

	cmd := m.refreshFileComplete()
	if cmd == nil {
		t.Fatal("the first @ must kick off a file scan")
	}
	if !m.fileIndexPending {
		t.Fatal("pending flag not set, so the popup cannot say why it is empty")
	}
	// While it is in flight, a second keystroke must not start another walk.
	m.input.SetValue("@xy")
	if again := m.refreshFileComplete(); again != nil {
		t.Fatal("a scan is already running; a second one is wasted work")
	}
}

func TestRefreshFileCompleteRescansStaleIndex(t *testing.T) {
	m := newFileCompleteModel([]string{"old.go"})
	m.fileIndexBuiltAt = time.Now().Add(-2 * fileIndexTTL)

	m.input.SetValue("@x")
	if cmd := m.refreshFileComplete(); cmd == nil {
		t.Fatal("a stale index should be rebuilt, or files created this session never appear")
	}
}

func TestApplyFileIndexFillsAnOpenPopup(t *testing.T) {
	m := newFileCompleteTestModel()
	m.input.SetValue("@main")
	m.refreshFileComplete()
	if len(m.fileCompleteItems) != 0 {
		t.Fatal("nothing can be matched before the scan lands")
	}

	m.applyFileIndex(tuiFileIndexMsg{files: []string{"main.go", "other.go"}})

	if m.fileIndexPending {
		t.Fatal("pending flag should be cleared")
	}
	if len(m.fileCompleteItems) != 1 || m.fileCompleteItems[0] != "main.go" {
		t.Fatalf("the popup should fill itself in when the scan lands, got %v", m.fileCompleteItems)
	}
}

func TestAcceptFileCompleteReplacesTheToken(t *testing.T) {
	m := newFileCompleteModel([]string{"display/tui.go"})
	m.input.SetValue("please read @tui")
	m.refreshFileComplete()

	m.acceptFileComplete()

	if got := m.input.Value(); got != "please read display/tui.go " {
		t.Fatalf("got %q", got)
	}
	if m.fileCompleteActive {
		t.Fatal("popup should close after accepting")
	}
}

func TestAcceptFileCompleteUsesTheHighlightedEntry(t *testing.T) {
	m := newFileCompleteModel([]string{"a/tui.go", "b/tui.go"})
	m.input.SetValue("@tui")
	m.refreshFileComplete()
	if len(m.fileCompleteItems) != 2 {
		t.Fatalf("setup: got %v", m.fileCompleteItems)
	}

	m.moveFileCompleteCursor(1)
	second := m.fileCompleteItems[1]
	m.acceptFileComplete()

	if !strings.Contains(m.input.Value(), second) {
		t.Fatalf("expected %q in %q", second, m.input.Value())
	}
}

func TestAcceptFileCompleteWithNoMatchesDoesNothing(t *testing.T) {
	m := newFileCompleteModel([]string{"main.go"})
	m.input.SetValue("@zzzz")
	m.refreshFileComplete()

	m.acceptFileComplete()
	if m.input.Value() != "@zzzz" {
		t.Fatalf("input should be untouched, got %q", m.input.Value())
	}
}

func TestMoveFileCompleteCursorWraps(t *testing.T) {
	m := newFileCompleteModel([]string{"a/x.go", "b/x.go"})
	m.input.SetValue("@x")
	m.refreshFileComplete()

	m.moveFileCompleteCursor(-1)
	if m.fileCompleteCursor != len(m.fileCompleteItems)-1 {
		t.Fatalf("Up from the top should wrap to the bottom, got %d", m.fileCompleteCursor)
	}
	m.moveFileCompleteCursor(1)
	if m.fileCompleteCursor != 0 {
		t.Fatalf("got %d", m.fileCompleteCursor)
	}
}

// TestFileCompleteKeysAreClaimedOnlyWhenOpen is what keeps the feature from
// breaking history navigation: Up and Down must go back to their normal jobs
// the moment the popup is closed.
func TestFileCompleteKeysAreClaimedOnlyWhenOpen(t *testing.T) {
	m := newFileCompleteModel([]string{"main.go"})

	for _, key := range []tea.KeyType{tea.KeyUp, tea.KeyDown, tea.KeyTab, tea.KeyEscape} {
		if m.handleFileCompleteKey(tea.KeyMsg{Type: key}) {
			t.Fatalf("key %v was claimed while the popup was closed", key)
		}
	}

	m.input.SetValue("@main")
	m.refreshFileComplete()
	for _, key := range []tea.KeyType{tea.KeyUp, tea.KeyDown} {
		if !m.handleFileCompleteKey(tea.KeyMsg{Type: key}) {
			t.Fatalf("key %v should move through the candidate list while open", key)
		}
	}
}

// TestFileCompleteEnterFallsThroughWithNoMatches: with an empty list the
// popup is only a hint, and Enter must still send the line.
func TestFileCompleteEnterFallsThroughWithNoMatches(t *testing.T) {
	m := newFileCompleteModel([]string{"main.go"})
	m.input.SetValue("@zzzz")
	m.refreshFileComplete()

	if m.handleFileCompleteKey(tea.KeyMsg{Type: tea.KeyEnter}) {
		t.Fatal("Enter should submit when there is nothing to accept")
	}
}

func TestFileCompleteEnterAcceptsWhenThereIsAMatch(t *testing.T) {
	m := newFileCompleteModel([]string{"main.go"})
	m.input.SetValue("@main")
	m.refreshFileComplete()

	if !m.handleFileCompleteKey(tea.KeyMsg{Type: tea.KeyEnter}) {
		t.Fatal("Enter should accept the highlighted path")
	}
	if !strings.Contains(m.input.Value(), "main.go") {
		t.Fatalf("got %q", m.input.Value())
	}
}

func TestFileCompleteEscapeClosesWithoutClearingInput(t *testing.T) {
	m := newFileCompleteModel([]string{"main.go"})
	m.input.SetValue("keep this @main")
	m.refreshFileComplete()

	if !m.handleFileCompleteKey(tea.KeyMsg{Type: tea.KeyEscape}) {
		t.Fatal("Escape should be claimed by the popup")
	}
	if m.fileCompleteActive {
		t.Fatal("popup should be closed")
	}
	if m.input.Value() != "keep this @main" {
		t.Fatalf("Escape must dismiss the popup, not the typed line: %q", m.input.Value())
	}
}

// ---------------------------------------------------------------------------
// Rendering / layout
// ---------------------------------------------------------------------------

// TestFileCompleteHeightMatchesRenderedLines is the layout invariant: the
// frame subtracts fileCompleteHeight from the message region, so a mismatch
// makes the transcript jump on every keystroke.
func TestFileCompleteHeightMatchesRenderedLines(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*TuiModel)
	}{
		{"closed", func(m *TuiModel) {}},
		{"scanning", func(m *TuiModel) {
			m.fileCompleteActive = true
			m.fileIndexPending = true
		}},
		{"no match", func(m *TuiModel) {
			m.fileCompleteActive = true
		}},
		{"three matches", func(m *TuiModel) {
			m.fileCompleteActive = true
			m.fileCompleteItems = []string{"a.go", "b.go", "c.go"}
		}},
		{"truncated index", func(m *TuiModel) {
			m.fileCompleteActive = true
			m.fileCompleteItems = []string{"a.go"}
			m.fileIndexTruncated = true
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newFileCompleteTestModel()
			m.width = 80
			tc.setup(&m)

			rendered := m.renderFileComplete(m.width)
			lines := 0
			if rendered != "" {
				lines = strings.Count(rendered, "\n")
			}
			if got := m.fileCompleteHeight(); got != lines {
				t.Fatalf("fileCompleteHeight()=%d but the popup draws %d lines", got, lines)
			}
		})
	}
}

func TestRenderFileCompleteIsEmptyWhenClosed(t *testing.T) {
	m := newFileCompleteTestModel()
	m.width = 80
	if got := m.renderFileComplete(80); got != "" {
		t.Fatalf("layout must be untouched for anyone who never types @, got %q", got)
	}
}

func TestTruncateForWidthKeepsTheFilename(t *testing.T) {
	long := "very/deeply/nested/directory/structure/final.go"
	got := truncateForWidth(long, 20)
	if len([]rune(got)) > 20 {
		t.Fatalf("got %d runes: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "final.go") {
		t.Fatalf("the matched filename must stay visible, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Completing in the middle of a line
// ---------------------------------------------------------------------------

// TestFileCompleteTokenMidText is the bug this cursor handling exists for:
// with the token judged against the end of the input, typing "@tui" anywhere
// but last offered nothing.
func TestFileCompleteTokenMidText(t *testing.T) {
	const value = "compare @tui with the old one"
	cursor := strings.Index(value, " with")

	at, query, ok := fileCompleteToken(value, cursor)
	if !ok {
		t.Fatal("a token the cursor sits in must be completable, wherever it is on the line")
	}
	if query != "tui" {
		t.Fatalf("query=%q want %q", query, "tui")
	}
	if value[at] != '@' {
		t.Fatalf("offset %d does not point at the @: %q", at, value)
	}
}

// TestFileCompleteTokenStopsAtTheCursor: text typed after the token is not
// part of the query, even when it has no space in it.
func TestFileCompleteTokenStopsAtTheCursor(t *testing.T) {
	const value = "@displayXXX"
	_, query, ok := fileCompleteToken(value, len("@display"))
	if !ok || query != "display" {
		t.Fatalf("query=%q ok=%v", query, ok)
	}
}

// TestFileCompleteTokenIgnoresLaterAt: a second "@" further along the line
// belongs to a different token and must not hijack the cursor's one.
func TestFileCompleteTokenIgnoresLaterAt(t *testing.T) {
	const value = "read @tui and also @main.go"
	_, query, ok := fileCompleteToken(value, len("read @tui"))
	if !ok || query != "tui" {
		t.Fatalf("query=%q ok=%v", query, ok)
	}
}

// moveCursorLeft walks the input cursor back the way the arrow key does.
func moveCursorLeft(m *TuiModel, n int) {
	for i := 0; i < n; i++ {
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyLeft})
	}
}

func TestInputCursorOffsetTracksTheTextarea(t *testing.T) {
	m := newFileCompleteModel(nil)

	const value = "first line\nsecond @tui line"
	m.input.SetValue(value)
	if got := m.inputCursorOffset(); got != len(value) {
		t.Fatalf("SetValue leaves the cursor at the end: got %d want %d", got, len(value))
	}

	moveCursorLeft(&m, len(" line"))
	want := strings.LastIndex(value, " line")
	if got := m.inputCursorOffset(); got != want {
		t.Fatalf("got %d want %d (crossing a row boundary is where this goes wrong)", got, want)
	}
}

func TestRefreshFileCompleteOpensWhenTheCursorMovesBackIntoAToken(t *testing.T) {
	m := newFileCompleteModel([]string{"display/tui.go"})

	m.input.SetValue("compare @tui with the old one")
	if m.refreshFileComplete(); m.fileCompleteActive {
		t.Fatal("the cursor is past the token, so nothing should be open yet")
	}

	moveCursorLeft(&m, len(" with the old one"))
	m.refreshFileComplete()
	if !m.fileCompleteActive {
		t.Fatal("the popup must open for the token the cursor is now inside")
	}
	if len(m.fileCompleteItems) != 1 || m.fileCompleteItems[0] != "display/tui.go" {
		t.Fatalf("got %v", m.fileCompleteItems)
	}
}

func TestAcceptFileCompleteKeepsTheRestOfTheLine(t *testing.T) {
	m := newFileCompleteModel([]string{"display/tui.go"})

	m.input.SetValue("compare @tui with the old one")
	moveCursorLeft(&m, len(" with the old one"))
	m.refreshFileComplete()

	m.acceptFileComplete()

	const want = "compare display/tui.go with the old one"
	if got := m.input.Value(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// No second space: the text after the token already provided one.
	if strings.Contains(m.input.Value(), "  ") {
		t.Fatalf("doubled separator: %q", m.input.Value())
	}
	// The cursor belongs right after what was just inserted, so typing
	// continues where the person was, not at the end of the line.
	if got, wantAt := m.inputCursorOffset(), len("compare display/tui.go"); got != wantAt {
		t.Fatalf("cursor at %d want %d", got, wantAt)
	}
}
