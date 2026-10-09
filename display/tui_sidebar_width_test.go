package display

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// useTempHome points $HOME at a fresh temporary directory, so the state file
// is never written to the developer's real home directory.
func useTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// sidebarWidthModel returns a sidebar test model at the given terminal width,
// with the sidebar open and the input focused.
func sidebarWidthModel(width int) TuiModel {
	m := newTestModelForSidebar()
	m.width = width
	m.openSidebar(sidebarTabTokens)
	return m
}

// pressKey sends one key message through Update and returns the new model.
func pressKey(t *testing.T, m TuiModel, msg tea.Msg) TuiModel {
	t.Helper()
	model, _ := m.Update(msg)
	return model.(TuiModel)
}

func TestSidebarWidth_ShiftKeysChangeOneColumn(t *testing.T) {
	useTempHome(t)
	m := sidebarWidthModel(120)
	base := m.sidebarColumnWidth()

	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	if got := m.sidebarColumnWidth(); got != base+1 {
		t.Fatalf("Shift+Left: want %d columns, got %d", base+1, got)
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight})
	if got := m.sidebarColumnWidth(); got != base-1 {
		t.Fatalf("Shift+Right twice: want %d columns, got %d", base-1, got)
	}
}

func TestSidebarWidth_FocusedFallbackKeys(t *testing.T) {
	useTempHome(t)
	m := sidebarWidthModel(120)
	m.sidebarFocused = true
	base := m.sidebarColumnWidth()

	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("<")})
	if got := m.sidebarColumnWidth(); got != base+1 {
		t.Fatalf("<: want %d columns, got %d", base+1, got)
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(">")})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(">")})
	if got := m.sidebarColumnWidth(); got != base-1 {
		t.Fatalf(">>: want %d columns, got %d", base-1, got)
	}
}

func TestSidebarWidth_InputFocusedDoesNotChangeOnPlainKeys(t *testing.T) {
	useTempHome(t)
	m := sidebarWidthModel(120)
	base := m.sidebarColumnWidth()

	// With the input focused, "<" and ">" are typed text, not resize keys.
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("<")})
	if got := m.sidebarColumnWidth(); got != base {
		t.Fatalf("< with input focused changed the width: want %d, got %d", base, got)
	}
}

func TestSidebarWidth_ShiftKeysIgnoredWhenSidebarClosed(t *testing.T) {
	useTempHome(t)
	m := newTestModelForSidebar()
	m.width = 120
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	if m.sidebarWidthPercent != defaultSidebarWidthPercent {
		t.Fatalf("closed sidebar: percent changed to %v", m.sidebarWidthPercent)
	}
	if m.sidebarWidthSeq != 0 {
		t.Fatalf("closed sidebar: a save was scheduled (seq %d)", m.sidebarWidthSeq)
	}
}

func TestSidebarWidth_LimitsHold(t *testing.T) {
	useTempHome(t)
	m := sidebarWidthModel(100)

	for i := 0; i < 200; i++ {
		m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight})
	}
	if got := m.sidebarColumnWidth(); got != sidebarMinPanel+1 {
		t.Fatalf("narrowest: want %d columns (sidebar min %d), got %d", sidebarMinPanel+1, sidebarMinPanel, got)
	}
	for i := 0; i < 200; i++ {
		m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	}
	if got := m.width - m.sidebarColumnWidth(); got != chatMinColumns {
		t.Fatalf("widest: want chat %d columns, got %d", chatMinColumns, got)
	}
}

func TestSidebarWidth_LimitsHoldOnNarrowTerminal(t *testing.T) {
	useTempHome(t)
	// At 60 columns the chat minimum and the sidebar minimum cannot both
	// hold. The sidebar minimum wins, as it did before this change, and the
	// keys do nothing.
	m := sidebarWidthModel(60)
	base := m.sidebarColumnWidth()
	if base != sidebarMinPanel+1 {
		t.Fatalf("narrow terminal: want %d columns, got %d", sidebarMinPanel+1, base)
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight})
	if got := m.sidebarColumnWidth(); got != base {
		t.Fatalf("narrow terminal: keys changed the width to %d", got)
	}
	if m.sidebarWidthSeq != 0 {
		t.Fatalf("narrow terminal: a press with no change was saved")
	}
}

