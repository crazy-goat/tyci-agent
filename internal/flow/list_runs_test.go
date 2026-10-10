package flow

import (
	"strings"
	"testing"
	"time"
)

// saveListRun writes a run of repo with the status and the id given.
func saveListRun(t *testing.T, home, repo, id, status string) {
	t.Helper()
	st := &RunState{Version: 1, Run: id, Workflow: "demo", Repo: "acme/" + repo, Issue: 7,
		Status: status, Current: "check", Params: map[string]string{"issue": "7"},
		StartedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	if err := (&Store{Dir: RunDir(home, repo, id)}).Save(st); err != nil {
		t.Fatal(err)
	}
}

func listRunIDs(list RunList) []string {
	var ids []string
	for _, r := range list.Runs {
		ids = append(ids, r.Run)
	}
	return ids
}

func TestListRunsCurrentOnlyByDefault(t *testing.T) {
	home := t.TempDir()
	saveListRun(t, home, "demo", "20261005-100000-1", "done")
	saveListRun(t, home, "demo", "20261005-110000-2", "running")
	saveListRun(t, home, "demo", "20261005-120000-3", "paused")
	saveListRun(t, home, "demo", "20261005-130000-4", "failed")
	saveListRun(t, home, "demo", "20261005-140000-5", "stopped")

	list, err := ListRuns(home, "demo", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(listRunIDs(list), " "); got != "20261005-120000-3 20261005-110000-2" {
		t.Fatalf("runs = %s, want the paused and running runs, newest first", got)
	}
	if list.Total != 2 || list.More || list.Limit != DefaultRunListLimit || list.Offset != 0 {
		t.Fatalf("page = %+v", list)
	}
	r := list.Runs[0]
	if r.Workflow != "demo" || r.Status != "paused" || r.State != "check" || r.Params["issue"] != "7" || r.StartedAt.IsZero() {
		t.Fatalf("row = %+v", r)
	}
}

func TestListRunsArchivedIncludesFinishedRuns(t *testing.T) {
	home := t.TempDir()
	saveListRun(t, home, "demo", "20261005-100000-1", "done")
	saveListRun(t, home, "demo", "20261005-110000-2", "running")
	saveListRun(t, home, "demo", "20261005-130000-4", "stopped")

	list, err := ListRuns(home, "demo", true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 3 || len(list.Runs) != 3 || list.Runs[0].Run != "20261005-130000-4" {
		t.Fatalf("archived list = %+v", list)
	}
}

func TestListRunsPaging(t *testing.T) {
	home := t.TempDir()
	for _, id := range []string{"20261005-100000-1", "20261005-110000-2", "20261005-120000-3", "20261005-130000-4", "20261005-140000-5"} {
		saveListRun(t, home, "demo", id, "running")
	}

	first, err := ListRuns(home, "demo", false, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(listRunIDs(first), " "); got != "20261005-140000-5 20261005-130000-4" || first.Total != 5 || !first.More {
		t.Fatalf("first page = %s %+v", got, first)
	}
	second, err := ListRuns(home, "demo", false, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(listRunIDs(second), " "); got != "20261005-120000-3 20261005-110000-2" || !second.More || second.Offset != 2 {
		t.Fatalf("second page = %s %+v", got, second)
	}
	last, err := ListRuns(home, "demo", false, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(listRunIDs(last), " "); got != "20261005-100000-1" || last.More || last.Total != 5 {
		t.Fatalf("last page = %s %+v", got, last)
	}
	past, err := ListRuns(home, "demo", false, 2, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(past.Runs) != 0 || past.Runs == nil || past.Total != 5 || past.More {
		t.Fatalf("page past the end = %+v", past)
	}
}

func TestListRunsScopedToRepo(t *testing.T) {
	home := t.TempDir()
	saveListRun(t, home, "demo", "20261005-100000-1", "running")
	saveListRun(t, home, "other", "20261005-110000-2", "running")

	list, err := ListRuns(home, "demo", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || list.Runs[0].Run != "20261005-100000-1" {
		t.Fatalf("runs of demo = %+v", list)
	}
}

func TestListRunsEmpty(t *testing.T) {
	home := t.TempDir()
	list, err := ListRuns(home, "demo", true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if list.Runs == nil || len(list.Runs) != 0 || list.Total != 0 || list.More {
		t.Fatalf("empty list = %+v", list)
	}
}

func TestListRunsRejectsBadPaging(t *testing.T) {
	home := t.TempDir()
	if _, err := ListRuns(home, "demo", false, -1, 0); err == nil {
		t.Error("negative limit accepted")
	}
	if _, err := ListRuns(home, "demo", false, 1, -1); err == nil {
		t.Error("negative offset accepted")
	}
}
