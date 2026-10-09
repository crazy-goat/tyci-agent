package display

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeStarter records the starts. A start with err set fails and starts nothing.
type fakeStarter struct {
	entries  []WorkflowEntry
	err      error
	warnings []string
	started  []string
}

func (f *fakeStarter) List() []WorkflowEntry { return f.entries }

func (f *fakeStarter) Start(name string, params []string) (string, []string, error) {
	if f.err != nil {
		return "", nil, f.err
	}
	f.started = append(f.started, strings.Join(append([]string{name}, params...), " "))
	return "run-1", f.warnings, nil
}

var issueToMerge = WorkflowEntry{
	Name:   "issue-to-merge",
	Params: []WorkflowParam{{Name: "issue", Required: true}},
	Source: "project",
}

func TestWorkflowLineStartsNamedWorkflowWithParams(t *testing.T) {
	list := []WorkflowEntry{issueToMerge}
	name, params, ok := workflowLine("/issue-to-merge 160 extra", list)
	if !ok || name != "issue-to-merge" || !reflect.DeepEqual(params, []string{"160", "extra"}) {
		t.Fatalf("got %q %q %v", name, params, ok)
	}
}

func TestWorkflowLineIgnoresOtherLines(t *testing.T) {
	list := []WorkflowEntry{
		issueToMerge,
		{Name: "broken", Err: "bad JSON"},
	}
	for _, line := range []string{
		"hello", "/", "/unknown 1", // not a workflow
		"/btw why", "/new", "/msg job text", "/compact", // builtins win
		"/resume --all",
	} {
		if _, _, ok := workflowLine(line, list); ok {
			t.Errorf("%q must not start a workflow", line)
		}
	}
}

func TestWorkflowCommandReachesReservedNames(t *testing.T) {
	cases := []struct {
		line   string
		name   string
		params []string
	}{
		{"/workflow btw 1", "btw", []string{"1"}},
		{"/workflow issue-to-merge 160", "issue-to-merge", []string{"160"}},
		{"/workflow", "", nil},
	}
	for _, c := range cases {
		name, params, ok := workflowLine(c.line, nil)
		if !ok || name != c.name || len(params) != len(c.params) {
			t.Errorf("%q: got %q %q %v", c.line, name, params, ok)
		}
	}
}

func TestIsReservedWorkflowName(t *testing.T) {
	for _, n := range []string{"btw", "compact", "exit", "msg", "new", "resume", "workflow"} {
		if !IsReservedWorkflowName(n) {
			t.Errorf("%q must be reserved", n)
		}
	}
	if IsReservedWorkflowName("issue-to-merge") {
		t.Error("issue-to-merge must not be reserved")
	}
}

func TestStartWorkflowStartsWithParams(t *testing.T) {
	f := &fakeStarter{warnings: []string{"check skipped"}}
	msg := startWorkflow(f, "issue-to-merge", []string{"160"})
	if msg.err != nil || msg.modelText != "" {
		t.Fatalf("unexpected result %+v", msg)
	}
	if !reflect.DeepEqual(f.started, []string{"issue-to-merge 160"}) {
		t.Fatalf("started %v", f.started)
	}
	if msg.notice != "started /issue-to-merge 160\nwarning: check skipped" {
		t.Fatalf("notice %q", msg.notice)
	}
}

func TestStartWorkflowMissingParamStartsNothingAndAsksTheModel(t *testing.T) {
	f := &fakeStarter{err: WorkflowParamError{Name: "issue", Description: "Issue number"}}
	msg := startWorkflow(f, "issue-to-merge", nil)
	want := "user wants /issue-to-merge, missing: issue (Issue number). Ask the user for it, then call workflow_start."
	if msg.modelText != want || msg.err != nil || msg.notice != "" {
		t.Fatalf("got %+v", msg)
	}
	if len(f.started) != 0 {
		t.Fatalf("a run started: %v", f.started)
	}
}

func TestStartWorkflowErrorIsReturnedNotSentToTheModel(t *testing.T) {
	f := &fakeStarter{err: errors.New("workflow \"x\" not found")}
	msg := startWorkflow(f, "x", []string{"1", "2"})
	if msg.err == nil || msg.modelText != "" {
		t.Fatalf("got %+v", msg)
	}
}

func TestStartWorkflowBareWorkflowCommandIsUsageError(t *testing.T) {
	msg := startWorkflow(&fakeStarter{}, "", nil)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "/workflow <name>") {
		t.Fatalf("got %+v", msg)
	}
}

