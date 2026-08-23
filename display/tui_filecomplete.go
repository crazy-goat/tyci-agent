package display

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/decodo/tyci/internal/agentdefs"
)

// @-completion for file paths and named agents in the input line.
//
// Typing "@" opens one list mixing files under the working directory with
// named agents discovered from .tyci/agents/*.md (global then project-local,
// same precedence agentdefs.List already establishes for the subagent tool),
// filtered as you keep typing. Tab or Enter inserts the selection: a file
// becomes "@file:<path>", an agent becomes "@agent:<name>". It saves nothing
// but keystrokes — which is the point: naming a file precisely is the single
// most common thing a person types here, and getting it wrong costs the
// agent a failed tool call and a round trip; naming an agent is the same idea
// one level up, since agentdefs already knows every name that can go there.
//
// The file list is built once, off the UI thread, because walking a large
// repository takes long enough to be felt as a stutter if done inline. Until
// it arrives the popup says so rather than showing an empty list, which would
// read as "no matches". The agent list has no such cost — a handful of small
// markdown files — so it is read fresh on every refresh, the same way
// agentdefs.List already behaves for the subagent tool ("stale is a bug, not
// a feature": an agent definition added mid-session should show up here
// immediately, same as `agents`/`subagent`).

const (
	// fileIndexMaxEntries bounds the index. A repository with more paths than
	// this is one where the person knows what they are looking for and will
	// type enough to narrow it; holding every path would cost memory for no
	// gain.
	fileIndexMaxEntries = 20000

	// fileCompleteMaxVisible is how many candidates the popup shows. Beyond
	// about this many, scanning the list is slower than typing another letter.
	fileCompleteMaxVisible = 8

	// fileIndexTTL is how long a built index is trusted. Files created by the
	// agent mid-session should become completable without a restart, and a
	// rescan is cheap enough to do occasionally.
	fileIndexTTL = 30 * time.Second
)

// Styling mirrors the queue panel (tui_queue.go): a dim strip directly above
// the input, so the popup reads as part of the input area rather than as new
// content in the conversation.
var (
	fileCompleteItemStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("250"))

	fileCompleteSelectedStyle = lipgloss.NewStyle().
					Background(lipgloss.Color("240")).
					Foreground(lipgloss.Color("255")).
					Bold(true)

	fileCompleteHintStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("243")).
				Italic(true)
)

// skippedDirs are never walked. Directories starting with "." are skipped
// too (see scanProjectFiles), so this list only needs the ones that do not.
var skippedDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"target":       true,
	"__pycache__":  true,
}

// tuiFileIndexMsg carries a finished file scan back to the UI thread.
type tuiFileIndexMsg struct {
	files []string
	// truncated reports that the walk hit fileIndexMaxEntries, so the popup
	// can say the list is partial instead of quietly missing paths.
	truncated bool
}

// scanProjectFilesCmd walks the working directory off the UI thread.
func scanProjectFilesCmd() tea.Cmd {
	return func() tea.Msg {
		files, truncated := scanProjectFiles(".")
		return tuiFileIndexMsg{files: files, truncated: truncated}
	}
}

func scanProjectFiles(root string) ([]string, bool) {
	var files []string
	truncated := false

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is not worth failing the whole scan
			// over — the person just will not see completions from it.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return nil
		}
		name := d.Name()

		if d.IsDir() {
			if strings.HasPrefix(name, ".") || skippedDirs[name] {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if len(files) >= fileIndexMaxEntries {
			truncated = true
			return fs.SkipAll
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})

	return files, truncated
}

// ---------------------------------------------------------------------------
// Detecting the token being typed
// ---------------------------------------------------------------------------

