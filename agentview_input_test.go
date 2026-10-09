package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow"
)

// saveTestRun writes a workflow run of repo "repo" under home, with one agent
// step that ran as session job.
func saveTestRun(t *testing.T, home, run, status, worktree, job string) {
	t.Helper()
	dir := filepath.Join(home, ".tyci", "runs", "repo", run)
	st := &flow.RunState{Version: 1, Run: run, Status: status, Worktree: worktree,
		History: []flow.Step{{Seq: 1, Kind: "agent", Session: job}}}
	if err := (&flow.Store{Dir: dir}).Save(st); err != nil {
		t.Fatal(err)
	}
}

func TestRunWorkdirIn_RefusesAgentsOfActiveRuns(t *testing.T) {
	home := t.TempDir()
	wt := t.TempDir()
	info := flow.RepoInfo{Home: home, Repo: "owner/repo"}

	saveTestRun(t, home, "run-active", "running", wt, "job-active")
	_, err := runWorkdirIn(info, "job-active")
	if err == nil || !strings.Contains(err.Error(), "run-active (running)") {
		t.Fatalf("err = %v, want a refusal that names the active run", err)
	}

	saveTestRun(t, home, "run-paused", "paused", wt, "job-paused")
	if dir, err := runWorkdirIn(info, "job-paused"); err != nil || dir != wt {
		t.Fatalf("paused run: dir=%q err=%v, want the run worktree", dir, err)
	}
}

func TestRunWorkdirIn_RefusesWhenTheWorktreeIsGone(t *testing.T) {
	home := t.TempDir()
	info := flow.RepoInfo{Home: home, Repo: "owner/repo"}
	gone := filepath.Join(t.TempDir(), "removed")
	saveTestRun(t, home, "run-done", "done", gone, "job-done")

	if _, err := runWorkdirIn(info, "job-done"); err == nil || !strings.Contains(err.Error(), "worktree of run run-done is gone") {
		t.Fatalf("err = %v, want a refusal for the missing worktree", err)
	}
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if dir, err := runWorkdirIn(info, "job-done"); err != nil || dir != gone {
		t.Fatalf("dir=%q err=%v, want the worktree once it exists", dir, err)
	}
}

func TestRunWorkdirIn_AgentOfNoRunIsAllowed(t *testing.T) {
	info := flow.RepoInfo{Home: t.TempDir(), Repo: "owner/repo"}
	if dir, err := runWorkdirIn(info, "job-btw"); err != nil || dir != "" {
		t.Fatalf("dir=%q err=%v, want no worktree and no error", dir, err)
	}
}

func TestPostToLiveAgent_IsRefusedForAFinishedJob(t *testing.T) {
	// A job that the registry does not know is not live: Post must refuse it
	// instead of publishing into a dead mailbox.
	if err := (agentViewInput{}).Post("job-unknown", "hi"); err == nil {
		t.Fatal("Post to a job that is not running must fail")
	}
}
