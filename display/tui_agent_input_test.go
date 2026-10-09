package display

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/tools"
)

// fakeAgentInput records the calls of the agent view input.
type fakeAgentInput struct {
	posted    []string
	resumed   []string
	checks    int
	tokens    int
	usd       float64
	checkErr  error
	newID     string
	resumeErr error
}

func (f *fakeAgentInput) Post(jobID, text string) error {
	f.posted = append(f.posted, jobID+": "+text)
	return nil
}

func (f *fakeAgentInput) ResumeCheck(jobID string) (int, float64, error) {
	f.checks++
	return f.tokens, f.usd, f.checkErr
}

func (f *fakeAgentInput) Resume(jobID, text string) (string, error) {
	f.resumed = append(f.resumed, jobID+": "+text)
	return f.newID, f.resumeErr
}

// openInputTestView returns a model with the agent view of jobID open, the
// sidebar unfocused, and the fake input connected. status is the job status.
func openInputTestView(t *testing.T, jobID string, status jobs.Status, fake *fakeAgentInput) TuiModel {
	t.Helper()
	m := newAgentViewTestModel(t, jobID)
	m.applyJobUpdate(jobs.Job{ID: jobID, Kind: jobs.KindSubagent, Status: status, Description: "worker task", StartedAt: time.Now()})
	model, _ := m.sidebarActivateRow()
	m2 := model.(TuiModel)
	m2.sidebarFocused = false
	m2.agentInput = fake
	return m2
}

// press sends one key to m and returns the new model and the command it made.
func press(m TuiModel, msg tea.KeyMsg) (TuiModel, tea.Cmd) {
	model, cmd := m.Update(msg)
	return model.(TuiModel), cmd
}

var enterKey = tea.KeyMsg{Type: tea.KeyEnter}

// runCmd runs cmd and feeds its message back into Update, as the program does.
func runCmd(t *testing.T, m TuiModel, cmd tea.Cmd) TuiModel {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command")
	}
	model, _ := m.Update(cmd())
	return model.(TuiModel)
}

func lastViewBlock(m TuiModel) block {
	blocks := m.agentView.model.blocks
	return blocks[len(blocks)-1]
}

func TestAgentInput_RunningAgentGetsTheTextAsMessage(t *testing.T) {
	fake := &fakeAgentInput{}
	m := openInputTestView(t, "agent-run", jobs.StatusRunning, fake)
	m.input.SetValue("  check the tests  ")

	m, cmd := press(m, enterKey)
	m = runCmd(t, m, cmd)

	if len(fake.posted) != 1 || fake.posted[0] != "agent-run: check the tests" {
		t.Fatalf("posted = %q, want the trimmed text to agent-run", fake.posted)
	}
	if m.input.Value() != "" {
		t.Fatalf("input = %q, want it emptied after the send", m.input.Value())
	}
	if fake.checks != 0 || len(fake.resumed) != 0 {
		t.Fatal("a running agent must not be resumed")
	}
	if got := lastViewBlock(m).content; !strings.Contains(got, "message queued for agent-run") {
		t.Fatalf("view notice = %q, want the queued notice", got)
	}
}

func TestAgentInput_FinishedAgentNeedsSecondEnterToResume(t *testing.T) {
	fake := &fakeAgentInput{tokens: 392000, usd: 1.25, newID: "agent-new"}
	m := openInputTestView(t, "agent-old", jobs.StatusDone, fake)
	m.input.SetValue("one more thing")

	m, cmd := press(m, enterKey)
	if cmd != nil || len(fake.resumed) != 0 {
		t.Fatal("the first Enter must not resume the agent")
	}
	if !m.agentView.confirming {
		t.Fatal("the first Enter must ask for a confirmation")
	}
	notice := lastViewBlock(m).content
	if !strings.Contains(notice, "392k tok") || !strings.Contains(notice, "$1.25") {
		t.Fatalf("confirmation notice = %q, want the context size and the cost", notice)
	}
	if m.input.Value() != "one more thing" {
		t.Fatal("the text must stay in the input until the resume is confirmed")
	}

	m, cmd = press(m, enterKey)
	m = runCmd(t, m, cmd)
	if len(fake.resumed) != 1 || fake.resumed[0] != "agent-old: one more thing" {
		t.Fatalf("resumed = %q, want one resume of agent-old with the text", fake.resumed)
	}
	if m.agentView == nil || m.agentView.jobID != "agent-new" {
		t.Fatalf("the view must follow the new job, got %+v", m.agentView)
	}
	if m.resumedFrom["agent-new"] != "agent-old" {
		t.Fatalf("resumedFrom = %v, want agent-new continues agent-old", m.resumedFrom)
	}
	if m.input.Value() != "" {
		t.Fatal("the input must be emptied after the resume")
	}
}

