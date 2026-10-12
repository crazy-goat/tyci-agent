package flow

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// saveTestRun writes a minimal state of run id of repo "r" under home.
func saveTestRun(t *testing.T, home, id string) {
	t.Helper()
	st := &RunState{Version: 1, Run: id, Workflow: "demo", Repo: "o/r", Issue: 17,
		Status: "done", Current: "end", Visits: map[string]int{}, History: []Step{}}
	if err := (&Store{Dir: RunDir(home, "r", id)}).Save(st); err != nil {
		t.Fatal(err)
	}
}

func TestNewRunID_ParallelCallsGetDifferentNumbers(t *testing.T) {
	home := t.TempDir()
	const n = 8
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], errs[i] = NewRunID(home, "r")
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for i := range n {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if seen[ids[i]] {
			t.Fatalf("run id %s given twice", ids[i])
		}
		seen[ids[i]] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[strconv.Itoa(i)] {
			t.Fatalf("numbers 1..%d expected, got %v", n, ids)
		}
	}
}

func TestNewRunID_NeverReusesDeletedNumber(t *testing.T) {
	home := t.TempDir()
	first := testRunID(home)
	if err := os.RemoveAll(RunDir(home, "r", first)); err != nil {
		t.Fatal(err)
	}
	if second := testRunID(home); second != "2" {
		t.Fatalf("second run id = %s, want 2", second)
	}
}

