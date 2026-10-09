package display

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// popupModel is an idle model with the "/" popup open on value, with entries
// as the workflow list it loaded.
func popupModel(t *testing.T, value string, entries []WorkflowEntry) TuiModel {
	t.Helper()
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.reading = true
	m.width = 80
	m.input.SetValue(value)
	m.refreshSlashComplete()
	m.slashEntries = entries
	m.filterSlashItems()
	return m
}

func itemNames(m TuiModel) []string {
	var out []string
	for _, it := range m.slashItems {
		out = append(out, it.name)
	}
	return out
}

func TestSlashPopupOpensOnSlashAndListsBuiltinsAndWorkflows(t *testing.T) {
	m := popupModel(t, "/", []WorkflowEntry{issueToMerge})
	if !m.slashActive {
		t.Fatal("popup closed on \"/\"")
	}
	got := strings.Join(itemNames(m), " ")
	if got != "btw compact exit msg new resume workflow issue-to-merge" {
		t.Fatalf("items %q", got)
	}
}

func TestSlashPopupFiltersWhileTyping(t *testing.T) {
	m := popupModel(t, "/is", []WorkflowEntry{issueToMerge, {Name: "other"}})
	if got := strings.Join(itemNames(m), " "); got != "issue-to-merge" {
		t.Fatalf("items %q", got)
	}
	m.input.SetValue("/e")
	m.filterSlashItems()
	// "exit" and "resume" contain "e"; prefix matches come first.
	if got := strings.Join(itemNames(m), " "); got != "exit new resume issue-to-merge other" {
		t.Fatalf("items %q", got)
	}
}

func TestSlashPopupClosesAtTheFirstSpace(t *testing.T) {
	m := popupModel(t, "/issue-to-merge ", []WorkflowEntry{issueToMerge})
	m.refreshSlashComplete()
	if m.slashActive {
		t.Fatal("popup open after a space")
	}
}

func TestSlashPopupRowsShowParamsAndSource(t *testing.T) {
	m := popupModel(t, "/issue", []WorkflowEntry{issueToMerge})
	m.width = 80
	out := m.renderSlashComplete(m.width)
	if !strings.Contains(out, "/issue-to-merge <issue>   project") {
		t.Fatalf("row missing in %q", out)
	}
	if h := m.slashCompleteHeight(); h != 1 {
		t.Fatalf("height %d", h)
	}
}

func TestSlashPopupShowsWhyAWorkflowDoesNotLoad(t *testing.T) {
	m := popupModel(t, "/bro", []WorkflowEntry{{Name: "broken", Err: "bad JSON"}})
	if out := m.renderSlashComplete(80); !strings.Contains(out, "/broken error: bad JSON") {
		t.Fatalf("reason missing in %q", out)
	}
}

