package flow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunOfSession_FindsTheRunOfAnAgentStep(t *testing.T) {
	home := t.TempDir()
	saveRun := func(run, status string, sessions ...string) {
		t.Helper()
		dir := filepath.Join(home, ".tyci", "runs", "repo", run)
		st := &RunState{Version: 1, Run: run, Status: status}
		for i, s := range sessions {
			st.History = append(st.History, Step{Seq: i + 1, Kind: "agent", Session: s})
		}
		if err := (&Store{Dir: dir}).Save(st); err != nil {
			t.Fatal(err)
		}
	}
	saveRun("run-a", "done", "job-1", "job-2")
	saveRun("run-b", "paused", "job-3")

	got, ok, err := RunOfSession(home, "repo", []string{"job-2"})
	if err != nil || !ok || got.Run != "run-a" || got.Status != "done" {
		t.Fatalf("RunOfSession(job-2) = %+v, %v, %v; want run-a done", got, ok, err)
	}
	if _, ok, err := RunOfSession(home, "repo", []string{"job-9"}); ok || err != nil {
		t.Fatal("an agent of no run must not be found")
	}
	if _, ok, err := RunOfSession(home, "other-repo", []string{"job-1"}); ok || err != nil {
		t.Fatal("runs of another repository must not be found")
	}
	if _, ok, err := RunOfSession(home, "repo", []string{""}); ok || err != nil {
		t.Fatal("an empty session must not be found")
	}
}

func TestRunOfSession_UnreadableStateIsAnError(t *testing.T) {
	home := t.TempDir()
	broken := filepath.Join(home, ".tyci", "runs", "repo", "run-broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, stateFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A run directory without a state file is not a run, so it is no error.
	if err := os.MkdirAll(filepath.Join(home, ".tyci", "runs", "repo", "run-empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := RunOfSession(home, "repo", []string{"job-x"}); ok || err == nil {
		t.Fatalf("ok=%v err=%v, want an error: the agent may belong to the broken run", ok, err)
	}
}

func TestRunByID_NoRunForANameThatIsNoRunId(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, ".tyci", "runs", "repo")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	// A plain file where a run directory would be: the path is not a directory.
	if err := os.WriteFile(filepath.Join(base, "20261008-125053-584"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"src/x.go", "Please review the change", "../repo", "20261008-125053-584", "run-active"} {
		if _, ok, err := RunByID(home, "repo", id); ok || err != nil {
			t.Fatalf("RunByID(%q) ok=%v err=%v, want no run and no error", id, ok, err)
		}
	}
}
