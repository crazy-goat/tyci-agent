package display

// The status bar while an agent view is open describes the viewed subagent,
// not the main conversation. Before this, the bar showed the main model, the
// main state and the main context figure while a subagent's conversation was
// on screen (#528).

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/crazy-goat/tyci-agent/internal/ledger"
	"github.com/crazy-goat/tyci-agent/internal/pricing"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/crazy-goat/tyci-agent/tools"
)

// openAgentViewStatusModel returns a busy main model with a known model, a
// running subagent and its transcript, and the agent view open on that
// subagent. The main conversation shows "sending request" and a context
// figure, so either of them in the bar means the main status leaked.
// The pricing catalog and the ledger are reset for each test.
func openAgentViewStatusModel(t *testing.T, jobID string, events ...tools.LiveEvent) TuiModel {
	t.Helper()
	dir := t.TempDir()
	writeTestCatalog(t, dir, `{"nexos":{"id":"nexos","models":{
		"deepseek":{"id":"deepseek","name":"DeepSeek","cost":{"input":1,"output":2},"limit":{"context":200000}}
	}}}`)
	t.Setenv("HOME", dir)
	pricing.Reset()
	ledger.Reset()
	t.Cleanup(pricing.Reset)
	t.Cleanup(ledger.Reset)

	m := newTestModelForSidebar()
	m.width = 120
	m.modelName = "main/main-model"
	m.reading = false
	m.status = "sending"
	m.requestStartTime = time.Now()
	m.lastUsage = stream.Usage{Input: 52_000}
	m.applyJobUpdate(jobs.Job{ID: jobID, Kind: jobs.KindSubagent, Status: jobs.StatusRunning, Description: "worker", StartedAt: time.Now()})
	if len(events) == 0 {
		events = []tools.LiveEvent{{Kind: "thinking", Content: "planning"}}
	}
	for _, ev := range events {
		tools.RecordLiveEvent(jobID, ev)
	}
	if !m.openAgentView(jobID, "worker") {
		t.Fatalf("the agent view for %s did not open", jobID)
	}
	return m
}

func TestAgentViewStatus_ShowsTheViewedAgentNotTheMain(t *testing.T) {
	m := openAgentViewStatusModel(t, "sub-528-model")
	ledger.Record(ledger.Subagent, "nexos", "deepseek", "sub-528-model", stream.Usage{Input: 500_000, Output: 100_000})

	got := m.buildStatus()
	for _, leaked := range []string{"main/main-model", "ctx 52k", "sending request"} {
		if strings.Contains(got, leaked) {
			t.Errorf("status %q shows the main conversation (%q) while an agent view is open", got, leaked)
		}
	}
	if !strings.Contains(got, "nexos/deepseek") {
		t.Errorf("status %q does not name the viewed agent's model", got)
	}
	if !strings.Contains(got, "⟳ thinking...") {
		t.Errorf("status %q does not show the viewed agent's live state", got)
	}
}

func TestAgentViewStatus_ShowsTheAgentsFiguresAndTheTotal(t *testing.T) {
	m := openAgentViewStatusModel(t, "sub-528-figures")
	ledger.Record(ledger.Main, "nexos", "deepseek", "", stream.Usage{Input: 1_000_000})
	ledger.Record(ledger.Subagent, "nexos", "deepseek", "sub-528-figures", stream.Usage{Input: 500_000, Output: 100_000})

	got := m.buildStatus()
	// The same figures as the agent's row in the Subagents list: its own
	// tokens and its cost. The session total stays on the right.
	for _, want := range []string{"600k tok, 0.700$", "total 1.70$"} {
		if !strings.Contains(got, want) {
			t.Errorf("status %q does not contain %q", got, want)
		}
	}
}