func TestSidebarWidth_PercentRoundTripSameColumns(t *testing.T) {
	useTempHome(t)
	for width := 80; width <= 250; width += 7 {
		m := sidebarWidthModel(width)
		// Every column count that the limits allow at this width. The percent
		// is what a key press at that width would save.
		for panel := sidebarMinPanel; panel <= width-1-chatMinColumns; panel++ {
			m.sidebarWidthPercent = roundPercent(float64(panel) * 100 / float64(width))
			want := panel + 1
			if err := saveSidebarWidthPercent(m.sidebarWidthPercent); err != nil {
				t.Fatalf("width %d: save: %v", width, err)
			}
			reloaded := m
			reloaded.sidebarWidthPercent = loadSidebarWidthPercent()
			if got := reloaded.sidebarColumnWidth(); got != want {
				t.Fatalf("width %d: saved %v gives %d columns, want %d", width, m.sidebarWidthPercent, got, want)
			}
		}
	}
}

func TestSidebarWidth_PercentKeepsThreeDecimals(t *testing.T) {
	useTempHome(t)
	m := sidebarWidthModel(137)
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	if err := saveSidebarWidthPercent(m.sidebarWidthPercent); err != nil {
		t.Fatalf("save: %v", err)
	}
	path, err := tuiStatePath()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var st tuiState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("file is not JSON: %v", err)
	}
	if got := roundPercent(st.SidebarWidthPercent); got != st.SidebarWidthPercent {
		t.Fatalf("saved percent %v has more than three decimals", st.SidebarWidthPercent)
	}
}

func TestSidebarWidth_ScalesOnResize(t *testing.T) {
	useTempHome(t)
	m := sidebarWidthModel(120)
	m.sidebarWidthPercent = 36.432
	// round(120 * 36.432 / 100) = round(43.718) = 44 panel columns, +1 border.
	if got := m.sidebarColumnWidth(); got != 45 {
		t.Fatalf("width 120: want 45 columns, got %d", got)
	}
	model, _ := m.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	m = model.(TuiModel)
	// round(150 * 36.432 / 100) = round(54.648) = 55 panel columns, +1 border.
	if got := m.sidebarColumnWidth(); got != 56 {
		t.Fatalf("width 150: want 56 columns, got %d", got)
	}
}

func TestSidebarWidth_DefaultsWithoutValidFile(t *testing.T) {
	cases := map[string]string{
		"missing":      "",
		"broken JSON":  `{"sidebar_width_percent": `,
		"no key":       `{}`,
		"out of range": `{"sidebar_width_percent": 150}`,
		"zero":         `{"sidebar_width_percent": 0}`,
		"wrong type":   `{"sidebar_width_percent": "wide"}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			home := useTempHome(t)
			if content != "" {
				dir := filepath.Join(home, ".tyci")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, tuiStateFile), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if got := loadSidebarWidthPercent(); got != defaultSidebarWidthPercent {
				t.Fatalf("want default %v, got %v", defaultSidebarWidthPercent, got)
			}
		})
	}
}

func TestSidebarWidth_LoadsSavedValue(t *testing.T) {
	useTempHome(t)
	if err := saveSidebarWidthPercent(36.432); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := loadSidebarWidthPercent(); got != 36.432 {
		t.Fatalf("want 36.432, got %v", got)
	}
}

func TestSidebarWidth_AtomicWriteRenamesTempFile(t *testing.T) {
	home := useTempHome(t)
	dir := filepath.Join(home, ".tyci")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"sidebar_width_percent": 41}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, tuiStateFile), old, 0600); err != nil {
		t.Fatal(err)
	}

	if err := saveSidebarWidthPercent(36.432); err != nil {
		t.Fatalf("save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != tuiStateFile {
		t.Fatalf("want only %s in %s after the write, got %v", tuiStateFile, dir, names)
	}
	data, err := os.ReadFile(filepath.Join(dir, tuiStateFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"sidebar_width_percent":36.432}` + "\n"; string(data) != want {
		t.Fatalf("file content: want %q, got %q", want, string(data))
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("config.json must not be written for the sidebar width")
	}
}

func TestSidebarWidth_DebounceSavesOnlyNewestPress(t *testing.T) {
	home := useTempHome(t)
	m := sidebarWidthModel(120)
	for i := 0; i < 5; i++ {
		m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	}
	if m.sidebarWidthSeq != 5 {
		t.Fatalf("want 5 presses, got seq %d", m.sidebarWidthSeq)
	}

	// The four earlier timers fire first. None of them may write.
	for seq := 1; seq <= 4; seq++ {
		_, cmd := m.Update(sidebarWidthSaveMsg{seq: seq})
		if cmd != nil {
			t.Fatalf("stale timer %d returned a save command", seq)
		}
	}
	path := filepath.Join(home, ".tyci", tuiStateFile)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file written before the newest timer fired")
	}

	// The newest timer writes once, with the final width.
	_, cmd := m.Update(sidebarWidthSaveMsg{seq: 5})
	if cmd == nil {
		t.Fatalf("newest timer returned no save command")
	}
	cmd()
	if got := loadSidebarWidthPercent(); got != m.sidebarWidthPercent {
		t.Fatalf("saved %v, want %v", got, m.sidebarWidthPercent)
	}
}