// TestWorkflowStartWhileBusyLeavesTheTurnAlone is the busy-turn rule: Enter on
// "/<name>" starts the workflow in a command, and the pending-message queue and
// the cancel channel of the running turn are not touched.
func TestWorkflowStartWhileBusyLeavesTheTurnAlone(t *testing.T) {
	f := &fakeStarter{entries: []WorkflowEntry{issueToMerge}}
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.reading = false
	m.workflows = f
	m.queue = make(chan string, 4)
	m.commands = make(chan string, 4)
	m.input.SetValue("/issue-to-merge 160")

	next, cmd := m.handleKeyWhileBusy(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no start command")
	}
	if len(f.started) != 0 {
		t.Fatal("the start ran before the command")
	}
	if got := len(next.(TuiModel).queue); got != 0 {
		t.Fatalf("the line was queued for the model (%d)", got)
	}
	if got := len(m.commands); got != 0 {
		t.Fatalf("the line went to the command channel (%d)", got)
	}
	if v := next.(TuiModel).input.Value(); v != "" {
		t.Fatalf("input not cleared: %q", v)
	}

	msg := cmd()
	res, _ := next.(TuiModel).Update(msg)
	if !reflect.DeepEqual(f.started, []string{"issue-to-merge 160"}) {
		t.Fatalf("started %v", f.started)
	}
	if got := len(res.(TuiModel).queue); got != 0 {
		t.Fatalf("notice touched the queue (%d)", got)
	}
}

// TestMissingParamWhileIdleSendsTheTextAsAPrompt checks the missing-param path:
// nothing starts, and the model gets the text as a prompt, with the typed text kept.
func TestMissingParamWhileIdleSendsTheTextAsAPrompt(t *testing.T) {
	results := make(chan string, 2)
	f := &fakeStarter{entries: []WorkflowEntry{issueToMerge}, err: WorkflowParamError{Name: "issue", Description: "Issue number"}}
	m := newModel(results, "test/model", "", nil, 0, 0, 0)
	m.reading = true
	m.workflows = f
	m.input.SetValue("/issue-to-merge")

	started, cmd := m.startWorkflowFromInput()
	if !started || cmd == nil {
		t.Fatal("the line was not taken")
	}
	m.input.SetValue("typing")
	res, _ := m.Update(cmd())
	if len(f.started) != 0 {
		t.Fatalf("a run started: %v", f.started)
	}
	got := <-results
	if !strings.HasPrefix(got, "user wants /issue-to-merge, missing: issue (Issue number).") {
		t.Fatalf("model text %q", got)
	}
	if v := res.(TuiModel).input.Value(); v != "typing" {
		t.Fatalf("typed text lost: %q", v)
	}
}

func TestStartWorkflowFromInputPassesOtherLinesOn(t *testing.T) {
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.workflows = &fakeStarter{}
	m.input.SetValue("/btw why")
	if handled, _ := m.startWorkflowFromInput(); handled {
		t.Fatal("/btw must not start a workflow")
	}
	if m.input.Value() != "/btw why" {
		t.Fatalf("input changed: %q", m.input.Value())
	}
}

// countingStarter counts the List calls.
type countingStarter struct {
	fakeStarter
	lists int
}

func (c *countingStarter) List() []WorkflowEntry {
	c.lists++
	return c.fakeStarter.List()
}

func TestStartWorkflowFromInputListsOnlyForWorkflowNames(t *testing.T) {
	for _, line := range []string{"hello", "/btw why", "/workflow x", "/exit", "/"} {
		c := &countingStarter{}
		m := newModel(nil, "test/model", "", nil, 0, 0, 0)
		m.workflows = c
		m.input.SetValue(line)
		m.startWorkflowFromInput()
		if c.lists != 0 {
			t.Fatalf("%q: List called %d times", line, c.lists)
		}
	}
	c := &countingStarter{}
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.workflows = c
	m.input.SetValue("/issue-to-merge 160")
	m.startWorkflowFromInput()
	if c.lists != 1 {
		t.Fatalf("workflow line: List called %d times, want 1", c.lists)
	}
}

func TestStartWorkflowFromInputRecordsHistory(t *testing.T) {
	f := &fakeStarter{entries: []WorkflowEntry{issueToMerge}}
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.workflows = f
	m.input.SetValue("/issue-to-merge 160")
	if handled, _ := m.startWorkflowFromInput(); !handled {
		t.Fatal("line not taken")
	}
	if len(m.inputHistory) != 1 || m.inputHistory[0] != "/issue-to-merge 160" {
		t.Fatalf("history %q", m.inputHistory)
	}
}

func TestWorkflowStartedReachesTranscriptWhileModalIsOpen(t *testing.T) {
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.btwListActive = true
	res, _ := m.Update(workflowStartedMsg{notice: "started /issue-to-merge 160"})
	tm := res.(TuiModel)
	if len(tm.blocks) == 0 || !strings.Contains(fmt.Sprint(tm.blocks), "started /issue-to-merge 160") {
		t.Fatalf("notice lost while a modal was open: %+v", tm.blocks)
	}
}

func TestMissingParamRecordsOnlyTheTypedLine(t *testing.T) {
	results := make(chan string, 2)
	f := &fakeStarter{entries: []WorkflowEntry{issueToMerge}, err: WorkflowParamError{Name: "issue", Description: "Issue number"}}
	m := newModel(results, "test/model", "", nil, 0, 0, 0)
	m.reading = true
	m.workflows = f
	m.input.SetValue("/issue-to-merge")

	_, cmd := m.startWorkflowFromInput()
	if cmd == nil {
		t.Fatal("no start command")
	}
	res, _ := m.Update(cmd())
	<-results
	got := res.(TuiModel).inputHistory
	if len(got) != 1 || got[0] != "/issue-to-merge" {
		t.Fatalf("history %q, want only the typed line", got)
	}
}