// fileCompleteToken finds the "@..." the person is currently typing and
// returns the byte offset of its "@" plus the query between it and the cursor.
//
// Everything is judged against the CURSOR, not the end of the input. An
// earlier version looked at the end, which meant typing "@tui" in the middle
// of an already-written sentence offered nothing at all — the popup only ever
// appeared while the path happened to be the last thing on the line. What
// makes a token "the one being typed" is that the cursor sits in it.
//
// Two guards remain. The "@" must start a word, so an e-mail address in a
// sentence does not open a file list; and no whitespace may fall between the
// "@" and the cursor, so the popup closes once the path is finished and the
// person types a space.
func fileCompleteToken(value string, cursor int) (at int, query string, ok bool) {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(value) {
		cursor = len(value)
	}
	head := value[:cursor]

	idx := strings.LastIndexByte(head, '@')
	if idx < 0 {
		return 0, "", false
	}
	// Must start a word: beginning of input, or preceded by whitespace.
	if idx > 0 {
		prev, _ := utf8.DecodeLastRuneInString(head[:idx])
		if !unicode.IsSpace(prev) {
			return 0, "", false
		}
	}
	rest := head[idx+1:]
	if strings.ContainsAny(rest, " \t\n") {
		return 0, "", false
	}
	return idx, rest, true
}

// inputCursorOffset is the cursor's byte offset within m.input.Value().
//
// The textarea exposes the cursor as a row plus a rune column (Line() and
// LineInfo(), whose StartColumn+ColumnOffset add back up to that column), and
// Value() joins the rows with "\n" — so the offset is the length of every
// earlier row, its newline, and the runes before the cursor on this one.
func (m TuiModel) inputCursorOffset() int {
	value := m.input.Value()
	lines := strings.Split(value, "\n")
	row := m.input.Line()
	if row < 0 || row >= len(lines) {
		return len(value)
	}

	li := m.input.LineInfo()
	col := li.StartColumn + li.ColumnOffset

	off := 0
	for i := 0; i < row; i++ {
		off += len(lines[i]) + 1 // +1 for the newline Value() puts back
	}
	runes := []rune(lines[row])
	if col > len(runes) {
		col = len(runes)
	}
	if col < 0 {
		col = 0
	}
	return off + len(string(runes[:col]))
}

// setInputValueWithCursor replaces the input and leaves the cursor at a byte
// offset inside it.
//
// SetValue always parks the cursor at the very end, and the textarea has no
// absolute cursor setter — SetCursor is a column within the current row. So
// the cursor is walked back one character at a time, which is the one thing
// that behaves correctly across row boundaries and soft wraps alike. The
// distances involved are a line of typing, not a file.
func (m *TuiModel) setInputValueWithCursor(value string, cursor int) {
	m.input.SetValue(value)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(value) {
		cursor = len(value)
	}
	for range []rune(value[cursor:]) {
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyLeft})
	}
}

// ---------------------------------------------------------------------------
// Ranking
// ---------------------------------------------------------------------------

// completeCandidate is one entry offered by the combined "@" popup, before
// ranking. kind is "file" or "agent"; desc is the agent's frontmatter
// description (empty for a file). key is what the query is matched against —
// the file's path, or the agent's bare name.
type completeCandidate struct {
	key  string
	kind string
	desc string
}

const (
	completeKindFile  = "file"
	completeKindAgent = "agent"
)

// rankFileMatches filters and orders file candidates for a query. Kept as its
// own entry point (rather than folded into rankCandidates below) because it
// is the direct, hermetic thing to unit-test on its own, and because
// refreshFileComplete needs file-only ranking on the (common) turn where no
// agents exist to blend in.
//
// The ordering is what makes this usable with a short query: a match on the
// file's own name beats a match somewhere in its directory path, and a
// prefix beats a match in the middle. Typing "tui" should offer tui.go
// before display/tui_internal/helpers/other.go.
func rankFileMatches(files []string, query string) []string {
	cands := make([]completeCandidate, len(files))
	for i, f := range files {
		cands[i] = completeCandidate{key: f, kind: completeKindFile}
	}
	ranked := rankCandidates(cands, query)
	out := make([]string, len(ranked))
	for i, c := range ranked {
		out[i] = c.key
	}
	return out
}