func TestSidebarWidth_FooterNamesBothKeysFirst(t *testing.T) {
	m := sidebarWidthModel(120)
	m.sidebarFocused = true
	for tab := 0; tab < sidebarTabCount; tab++ {
		m.sidebarTab = tab
		line := m.sidebarFooter()
		if !strings.HasPrefix(line, "Shift+←/→ or </>: width") {
			t.Fatalf("tab %d: key line %q does not start with the width keys", tab, line)
		}
	}
}

func TestSidebarWidth_FocusedShiftKeysChangeOneColumnEach(t *testing.T) {
	useTempHome(t)
	m := sidebarWidthModel(120)
	m.sidebarFocused = true
	base := m.sidebarColumnWidth()

	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft})
	if got := m.sidebarColumnWidth(); got != base+1 {
		t.Fatalf("focused Shift+Left: want %d columns, got %d", base+1, got)
	}
	if !m.sidebarFocused {
		t.Fatalf("focused Shift+Left: focus left the sidebar")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight})
	if got := m.sidebarColumnWidth(); got != base {
		t.Fatalf("focused Shift+Right: want %d columns, got %d", base, got)
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight})
	if got := m.sidebarColumnWidth(); got != base-1 {
		t.Fatalf("focused Shift+Right again: want %d columns, got %d", base-1, got)
	}
	if !m.sidebarFocused {
		t.Fatalf("focused Shift+Right: focus left the sidebar")
	}
}

func TestSidebarWidth_KeyPressSchedulesSaveOfNewestPress(t *testing.T) {
	home := useTempHome(t)
	m := sidebarWidthModel(120)

	model, cmd := m.resizeSidebar(1)
	m = model
	if cmd == nil {
		t.Fatalf("a key press returned no timer command, so it would never save")
	}
	// Running the tick cmd gives the save message of this press.
	msg, ok := cmd().(sidebarWidthSaveMsg)
	if !ok {
		t.Fatalf("timer command did not produce a sidebarWidthSaveMsg")
	}
	if msg.seq != m.sidebarWidthSeq {
		t.Fatalf("timer message seq %d, want newest seq %d", msg.seq, m.sidebarWidthSeq)
	}

	// A second press makes the first timer stale.
	model, cmd2 := m.resizeSidebar(1)
	m = model
	if cmd2 == nil {
		t.Fatalf("second key press returned no timer command")
	}
	if _, saveCmd := m.Update(msg); saveCmd != nil {
		t.Fatalf("stale timer must not save")
	}

	// The newest timer saves the width of the last press.
	newest, ok := cmd2().(sidebarWidthSaveMsg)
	if !ok {
		t.Fatalf("second timer command did not produce a sidebarWidthSaveMsg")
	}
	_, saveCmd := m.Update(newest)
	if saveCmd == nil {
		t.Fatalf("newest timer returned no save command")
	}
	saveCmd()
	path := filepath.Join(home, ".tyci", tuiStateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	var st tuiState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("state file is not JSON: %v", err)
	}
	if st.SidebarWidthPercent != m.sidebarWidthPercent {
		t.Fatalf("saved %v, want %v", st.SidebarWidthPercent, m.sidebarWidthPercent)
	}
}

func TestSidebarWidth_PressAtLimitSchedulesNothing(t *testing.T) {
	useTempHome(t)
	m := sidebarWidthModel(100)
	for m.sidebarColumnWidth() > sidebarMinPanel+1 {
		model, _ := m.resizeSidebar(-1)
		m = model
	}
	seq := m.sidebarWidthSeq
	percent := m.sidebarWidthPercent

	model, cmd := m.resizeSidebar(-1)
	if cmd != nil {
		t.Fatalf("press at the sidebar minimum returned a command")
	}
	if model.sidebarWidthSeq != seq || model.sidebarWidthPercent != percent {
		t.Fatalf("press at the sidebar minimum changed the state")
	}
}
