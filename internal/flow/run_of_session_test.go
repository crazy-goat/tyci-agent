package flow

import (
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

	got, ok := RunOfSession(home, "repo", "job-2")
	if !ok || got.Run != "run-a" || got.Status != "done" {
		t.Fatalf("RunOfSession(job-2) = %+v, %v; want run-a done", got, ok)
	}
	if _, ok := RunOfSession(home, "repo", "job-9"); ok {
		t.Fatal("an agent of no run must not be found")
	}
	if _, ok := RunOfSession(home, "other-repo", "job-1"); ok {
		t.Fatal("runs of another repository must not be found")
	}
	if _, ok := RunOfSession(home, "repo", ""); ok {
		t.Fatal("an empty session must not be found")
	}
}