// rankCandidates is the shared scoring engine behind rankFileMatches and the
// combined file+agent popup: matching a candidate's own name outranks
// matching somewhere earlier in a "/"-separated key (a file's directory; an
// agent's key never contains "/", so it always matches on its own name), and
// a prefix outranks a match in the middle.
func rankCandidates(cands []completeCandidate, query string) []completeCandidate {
	if query == "" {
		n := fileCompleteMaxVisible
		if n > len(cands) {
			n = len(cands)
		}
		out := make([]completeCandidate, n)
		copy(out, cands[:n])
		return out
	}

	q := strings.ToLower(query)

	type scored struct {
		cand  completeCandidate
		score int
	}
	var matches []scored

	for _, c := range cands {
		lower := strings.ToLower(c.key)
		base := lower
		if i := strings.LastIndexByte(lower, '/'); i >= 0 {
			base = lower[i+1:]
		}

		switch {
		case base == q:
			matches = append(matches, scored{c, 0})
		case strings.HasPrefix(base, q):
			matches = append(matches, scored{c, 1})
		case strings.Contains(base, q):
			matches = append(matches, scored{c, 2})
		case strings.HasPrefix(lower, q):
			matches = append(matches, scored{c, 3})
		case strings.Contains(lower, q):
			matches = append(matches, scored{c, 4})
		}
	}

	// Ties broken by key length then alphabetically: the shorter key is
	// nearly always the one meant, and a stable order keeps the highlighted
	// entry from jumping around between keystrokes.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score < matches[j].score
		}
		if len(matches[i].cand.key) != len(matches[j].cand.key) {
			return len(matches[i].cand.key) < len(matches[j].cand.key)
		}
		return matches[i].cand.key < matches[j].cand.key
	})

	out := make([]completeCandidate, 0, fileCompleteMaxVisible)
	for i := 0; i < len(matches) && i < fileCompleteMaxVisible; i++ {
		out = append(out, matches[i].cand)
	}
	return out
}

// rankCompleteCandidates ranks files and agents together for the combined
// popup. Agents are listed first so that on a bare "@" — before anything is
// typed — the (usually short and worth advertising) list of named agents
// shows above the file list rather than being crowded out of the
// fileCompleteMaxVisible cap by it.
func rankCompleteCandidates(files []string, agents []agentdefs.Def, query string) []completeCandidate {
	cands := make([]completeCandidate, 0, len(agents)+len(files))
	for _, a := range agents {
		cands = append(cands, completeCandidate{key: a.Name, kind: completeKindAgent, desc: a.Description})
	}
	for _, f := range files {
		cands = append(cands, completeCandidate{key: f, kind: completeKindFile})
	}
	return rankCandidates(cands, query)
}

// splitCandidates unzips ranked candidates into the three parallel slices
// TuiModel stores them as.
func splitCandidates(cands []completeCandidate) (items, kinds, descs []string) {
	items = make([]string, len(cands))
	kinds = make([]string, len(cands))
	descs = make([]string, len(cands))
	for i, c := range cands {
		items[i] = c.key
		kinds[i] = c.kind
		descs[i] = c.desc
	}
	return items, kinds, descs
}

// ---------------------------------------------------------------------------
// Model state transitions
// ---------------------------------------------------------------------------

// refreshFileComplete re-evaluates the popup against the current input. Run
// after every keystroke that reaches the textarea, so opening, filtering and
// closing all follow from what is actually typed rather than from remembering
// which key did what.
//
// The returned command is non-nil when a file scan needs to start.
func (m *TuiModel) refreshFileComplete() tea.Cmd {
	cursor := m.inputCursorOffset()
	at, query, ok := fileCompleteToken(m.input.Value(), cursor)
	if !ok {
		m.fileCompleteActive = false
		return nil
	}

	m.fileCompleteActive = true
	m.fileCompleteAt = at
	m.fileCompleteEnd = cursor

	// Keep the highlighted entry where it was while the query only grows and
	// the same file is still top of the list; reset it when the candidate set
	// changes shape, so the highlight never points past the end.
	prev := ""
	if m.fileCompleteCursor < len(m.fileCompleteItems) {
		prev = m.fileCompleteItems[m.fileCompleteCursor]
	}

	agents := agentdefs.List(m.cwd)
	ranked := rankCompleteCandidates(m.fileIndex, agents, query)
	m.fileCompleteItems, m.fileCompleteKinds, m.fileCompleteDescs = splitCandidates(ranked)
	m.fileCompleteCursor = 0
	for i, item := range m.fileCompleteItems {
		if item == prev {
			m.fileCompleteCursor = i
			break
		}
	}

	if m.fileIndexPending {
		return nil
	}
	if m.fileIndex == nil || time.Since(m.fileIndexBuiltAt) > fileIndexTTL {
		m.fileIndexPending = true
		return scanProjectFilesCmd()
	}
	return nil
}

