package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/agent"
	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
)

// TestJobResumerAdapter_ChainKeepsTheOriginOfTheRun covers the stash that the
// resume adapter makes for a resumed job. The origin must survive a chain of
// resumes, so the run guard still finds the run of the first agent.
func TestJobResumerAdapter_ChainKeepsTheOriginOfTheRun(t *testing.T) {
	reg, _ := withTestWiring(t)
	resetResumableForTest(t)

	const orig = "resume-origin-orig"
	// Two turns: the first resume and the resume of its resumed job share one model client.
	fake := connectortest.Text("first answer")
	fake.Turns = append(fake.Turns, connectortest.Text("second answer").Turns[0])
	stashResumable(orig, resumableEntry{
		msgs: []connector.Message{
			{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}},
		},
		mc:          fake,
		cfg:         agent.Config{MaxRetries: 1},
		todoAgentID: orig,
	})
	ctx := connector.WithModelClient(context.Background(), fake)

	first, err := (jobResumerAdapter{reg: reg}).Resume(ctx, orig, "continue")
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	if _, ok := reg.Wait(context.Background(), first.ID(), 5*time.Second); !ok {
		t.Fatal("first resumed job never finished")
	}
	resumableMu.Lock()
	firstEntry, ok := resumable[first.ID()]
	resumableMu.Unlock()
	if !ok || len(firstEntry.chain) != 2 || firstEntry.chain[0] != orig || firstEntry.chain[1] != first.ID() {
		t.Fatalf("stash of the first resume = %+v (ok=%v), want chain [%s %s]", firstEntry.chain, ok, orig, first.ID())
	}

	second, err := (jobResumerAdapter{reg: reg}).Resume(ctx, first.ID(), "more")
	if err != nil {
		t.Fatalf("chained resume: %v", err)
	}
	if _, ok := reg.Wait(context.Background(), second.ID(), 5*time.Second); !ok {
		t.Fatal("chained resumed job never finished")
	}
	resumableMu.Lock()
	secondEntry, ok := resumable[second.ID()]
	resumableMu.Unlock()
	if !ok || len(secondEntry.chain) != 3 || secondEntry.chain[0] != orig {
		t.Fatalf("stash of the chained resume = %+v (ok=%v), want a chain that starts at %s", secondEntry.chain, ok, orig)
	}

	// The run guard of the chained job uses the chain: a running run refuses it.
	home := t.TempDir()
	saveTestRun(t, home, "run-active", "running", t.TempDir(), orig)
	useRunInfo(t, home, nil)
	if _, err := agentRunWorkdir(second.ID()); err == nil || !strings.Contains(err.Error(), "run-active (running)") {
		t.Fatalf("err = %v, want the chained job refused for the active run", err)
	}
}

// TestJobResumerAdapter_ReportReminderIsGuarded covers a run that records the
// reminder job: the runner saves the id of the last reminder in the history.
// The guard must find the run under the reminder id and under the original id.
func TestJobResumerAdapter_ReportReminderIsGuarded(t *testing.T) {
	reg, _ := withTestWiring(t)
	resetResumableForTest(t)

	const orig = "resume-reminder-orig"
	fake := connectortest.Text("reminded answer")
	stashResumable(orig, resumableEntry{
		msgs: []connector.Message{
			{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}},
		},
		mc:          fake,
		cfg:         agent.Config{MaxRetries: 1},
		todoAgentID: orig,
	})
	ctx := connector.WithModelClient(context.Background(), fake)
	reminder, err := (jobResumerAdapter{reg: reg}).Resume(ctx, orig, "write your report")
	if err != nil {
		t.Fatalf("reminder resume: %v", err)
	}
	if _, ok := reg.Wait(context.Background(), reminder.ID(), 5*time.Second); !ok {
		t.Fatal("reminder job never finished")
	}

	t.Run("active run", func(t *testing.T) {
		home := t.TempDir()
		saveTestRun(t, home, "run-active", "running", t.TempDir(), reminder.ID())
		useRunInfo(t, home, nil)
		for _, id := range []string{orig, reminder.ID()} {
			if _, err := agentRunWorkdir(id); err == nil || !strings.Contains(err.Error(), "run-active (running)") {
				t.Fatalf("%s: err = %v, want a refusal for the active run", id, err)
			}
		}
	})

	t.Run("done run", func(t *testing.T) {
		home := t.TempDir()
		wt := t.TempDir()
		saveTestRun(t, home, "run-done", "done", wt, reminder.ID())
		useRunInfo(t, home, nil)
		for _, id := range []string{orig, reminder.ID()} {
			if dir, err := agentRunWorkdir(id); err != nil || dir != wt {
				t.Fatalf("%s: dir=%q err=%v, want the run worktree", id, dir, err)
			}
		}
	})
}
