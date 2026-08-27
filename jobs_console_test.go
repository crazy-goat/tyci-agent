package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/jobs"
)

func TestPrintJobs_EmptyRegistry(t *testing.T) {
	var buf bytes.Buffer
	printJobs(&buf, nil)
	if got := buf.String(); got != "No background jobs.\n" {
		t.Errorf("got %q", got)
	}
}

// TestPrintJobs_WaitingQuestionIsVisibleAndFirst is the actual point of item
// 36: a person running "/jobs" must be able to notice a child blocked on a
// question without the model choosing to relay it — waiting_answer jobs
// come first and show the question text, not the (possibly stale)
// description.
func TestPrintJobs_WaitingQuestionIsVisibleAndFirst(t *testing.T) {
	now := time.Now()
	all := []jobs.Job{
		{ID: "job-1-1", Status: jobs.StatusRunning, Description: "doing something", StartedAt: now.Add(-2 * time.Second)},
		{ID: "job-2-1", Status: jobs.StatusWaitingAnswer, Description: "old description", Question: "which branch?", StartedAt: now.Add(-1 * time.Second)},
	}
	var buf bytes.Buffer
	printJobs(&buf, all)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %v", lines)
	}
	if !strings.Contains(lines[0], "waiting_answer") || !strings.Contains(lines[0], `asks: "which branch?"`) {
		t.Errorf("waiting job must be first and show its question, got: %q", lines[0])
	}
	if strings.Contains(lines[0], "old description") {
		t.Errorf("a waiting job's line must show the question, not the stale description: %q", lines[0])
	}
	if !strings.Contains(lines[1], "running") {
		t.Errorf("running job expected second, got: %q", lines[1])
	}
}

func TestPrintJobs_TerminalJobsCappedWithCount(t *testing.T) {
	now := time.Now()
	var all []jobs.Job
	for i := 0; i < maxConsoleTerminalJobLines+3; i++ {
		all = append(all, jobs.Job{
			ID: "job-x-" + string(rune('a'+i)), Status: jobs.StatusDone,
			StartedAt: now, FinishedAt: now.Add(time.Duration(i) * time.Second),
		})
	}
	var buf bytes.Buffer
	printJobs(&buf, all)
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// maxConsoleTerminalJobLines job lines + 1 "and N more" line.
	if len(lines) != maxConsoleTerminalJobLines+1 {
		t.Fatalf("expected %d lines, got %d: %v", maxConsoleTerminalJobLines+1, len(lines), lines)
	}
	if !strings.Contains(lines[len(lines)-1], "… and 3 more finished job(s)") {
		t.Errorf("expected an overflow count line, got: %q", lines[len(lines)-1])
	}
}

func TestPrintJobs_LiveJobsNeverCapped(t *testing.T) {
	now := time.Now()
	var all []jobs.Job
	for i := 0; i < maxConsoleTerminalJobLines+5; i++ {
		all = append(all, jobs.Job{ID: "job-y-" + string(rune('a'+i)), Status: jobs.StatusRunning, StartedAt: now})
	}
	var buf bytes.Buffer
	printJobs(&buf, all)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != len(all) {
		t.Errorf("expected every running job shown uncapped, got %d lines for %d jobs", len(lines), len(all))
	}
}

// TestHandleCommand_JobsIsRecognized pins the dispatch: "/jobs" must not
// fall through to the "Unknown command" default branch. A bare
// &interactiveState{} is safe here because the /jobs branch touches only
// the global JobRegistry and os.Stdout, never s.cond/s.display.
func TestHandleCommand_JobsIsRecognized(t *testing.T) {
	reg := jobs.NewRegistry()
	orig := JobRegistry
	JobRegistry = reg
	defer func() { JobRegistry = orig }()

	s := &interactiveState{}
	_, cancel := context.WithCancel(context.Background())
	exit, handled := s.handleCommand("/jobs", cancel)
	if exit {
		t.Fatal("/jobs must not exit the loop")
	}
	if !handled {
		t.Fatal("/jobs must be handled, not fall through to submit/unknown-command")
	}
}