// applyFileIndex stores a finished scan and re-filters, so the popup fills in
// as soon as the walk lands rather than after the next keystroke.
func (m *TuiModel) applyFileIndex(msg tuiFileIndexMsg) {
	m.fileIndex = msg.files
	m.fileIndexTruncated = msg.truncated
	m.fileIndexBuiltAt = time.Now()
	m.fileIndexPending = false

	if m.fileCompleteActive {
		if _, query, ok := fileCompleteToken(m.input.Value(), m.inputCursorOffset()); ok {
			agents := agentdefs.List(m.cwd)
			ranked := rankCompleteCandidates(m.fileIndex, agents, query)
			m.fileCompleteItems, m.fileCompleteKinds, m.fileCompleteDescs = splitCandidates(ranked)
			if m.fileCompleteCursor >= len(m.fileCompleteItems) {
				m.fileCompleteCursor = 0
			}
		}
	}
}

// itemKind returns the kind ("file" or "agent") of the i-th candidate.
// Missing/short kinds (as in a test that sets fileCompleteItems directly, or
// any candidate ranked before this field existed) default to "file", the
// prior universal behavior.
func (m TuiModel) itemKind(i int) string {
	if i >= 0 && i < len(m.fileCompleteKinds) {
		return m.fileCompleteKinds[i]
	}
	return completeKindFile
}

// itemDesc returns the agent description of the i-th candidate, or "" for a
// file or an out-of-range index.
func (m TuiModel) itemDesc(i int) string {
	if i >= 0 && i < len(m.fileCompleteDescs) {
		return m.fileCompleteDescs[i]
	}
	return ""
}

// acceptFileComplete replaces the "@query" token with the highlighted
// candidate, tagged with which kind of thing it is: "@file:<path>" for a
// file, "@agent:<name>" for a named agent. The "@" goes with it: it was a way
// to ask for the list, not part of what the person meant to send.
//
// The tag exists so the two are told apart downstream without guessing: an
// "@file:" mention is just a precise path for the model to read on its own,
// but an "@agent:" mention is an instruction — conductor.Submit (see
// extractAgentMentions) reads it back out of the submitted line and tells
// the model to continue that part of the request in a subagent using that
// named agent.
//
// Only the token itself is replaced — whatever was typed after it stays put,
// and the cursor lands right after the inserted text, so completing a name
// in the middle of a sentence leaves the rest of that sentence alone.
func (m *TuiModel) acceptFileComplete() {
	if !m.fileCompleteActive || len(m.fileCompleteItems) == 0 {
		return
	}
	pick := m.fileCompleteItems[m.fileCompleteCursor]
	kind := m.itemKind(m.fileCompleteCursor)
	value := m.input.Value()
	if m.fileCompleteAt > len(value) || m.fileCompleteEnd > len(value) || m.fileCompleteEnd < m.fileCompleteAt {
		m.closeFileComplete()
		return
	}

	head := value[:m.fileCompleteAt]
	tail := value[m.fileCompleteEnd:]

	tag := "@file:"
	if kind == completeKindAgent {
		tag = "@agent:"
	}

	// A trailing space separates the tagged token from what comes next, but
	// only when there is nothing there already: inserting one before
	// existing text would leave a double space every time.
	insert := tag + pick
	if tail == "" || !strings.ContainsRune(" \t\n", rune(tail[0])) {
		insert += " "
	}

	m.setInputValueWithCursor(head+insert+tail, len(head)+len(insert))
	m.closeFileComplete()
}

func (m *TuiModel) moveFileCompleteCursor(delta int) {
	if len(m.fileCompleteItems) == 0 {
		return
	}
	m.fileCompleteCursor += delta
	if m.fileCompleteCursor < 0 {
		m.fileCompleteCursor = len(m.fileCompleteItems) - 1
	}
	if m.fileCompleteCursor >= len(m.fileCompleteItems) {
		m.fileCompleteCursor = 0
	}
}