// blockingStarter holds Start until release is closed, like a slow Prepare.
type blockingStarter struct {
	fakeStarter
	release chan struct{}
}

func (b *blockingStarter) Start(name string, params []string) (string, []string, error) {
	<-b.release
	return b.fakeStarter.Start(name, params)
}

func TestStartingNoticeShowsBeforeStartReturns(t *testing.T) {
	f := &blockingStarter{fakeStarter: fakeStarter{entries: []WorkflowEntry{issueToMerge}}, release: make(chan struct{})}
	defer close(f.release)
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.workflows = f
	m.input.SetValue("/issue-to-merge 160")

	handled, cmd := m.startWorkflowFromInput()
	if !handled || cmd == nil {
		t.Fatal("line not taken")
	}
	if !strings.Contains(fmt.Sprint(m.blocks), "starting /issue-to-merge 160...") {
		t.Fatalf("no starting notice before the start returns: %+v", m.blocks)
	}
	if len(f.started) != 0 {
		t.Fatal("the start ran before the command")
	}
	go cmd()
}

func TestStartWorkflowFromInputSkipsListForPastedPath(t *testing.T) {
	c := &countingStarter{}
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.workflows = c
	m.input.SetValue("/Users/x/file.go explain")
	if handled, _ := m.startWorkflowFromInput(); handled {
		t.Fatal("a path must go to the model")
	}
	if c.lists != 0 {
		t.Fatalf("List called %d times for a path", c.lists)
	}
}

func TestBrokenWorkflowIsShownAndNeverSent(t *testing.T) {
	for _, line := range []string{"/broken 160", "/broken"} {
		results := make(chan string, 2)
		m := newModel(results, "test/model", "", nil, 0, 0, 0)
		m.reading = true
		m.workflows = &fakeStarter{entries: []WorkflowEntry{{Name: "broken", Err: "bad JSON"}}}
		m.input.SetValue(line)

		handled, _ := m.startWorkflowFromInput()
		if !handled {
			t.Fatalf("%q: not taken", line)
		}
		if len(results) != 0 || m.queueItems != nil {
			t.Fatalf("%q: sent to the model or queued", line)
		}
		if !hasBlock(m, "error", "/broken: bad JSON") {
			t.Fatalf("%q: no error block: %+v", line, m.blocks)
		}
		if !reflect.DeepEqual(m.inputHistory, []string{line}) {
			t.Fatalf("%q: history %q", line, m.inputHistory)
		}
		if m.input.Value() != "" {
			t.Fatalf("%q: input not cleared", line)
		}
	}
}

func TestBrokenWorkflowExactPopupNameIsShownAndNeverSent(t *testing.T) {
	f := &fakeStarter{entries: []WorkflowEntry{{Name: "broken", Err: "bad JSON"}}}
	m := popupModel(t, "/broken", f.entries)
	m.workflows = f
	if m.handleSlashCompleteKey(tea.KeyMsg{Type: tea.KeyEnter}) {
		t.Fatal("Enter was taken by the popup")
	}
	if handled, _ := m.startWorkflowFromInput(); !handled {
		t.Fatal("broken workflow line went on to the model")
	}
	if !hasBlock(m, "error", "/broken: bad JSON") {
		t.Fatalf("no error block: %+v", m.blocks)
	}
}

func TestBareWorkflowShowsUsageBeforeStarting(t *testing.T) {
	f := &fakeStarter{}
	m := newModel(nil, "test/model", "", nil, 0, 0, 0)
	m.workflows = f
	m.input.SetValue("/workflow")
	if handled, _ := m.startWorkflowFromInput(); !handled {
		t.Fatal("bare /workflow not taken")
	}
	if hasBlock(m, "block", "") || strings.Contains(fmt.Sprint(m.blocks), "starting") {
		t.Fatalf("starting notice shown for a bare /workflow: %+v", m.blocks)
	}
	if !hasBlock(m, "error", workflowUsage) {
		t.Fatalf("no usage error: %+v", m.blocks)
	}
}

func TestWorkflowNameCaseIsTheSameInBothForms(t *testing.T) {
	list := []WorkflowEntry{issueToMerge}
	for _, line := range []string{"/Issue-To-Merge 1", "/workflow Issue-To-Merge 1"} {
		name, params, ok := workflowLine(line, list)
		if !ok || name != "issue-to-merge" || !reflect.DeepEqual(params, []string{"1"}) {
			t.Errorf("%q: got %q %q %v", line, name, params, ok)
		}
	}
}

// hasBlock reports whether the transcript holds a block of kind with content.
// An empty content matches any block of kind.
func hasBlock(m TuiModel, kind, content string) bool {
	for _, b := range m.blocks {
		if b.kind == kind && (content == "" || b.content == content) {
			return true
		}
	}
	return false
}
