package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
)

// scriptedDrain returns a NextMessages callback that answers its i-th call
// with steps[i], and nothing once the script is over.
func scriptedDrain(steps ...[]string) func() []string {
	call := 0
	return func() []string {
		defer func() { call++ }()
		if call < len(steps) {
			return steps[call]
		}
		return nil
	}
}

// countMarked counts the messages whose text holds marker.
func countMarked(msgs []connector.Message, marker string) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, marker) {
				n++
			}
		}
	}
	return n
}

// A notice from a job or a run is not a person typing, so it must not start a
// fresh sub-task. Each notice below arrives right after a job reminder, which
// is the case that used to reset the budget and nag without a limit.
func TestJobReminderBudgetSurvivesNotices(t *testing.T) {
	notice := []string{"[notice from=job-2 kind=job.status] job-2 finished"}
	cfg := Config{
		MaxRetries: 1,
		PendingJobs: func() []string {
			return []string{`WAITING FOR ANSWER: forever (job_id=job-1) asks: "still there?"`}
		},
		NextMessages: scriptedDrain(nil, notice, nil, notice, nil),
	}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	if _, err := Run(context.Background(), countingText(), &silentDisplay{}, &msgs, cfg); err != nil {
		t.Fatal(err)
	}

	if got := countMarked(msgs, "automated check"); got != maxJobReminders {
		t.Fatalf("job reminders = %d, want %d: notices must not reset the budget", got, maxJobReminders)
	}
}

// A real user follow-up still starts a fresh sub-task, so the job reminder may
// run again after it. A batch that mixes a notice with a user line is a user
// follow-up too.
func TestJobReminderBudgetResetsOnUserMessage(t *testing.T) {
	cfg := Config{
		MaxRetries: 1,
		PendingJobs: func() []string {
			return []string{`WAITING FOR ANSWER: forever (job_id=job-1) asks: "still there?"`}
		},
		NextMessages: scriptedDrain(nil, nil,
			[]string{"[notice from=job-2 kind=job.status] job-2 finished", "what about the tests?"},
			nil, nil),
	}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	if _, err := Run(context.Background(), countingText(), &silentDisplay{}, &msgs, cfg); err != nil {
		t.Fatal(err)
	}

	if got, want := countMarked(msgs, "automated check"), 2*maxJobReminders; got != want {
		t.Fatalf("job reminders = %d, want %d: a user message must reset the budget", got, want)
	}
}

// The todo budget has the same comment and the same reset, so a notice must
// not reset it either.
func TestTodoReminderBudgetSurvivesNotices(t *testing.T) {
	notice := []string{"[notice from=job-2 kind=job.status] job-2 finished"}
	cfg := Config{
		MaxRetries:   1,
		PendingTodos: func() []string { return []string{"1. [doing] high finish the thing"} },
		NextMessages: scriptedDrain(nil, notice, nil, notice, nil),
	}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	if _, err := Run(context.Background(), countingText(), &silentDisplay{}, &msgs, cfg); err != nil {
		t.Fatal(err)
	}

	if got := countMarked(msgs, "finish the thing"); got != maxTodoReminders {
		t.Fatalf("todo reminders = %d, want %d: notices must not reset the budget", got, maxTodoReminders)
	}
}