func TestSlashPopupUpDownWrapAround(t *testing.T) {
	m := popupModel(t, "/", nil)
	n := len(m.slashItems)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(TuiModel)
	if m.slashCursor != n-1 {
		t.Fatalf("Up from the first row: cursor %d, want %d", m.slashCursor, n-1)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(TuiModel)
	if m.slashCursor != 0 {
		t.Fatalf("Down from the last row: cursor %d", m.slashCursor)
	}
	if m.input.Value() != "/" {
		t.Fatalf("Up/Down changed the input: %q", m.input.Value())
	}
}

func TestSlashPopupTabAcceptsTheHighlightedCommand(t *testing.T) {
	m := popupModel(t, "/iss", []WorkflowEntry{issueToMerge})
	if !m.handleSlashCompleteKey(tea.KeyMsg{Type: tea.KeyTab}) {
		t.Fatal("Tab not handled")
	}
	if v := m.input.Value(); v != "/issue-to-merge " {
		t.Fatalf("input %q", v)
	}
	if m.slashActive {
		t.Fatal("popup still open after Tab")
	}
}

func TestSlashPopupEnterAcceptsUnlessTheNameIsTyped(t *testing.T) {
	m := popupModel(t, "/iss", []WorkflowEntry{issueToMerge})
	if !m.handleSlashCompleteKey(tea.KeyMsg{Type: tea.KeyEnter}) {
		t.Fatal("Enter not handled while a partial name is typed")
	}
	if v := m.input.Value(); v != "/issue-to-merge " {
		t.Fatalf("input %q", v)
	}

	// The typed name is the highlighted command: Enter must submit, not add a space.
	m = popupModel(t, "/new", nil)
	if m.handleSlashCompleteKey(tea.KeyMsg{Type: tea.KeyEnter}) {
		t.Fatal("Enter on an exact name was swallowed by the popup")
	}
	if m.slashActive || m.input.Value() != "/new" {
		t.Fatalf("popup open or input changed: %v %q", m.slashActive, m.input.Value())
	}
}

func TestSlashPopupEscCloses(t *testing.T) {
	m := popupModel(t, "/", nil)
	if !m.handleSlashCompleteKey(tea.KeyMsg{Type: tea.KeyEscape}) {
		t.Fatal("Esc not handled")
	}
	if m.slashActive || m.slashItems != nil {
		t.Fatal("popup still open after Esc")
	}
}

func TestSlashPopupErrorRowDoesNotInsertButExplains(t *testing.T) {
	m := popupModel(t, "/bro", []WorkflowEntry{{Name: "broken", Err: "bad JSON"}})
	m.handleSlashCompleteKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.input.Value() != "/bro" || !strings.Contains(m.statusMessage, "bad JSON") {
		t.Fatalf("input %q, status %q", m.input.Value(), m.statusMessage)
	}
}

func TestSlashPopupReservedWorkflowIsInsertedAsWorkflowCommand(t *testing.T) {
	entry := WorkflowEntry{Name: "btw", Err: "name is reserved"}
	m := popupModel(t, "/bt", []WorkflowEntry{entry})
	// The builtin row comes first; move to the workflow row with Down.
	m.handleSlashCompleteKey(tea.KeyMsg{Type: tea.KeyDown})
	if m.slashItems[m.slashCursor].err == "" {
		t.Fatal("highlight is not on the reserved workflow row")
	}
	m.handleSlashCompleteKey(tea.KeyMsg{Type: tea.KeyTab})
	if v := m.input.Value(); v != "/workflow btw " {
		t.Fatalf("input %q", v)
	}
}

func TestSlashListMessageFillsAnOpenPopup(t *testing.T) {
	m := popupModel(t, "/iss", nil)
	if len(m.slashItems) != 0 {
		t.Fatalf("items before the list arrived: %v", itemNames(m))
	}
	next, _ := m.Update(slashListMsg{entries: []WorkflowEntry{issueToMerge}})
	if got := strings.Join(itemNames(next.(TuiModel)), " "); got != "issue-to-merge" {
		t.Fatalf("items %q", got)
	}
}

func TestSlashPopupOpeningAsksForTheWorkflowList(t *testing.T) {
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.reading = true
	m.workflows = &fakeStarter{entries: []WorkflowEntry{issueToMerge}}
	m.input.SetValue("/")
	cmd := m.refreshSlashComplete()
	if cmd == nil {
		t.Fatal("no command to load the workflow list")
	}
	if msg, ok := cmd().(slashListMsg); !ok || len(msg.entries) != 1 {
		t.Fatalf("message %#v", cmd())
	}
}

func TestSlashPopupEnterRunsTypedNameAfterBackspace(t *testing.T) {
	f := &fakeStarter{entries: []WorkflowEntry{{Name: "review"}, {Name: "review-pr"}}}
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.reading = true
	m.width = 80
	m.workflows = f
	m.input.SetValue("/review-")
	m.refreshSlashComplete()
	m.slashEntries = f.entries
	m.filterSlashItems()
	if got := itemNames(m); len(got) == 0 || got[m.slashCursor] != "review-pr" {
		t.Fatalf("highlight %v on %d", got, m.slashCursor)
	}
	m.input.SetValue("/review")
	m.filterSlashItems()
	if m.slashItems[m.slashCursor].name != "review" {
		t.Fatalf("highlight on %q, want review", m.slashItems[m.slashCursor].name)
	}
	if m.handleSlashCompleteKey(tea.KeyMsg{Type: tea.KeyEnter}) {
		t.Fatal("Enter was taken by the popup instead of running the line")
	}
	handled, cmd := m.startWorkflowFromInput()
	if !handled || cmd == nil {
		t.Fatal("line not started")
	}
	cmd()
	if !reflect.DeepEqual(f.started, []string{"review"}) {
		t.Fatalf("started %v, want review", f.started)
	}
}
