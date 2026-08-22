package tools

// The "still running" heads-up for a backgrounded command.
//
// The case it exists for: a typo turns a five-second command into a hang, the
// model has already moved on, and nothing says anything until the 3600s
// backstop. A slow build looks identical from the outside, so the notice
// reports the age and asks for nothing — only the model knows which of the two
// it wrote.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// startWatched runs a command through watchBackgroundRun with a millisecond
// schedule, returning once it has finished.
func startWatched(t *testing.T, notifier *recordingNotifier, command string, first, every time.Duration) {
	t.Helper()
	run, err := startBash(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	waitErr, killed := watchBackgroundRun(context.Background(), run, "job-test", "the slow one", time.Now(), first, every)
	if killed {
		t.Fatal("the command was killed, not finished")
	}
	_ = waitErr
}

func TestBackgroundNoticeAfterTheFirstInterval(t *testing.T) {
	_, notifier := bgTestEnv(t)

	startWatched(t, notifier, "sleep 0.35", 100*time.Millisecond, time.Hour)

	notices := notifier.all()
	if len(notices) != 1 {
		t.Fatalf("expected exactly one heads-up, got %v", notices)
	}
	n := notices[0]
	for _, want := range []string{"the slow one", "has been running", "job-test", "no action needed"} {
		if !strings.Contains(n, want) {
			t.Errorf("the notice is missing %q: %q", want, n)
		}
	}
	// It must not order the model around: the command is probably fine, and
	// interrupting real work every time this fires would be worse than silence.
	for _, banned := range []string{"stop ", "check whether", "verify ", "must "} {
		if strings.Contains(strings.ToLower(n), banned) {
			t.Errorf("the notice tells the model to act (%q): %q", banned, n)
		}
	}
}

func TestBackgroundNoticeRepeatsOnTheLongerInterval(t *testing.T) {
	_, notifier := bgTestEnv(t)

	startWatched(t, notifier, "sleep 0.55", 100*time.Millisecond, 150*time.Millisecond)

	notices := notifier.all()
	if len(notices) < 3 {
		t.Fatalf("expected the notice to repeat, got %v", notices)
	}
	if !strings.Contains(notices[0], "has been running") {
		t.Errorf("first notice = %q", notices[0])
	}
	if !strings.Contains(notices[1], "still running") {
		t.Errorf("second notice should be the shorter follow-up, got %q", notices[1])
	}
}

// TestNoNoticeForACommandThatFinishesFirst: the overwhelming majority of
// backgrounded commands finish before the first interval, and a notice for
// those would be pure noise.
func TestNoNoticeForACommandThatFinishesFirst(t *testing.T) {
	_, notifier := bgTestEnv(t)

	startWatched(t, notifier, "true", time.Hour, time.Hour)

	if n := notifier.all(); len(n) != 0 {
		t.Fatalf("a quick command produced a heads-up: %v", n)
	}
}

// TestNoticeIsDueImmediatelyForAnAlreadyOldCommand: the age is measured from
// when the command started, not from when it was backgrounded, so one that was
// already past the interval on arrival is not skipped.
func TestNoticeIsDueImmediatelyForAnAlreadyOldCommand(t *testing.T) {
	_, notifier := bgTestEnv(t)

	run, err := startBash(context.Background(), "sleep 0.2")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-90 * time.Second) // as if it had run 90s already
	watchBackgroundRun(context.Background(), run, "job-old", "the old one", started, 60*time.Second, time.Hour)

	notices := notifier.all()
	if len(notices) != 1 {
		t.Fatalf("expected one immediate notice, got %v", notices)
	}
	if !strings.Contains(notices[0], "for 1m") {
		t.Errorf("the notice should report the age since the command started: %q", notices[0])
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		3 * time.Second:              "3s",
		59 * time.Second:             "59s",
		90 * time.Second:             "1m",
		5 * time.Minute:              "5m",
		59 * time.Minute:             "59m",
		time.Hour + 5*time.Minute:    "1h05m",
		2*time.Hour + 30*time.Minute: "2h30m",
	}
	for d, want := range cases {
		if got := humanDuration(d); got != want {
			t.Errorf("%v -> %q, want %q", d, got, want)
		}
	}
}

// TestTypingMovesABashCommandToTheBackground: same second trigger as the
// subagent handoff. The command keeps running either way, so holding the turn
// open buys the person nothing but a wait.
func TestTypingMovesABashCommandToTheBackground(t *testing.T) {
	reg, _ := bgTestEnv(t)

	SetUserPending(func() bool { return true })
	defer SetUserPending(nil)

	start := time.Now()
	res := RunTool(context.Background(), "bash", map[string]any{
		"command":     "sleep 5; echo finished-anyway",
		"description": "the slow one",
	})
	if !res.Success {
		t.Fatalf("%s", res.Error)
	}
	// The default handoff is at 30s; typing must not wait for it.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("waited %v before handing over", elapsed)
	}
	if !strings.Contains(res.Content, "job_id=") {
		t.Fatalf("expected a handoff, got %q", res.Content)
	}

	// Untouched: the command finishes on its own and its output is readable.
	id := bgJobIDFrom(t, res.Content)
	job, ok := reg.Wait(context.Background(), id, 15*time.Second)
	if !ok {
		t.Fatal("the command did not finish")
	}
	if !strings.Contains(job.Result, "finished-anyway") {
		t.Fatalf("the command was disturbed: %q", job.Result)
	}
}

func bgJobIDFrom(t *testing.T, content string) string {
	t.Helper()
	const marker = `job_id="`
	i := strings.Index(content, marker)
	if i < 0 {
		t.Fatalf("no job_id in %q", content)
	}
	rest := content[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("unterminated job_id in %q", content)
	}
	return rest[:j]
}