func TestNewRunID_MissingCounterStartsAfterHighestRunDir(t *testing.T) {
	home := t.TempDir()
	if got := testRunID(home); got != "1" {
		t.Fatalf("empty repo: run id = %s, want 1", got)
	}
	home = t.TempDir()
	for _, name := range []string{"7", "3", "20261005-120000-200"} {
		if err := os.MkdirAll(RunDir(home, "r", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got := testRunID(home); got != "8" {
		t.Fatalf("run id = %s, want 8", got)
	}
}

func TestParseRunID(t *testing.T) {
	ok := map[string]string{
		"17":                       "17",
		"#17":                      "17",
		"20261005-120000-200":      "20261005-120000-200",
		"20261005-120000-0-ab12cd": "20261005-120000-0-ab12cd",
	}
	for in, want := range ok {
		if got, valid := ParseRunID(in); !valid || got != want {
			t.Errorf("ParseRunID(%q) = %q, %v; want %q, true", in, got, valid, want)
		}
	}
	for _, in := range []string{"", "#", "0", "017", "##17", "-1", "../17", "17/../x", "#../17", "abc", "17 "} {
		if _, valid := ParseRunID(in); valid {
			t.Errorf("ParseRunID(%q) is valid, want refused", in)
		}
	}
}

func TestRunLookup_NumberAndOldID(t *testing.T) {
	home := t.TempDir()
	info := RepoInfo{Home: home, Repo: "o/r"}
	saveTestRun(t, home, "17")
	saveTestRun(t, home, "20261005-120000-200")

	for _, id := range []string{"17", "#17"} {
		st, err := loadRun(info, id)
		if err != nil || st.Run != "17" {
			t.Fatalf("loadRun(%q) = %v, %v", id, st, err)
		}
		st, _, err = FindRun(home, id)
		if err != nil || st.Run != "17" {
			t.Fatalf("FindRun(%q) = %v, %v", id, st, err)
		}
		st, ok, err := RunByID(home, "r", id)
		if err != nil || !ok || st.Run != "17" {
			t.Fatalf("RunByID(%q) = %v, %v, %v", id, st, ok, err)
		}
	}
	const old = "20261005-120000-200"
	if st, err := loadRun(info, old); err != nil || st.Run != old {
		t.Fatalf("old id loadRun(%q) = %v, %v", old, st, err)
	}
	if st, _, err := FindRun(home, old); err != nil || st.Run != old {
		t.Fatalf("old id FindRun(%q) = %v, %v", old, st, err)
	}
	recent := RecentRuns(home, "r", 10)
	if len(recent) != 2 {
		t.Fatalf("RecentRuns lists %d runs, want 2 (old and new)", len(recent))
	}
	if got := ShowRunID("20261005-120000-200"); got != "20261005-120000-200" {
		t.Fatalf("ShowRunID of an old id = %q", got)
	}
	if got := ShowRunID("17"); got != "#17" {
		t.Fatalf("ShowRunID = %q, want #17", got)
	}
}

func TestRunLookup_InvalidIDsAreRefused(t *testing.T) {
	home := t.TempDir()
	info := RepoInfo{Home: home, Repo: "o/r"}
	// A state file outside the runs dir must not be reachable through an id.
	outside := filepath.Join(home, ".tyci", "runs", "secret")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../secret", "../../r/17", "17/..", "0", "017", "#", "abc"} {
		if _, err := loadRun(info, id); err == nil {
			t.Errorf("loadRun(%q) accepted an invalid id", id)
		}
		if _, _, err := FindRun(home, id); err == nil {
			t.Errorf("FindRun(%q) accepted an invalid id", id)
		}
		if _, ok, _ := RunByID(home, "r", id); ok {
			t.Errorf("RunByID(%q) accepted an invalid id", id)
		}
	}
}

func TestManagerStatus_AcceptsNumberWithHash(t *testing.T) {
	home := t.TempDir()
	info := RepoInfo{Home: home, Repo: "o/r"}
	saveTestRun(t, home, "17")
	m := &Manager{Info: func() (RepoInfo, error) { return info, nil }}
	for _, id := range []string{"17", "#17"} {
		st, err := m.Status(id)
		if err != nil || st.Run != "17" {
			t.Fatalf("Status(%q) = %v, %v", id, st, err)
		}
	}
	if _, err := m.Status("../17"); err == nil {
		t.Fatal("Status accepted a path")
	}
}

func TestRunsListShowsNumberWithHash(t *testing.T) {
	home := t.TempDir()
	saveTestRun(t, home, "17")
	list, err := ListRuns(home, "r", true, 10, 0)
	if err != nil || len(list.Runs) != 1 || list.Runs[0].Run != "#17" {
		t.Fatalf("ListRuns = %+v, %v", list, err)
	}
}

func TestFindRun_SameNumberInTwoRepos_IsAmbiguous(t *testing.T) {
	home := t.TempDir()
	saveTestRun(t, home, "1")
	if err := (&Store{Dir: RunDir(home, "other", "1")}).Save(&RunState{Version: 1, Run: "1",
		Workflow: "demo", Repo: "o/other", Status: "done", Current: "end",
		Visits: map[string]int{}, History: []Step{}}); err != nil {
		t.Fatal(err)
	}
	_, _, err := FindRun(home, "#1")
	if err == nil || !strings.Contains(err.Error(), "#1 is in more than one repository") {
		t.Fatalf("FindRun = %v, want an ambiguity error", err)
	}
}

func TestRunOrder_NumbersByValueBeforeOldIds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const old = "20261012-120000-17"
	for _, id := range []string{"2", "9", "10", old} {
		saveTestRun(t, home, id)
	}
	dir, err := LatestRun(home, "r")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) != "10" {
		t.Fatalf("LatestRun = %s, want 10", filepath.Base(dir))
	}
	want := []string{"10", "9", "2", old}
	var got []string
	for _, st := range RecentRuns(home, "r", 10) {
		got = append(got, st.Run)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("RecentRuns order = %v, want %v", got, want)
	}
	list, err := ListRuns(home, "r", true, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	got = got[:0]
	for _, row := range list.Runs {
		got = append(got, row.Run)
	}
	if want := []string{"#10", "#9", "#2", old}; !slices.Equal(got, want) {
		t.Fatalf("ListRuns order = %v, want %v", got, want)
	}
}
