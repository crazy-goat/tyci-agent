package flow

import (
	"os"
	"path/filepath"
	"testing"
)

// saveLegacyRun writes a run of repo into the runs dir layout before #846:
// <home>/.tyci/runs/<name>/<id>, without the owner.
func saveLegacyRun(t *testing.T, home, name, repo, id string) {
	t.Helper()
	st := &RunState{Version: 1, Run: id, Workflow: "demo", Repo: repo, Issue: 5,
		Status: "paused", Current: "ask", Visits: map[string]int{}, History: []Step{}}
	if err := (&Store{Dir: filepath.Join(home, ".tyci", "runs", name, id)}).Save(st); err != nil {
		t.Fatal(err)
	}
}

func TestRunDirKeepsOwner(t *testing.T) {
	home := t.TempDir()
	a := RunDir(home, RepoInfo{Repo: "acme/demo"}.Key(), "1")
	b := RunDir(home, RepoInfo{Repo: "other/demo"}.Key(), "1")
	if a == b {
		t.Fatalf("acme/demo and other/demo share run dir %s", a)
	}
	if got := RepoKey("group/sub/project"); got != "group+sub+project" {
		t.Fatalf("RepoKey = %q", got)
	}
}

func TestLegacyRunDirStillLoads(t *testing.T) {
	home := t.TempDir()
	saveLegacyRun(t, home, "demo", "acme/demo", "3")
	saveLegacyRun(t, home, "demo", "other/demo", "4")
	key := RepoKey("acme/demo")

	st, ok, err := RunByID(home, key, "#3")
	if err != nil || !ok || st.Repo != "acme/demo" {
		t.Fatalf("RunByID(#3) = %+v, %v, %v; want the legacy run of acme/demo", st, ok, err)
	}
	if _, ok, _ := RunByID(home, key, "#4"); ok {
		t.Fatal("run #4 of other/demo must not load as a run of acme/demo")
	}
	if got := RunDir(home, key, "3"); got != filepath.Join(home, ".tyci", "runs", "demo", "3") {
		t.Fatalf("RunDir of a legacy run = %s", got)
	}
	runs := RecentRuns(home, key, 10)
	if len(runs) != 1 || runs[0].Run != "3" {
		t.Fatalf("RecentRuns = %v, want only run 3", runs)
	}
}

func TestOldRunRecordDefaultsToCwdRepo(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".tyci", "runs", "demo", "2")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A state saved without "repo".
	state := `{"version":1,"run":"2","workflow":"demo","status":"done","current":"end"}`
	if err := os.WriteFile(StatePath(dir), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	info := RepoInfo{Home: home, Repo: "acme/demo"}
	m := &Manager{Info: func() (RepoInfo, error) { return info, nil }}
	st, err := m.Status("#2")
	if err != nil {
		t.Fatal(err)
	}
	if st.Repo != "acme/demo" {
		t.Fatalf("Repo = %q, want the repo of the lookup", st.Repo)
	}
}

func TestNewRunIDStartsAboveLegacyRuns(t *testing.T) {
	home := t.TempDir()
	saveLegacyRun(t, home, "demo", "acme/demo", "7")
	id, err := NewRunID(home, RepoKey("acme/demo"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "8" {
		t.Fatalf("first new run id = %s, want 8 (above the legacy run 7)", id)
	}
}