func (m *TuiModel) closeFileComplete() {
	m.fileCompleteActive = false
	m.fileCompleteItems = nil
	m.fileCompleteKinds = nil
	m.fileCompleteDescs = nil
	m.fileCompleteCursor = 0
}

// handleFileCompleteKey intercepts the keys the popup owns while it is open.
//
// It has to run before the global key handler, which binds Up and Down to
// history navigation: while a list of candidates is on screen those arrows
// obviously mean "move through the list".
func (m *TuiModel) handleFileCompleteKey(msg tea.KeyMsg) (handled bool) {
	if !m.fileCompleteActive {
		return false
	}
	switch msg.Type {
	case tea.KeyUp:
		m.moveFileCompleteCursor(-1)
		return true
	case tea.KeyDown:
		m.moveFileCompleteCursor(1)
		return true
	case tea.KeyTab:
		m.acceptFileComplete()
		return true
	case tea.KeyEnter:
		// Only when there is something to accept; otherwise the popup is
		// just an empty hint and Enter should send the line.
		if len(m.fileCompleteItems) > 0 && !msg.Alt {
			m.acceptFileComplete()
			return true
		}
		return false
	case tea.KeyEscape:
		m.closeFileComplete()
		return true
	}
	return false
}

// renderFileComplete draws the candidate list above the input. Returns "" when
// inactive, so the layout is unchanged for anyone who never types "@".
func (m TuiModel) renderFileComplete(width int) string {
	if !m.fileCompleteActive {
		return ""
	}

	var b strings.Builder

	if len(m.fileCompleteItems) == 0 {
		if m.fileIndexPending {
			b.WriteString(fileCompleteHintStyle.Render("  scanning files…"))
		} else {
			b.WriteString(fileCompleteHintStyle.Render("  no matching file or agent"))
		}
		b.WriteString("\n")
		return b.String()
	}

	for i, item := range m.fileCompleteItems {
		isAgent := m.itemKind(i) == completeKindAgent

		var raw string
		if isAgent {
			raw = "  @agent:" + item
			if desc := m.itemDesc(i); desc != "" {
				raw += "  " + desc
			}
		} else {
			raw = "  " + item
		}

		// Agent lines are truncated from the right: the name at the start is
		// what is being matched and must stay visible, unlike a file path,
		// where the matched filename sits at the end.
		var line string
		if isAgent {
			line = truncateForWidthRight(raw, width)
		} else {
			line = truncateForWidth(raw, width)
		}
		if i == m.fileCompleteCursor {
			b.WriteString(fileCompleteSelectedStyle.Render(line))
		} else {
			b.WriteString(fileCompleteItemStyle.Render(line))
		}
		b.WriteString("\n")
	}
	if m.fileIndexTruncated {
		b.WriteString(fileCompleteHintStyle.Render("  (file list is partial: this project has a lot of files)"))
		b.WriteString("\n")
	}
	return b.String()
}

// truncateForWidth keeps a candidate line inside the terminal, trimming from
// the LEFT so the filename — the part being matched — stays visible.
func truncateForWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[len(r)-width:])
	}
	return "…" + string(r[len(r)-(width-1):])
}

// truncateForWidthRight keeps a candidate line inside the terminal, trimming
// from the RIGHT so the agent name at the start — the part being matched —
// stays visible; whatever trailing description does not fit is what gets
// dropped, not the name.
func truncateForWidthRight(s string, width int) string {
	if width <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:width])
	}
	return string(r[:width-1]) + "…"
}

// fileCompleteHeight is how many terminal rows the popup occupies. The frame
// subtracts it from the message region the same way it does for the queue and
// jobs panels — without that, adding rows above the input pushes the whole
// transcript up by that many lines on every keystroke.
func (m TuiModel) fileCompleteHeight() int {
	if !m.fileCompleteActive {
		return 0
	}
	if len(m.fileCompleteItems) == 0 {
		return 1 // the "scanning…" / "no matching file" line
	}
	h := len(m.fileCompleteItems)
	if m.fileIndexTruncated {
		h++
	}
	return h
}