// A tool that ended must not leave the view on "tool". The replay has no event
// for the next request, so the agent is waiting for its response.
func TestAgentViewStatus_AfterAToolShowsWaitingNotTool(t *testing.T) {
	m := openAgentViewStatusModel(t, "sub-528-tool",
		tools.LiveEvent{Kind: "tool-start", ToolName: "bash"},
		tools.LiveEvent{Kind: "tool-end", Content: "ok"})

	got := m.buildStatus()
	if strings.Contains(got, "tool") {
		t.Errorf("status %q shows a tool that has already ended", got)
	}
	if !strings.Contains(got, "⟳ waiting for response...") {
		t.Errorf("status %q does not show that the agent waits for its next response", got)
	}
}

func TestAgentViewStatus_EndedJobShowsItsFinalState(t *testing.T) {
	m := openAgentViewStatusModel(t, "sub-528-done")
	m.applyJobUpdate(jobs.Job{ID: "sub-528-done", Kind: jobs.KindSubagent, Status: jobs.StatusDone, Description: "worker", StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now()})

	got := m.buildStatus()
	if !strings.Contains(got, "done") {
		t.Errorf("status %q does not show that the agent is done", got)
	}
	if strings.Contains(got, "⟳") {
		t.Errorf("status %q shows a spinner for a finished agent", got)
	}
}

func TestAgentViewStatus_JobWithoutUsageShowsNoFigures(t *testing.T) {
	m := openAgentViewStatusModel(t, "sub-528-new")

	got := m.buildStatus()
	if strings.Contains(got, " tok") {
		t.Errorf("status %q shows token figures before the agent has any usage", got)
	}
	if strings.Contains(got, "nexos/") {
		t.Errorf("status %q names a model before the agent has made a call", got)
	}
	if !strings.Contains(got, "total") {
		t.Errorf("status %q lost the session total", got)
	}
}

// A job that is no longer tracked has no reliable state. The bar must not
// show the transcript's last state as if the agent were still running.
func TestAgentViewStatus_UntrackedJobShowsNoState(t *testing.T) {
	m := openAgentViewStatusModel(t, "sub-528-gone")
	delete(m.backgroundJobs, "sub-528-gone")

	got := m.buildStatus()
	if strings.Contains(got, "⟳") || strings.Contains(got, "thinking") {
		t.Errorf("status %q shows a live state for a job that is not tracked", got)
	}
}

// The catch-up replay of a long transcript must not count as a throughput
// measured in the first moments after the view opens.
func TestAgentViewStatus_CatchUpReplayIsNotThroughput(t *testing.T) {
	m := openAgentViewStatusModel(t, "sub-528-catchup",
		tools.LiveEvent{Kind: "text", Content: strings.Repeat("x", 4000)})

	got := m.buildStatus()
	if strings.Contains(got, "tok") {
		t.Errorf("status %q counts the replayed transcript as this round's tokens", got)
	}
}

func TestAgentViewStatus_EscRestoresTheMainStatus(t *testing.T) {
	m := openAgentViewStatusModel(t, "sub-528-esc")

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	got := model.(TuiModel).buildStatus()
	if !strings.Contains(got, "main/main-model") || !strings.Contains(got, "sending request") {
		t.Errorf("status after Esc = %q, want the main conversation again", got)
	}
}

// The session total is the item that must survive a narrow bar, so it comes
// first: fitStatusRight keeps the leading items and drops the rest.
func TestAgentViewStatus_TotalSurvivesANarrowBar(t *testing.T) {
	m := openAgentViewStatusModel(t, "sub-528-narrow")
	m.width = 20
	ledger.Record(ledger.Main, "nexos", "deepseek", "", stream.Usage{Input: 1_000_000})
	ledger.Record(ledger.Subagent, "nexos", "deepseek", "sub-528-narrow", stream.Usage{Input: 500_000, Output: 100_000})

	got := m.buildStatus()
	if !strings.Contains(got, "total 1.70$") {
		t.Errorf("status %q at width 20 lost the session total", got)
	}
}