func TestAgentInput_OtherKeyCancelsTheResume(t *testing.T) {
	fake := &fakeAgentInput{newID: "agent-new"}
	m := openInputTestView(t, "agent-old", jobs.StatusDone, fake)
	m.input.SetValue("text")

	m, _ = press(m, enterKey)
	m, cmd := press(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if cmd != nil || m.agentView.confirming {
		t.Fatal("another key must cancel the resume")
	}

	// Esc cancels a pending resume too, and it must not also close the view.
	m, _ = press(m, enterKey)
	m, cmd = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.agentView == nil || m.agentView.confirming {
		t.Fatal("Esc must cancel the resume and keep the view open")
	}
	if cmd != nil || len(fake.resumed) != 0 {
		t.Fatal("a cancelled resume must not run")
	}
	if m.input.Value() != "text" {
		t.Fatal("a cancelled resume must keep the text in the input")
	}
}

func TestAgentInput_RefusesResumeOfAnAgentOfAnActiveRun(t *testing.T) {
	fake := &fakeAgentInput{checkErr: errors.New("the agent belongs to run r1 (running)")}
	m := openInputTestView(t, "agent-busy", jobs.StatusDone, fake)
	m.input.SetValue("text")

	m, cmd := press(m, enterKey)
	if cmd != nil || m.agentView.confirming {
		t.Fatal("a refused agent must not ask for a confirmation")
	}
	got := lastViewBlock(m)
	if got.kind != "error" || !strings.Contains(got.content, "belongs to run r1 (running)") {
		t.Fatalf("refusal = %+v, want an error with the reason", got)
	}
	if len(fake.resumed) != 0 {
		t.Fatal("a refused agent must not be resumed")
	}
}

func TestAgentInput_PlaceholderNamesTheViewedAgent(t *testing.T) {
	m := openInputTestView(t, "20261008-125053", jobs.StatusRunning, &fakeAgentInput{})
	want := "message to 20261008-125053/worker task (Esc: back to main)"
	if got := m.input.Placeholder; got != want {
		t.Fatalf("placeholder = %q, want %q", got, want)
	}

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := model.(TuiModel).input.Placeholder; got != inputPlaceholder {
		t.Fatalf("placeholder after Esc = %q, want the default", got)
	}
}

func TestAgentInput_ResumedViewOpensBeforeItsTranscriptExists(t *testing.T) {
	fake := &fakeAgentInput{newID: "agent-fresh"}
	m := openInputTestView(t, "agent-old2", jobs.StatusDone, fake)
	m.input.SetValue("go on")
	m, _ = press(m, enterKey)
	m, cmd := press(m, enterKey)
	m = runCmd(t, m, cmd)
	if m.agentView == nil || m.agentView.jobID != "agent-fresh" {
		t.Fatalf("the view must follow agent-fresh, got %+v", m.agentView)
	}
	if tools.HasLiveTranscript("agent-fresh") {
		t.Fatal("test setup: agent-fresh must have no transcript yet")
	}

	tools.RecordLiveEvent("agent-fresh", tools.LiveEvent{Kind: "text", Content: "resumed reply"})
	model, _ := m.Update(statusTickMsg{})
	m = model.(TuiModel)
	if got := m.agentView.model.blocks; len(got) == 0 || got[len(got)-1].content != "resumed reply" {
		t.Fatalf("view blocks = %+v, want the resumed reply after the next tick", got)
	}
}

func TestAgentInput_ResumedJobIsListedUnderTheJobItContinues(t *testing.T) {
	m := newTestModelForSidebar()
	m.applyJobUpdate(jobs.Job{ID: "agent-root", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "review", StartedAt: time.Now()})
	m.applyJobUpdate(jobs.Job{ID: "agent-cont", Kind: jobs.KindSubagent, Status: jobs.StatusRunning, Description: "fix the nit", StartedAt: time.Now()})
	m.resumedFrom["agent-cont"] = "agent-root"

	rows := m.buildSubagentTree()
	var cont, root *subagentTreeRow
	for i := range rows {
		switch rows[i].job.ID {
		case "agent-cont":
			cont = &rows[i]
		case "agent-root":
			root = &rows[i]
		}
	}
	if cont == nil || root == nil {
		t.Fatalf("rows = %+v, want both jobs", rows)
	}
	if !cont.continues || cont.depth != root.depth+1 {
		t.Fatalf("continuation row = %+v, want it nested one level under %+v", cont, root)
	}
	if cont.job.Description != "review" {
		t.Fatalf("continuation description = %q, want the name of the job it continues", cont.job.Description)
	}
	if line := m.formatSubagentRow(*cont, 80, 10, 10); !strings.Contains(line, "↻ review") {
		t.Fatalf("row = %q, want the continuation marker and the name", line)
	}
}

func TestAgentInput_TextGoesToTheAgentWhileMainIsBusy(t *testing.T) {
	fake := &fakeAgentInput{}
	m := openInputTestView(t, "agent-busy-main", jobs.StatusRunning, fake)
	m.reading = false
	m.input.SetValue("for the agent")

	m, cmd := press(m, enterKey)
	m = runCmd(t, m, cmd)
	if len(fake.posted) != 1 || fake.posted[0] != "agent-busy-main: for the agent" {
		t.Fatalf("posted = %q, want the text to the viewed agent", fake.posted)
	}
	if len(m.queueItems) != 0 {
		t.Fatalf("queueItems = %q, the text must not join the main queue", m.queueItems)
	}
}
