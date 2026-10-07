package flow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStore_SaveIsAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	s := &Store{Dir: dir}
	st := newRun("a")
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			b, err := os.ReadFile(path)
			if err != nil || !json.Valid(b) {
				done <- os.ErrInvalid
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		if err := s.Save(st); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal("reader saw a missing or partial file")
	}
	m, _ := filepath.Glob(filepath.Join(dir, "*.tmp*"))
	if len(m) != 0 {
		t.Fatalf("temp files left: %v", m)
	}
}

func TestStore_PermissionBits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	if err := (&Store{Dir: dir}).Save(newRun("a")); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(dir, "state.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode().Perm())
	}
}

func TestStore_MasksTokens(t *testing.T) {
	dir := t.TempDir()
	st := newRun("a")
	st.Reason = "bad ghp_abc123"
	st.Ask = &Ask{Message: "Bearer abc", Reason: "github_pat_x_y"}
	st.History = []Step{{StderrTail: "key sk-ab-cd", Warnings: []string{"ghp_zzz"}}}
	if err := (&Store{Dir: dir}).Save(st); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	for _, bad := range []string{"ghp_", "abc", "github_pat_", "sk-"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("token %q leaked: %s", bad, b)
		}
	}
	if !strings.Contains(string(b), "***") {
		t.Fatal("no mask")
	}
	if st.Reason != "bad ghp_abc123" || st.Ask.Message != "Bearer abc" ||
		st.History[0].StderrTail != "key sk-ab-cd" || st.History[0].Warnings[0] != "ghp_zzz" {
		t.Fatal("caller struct changed")
	}
}

func TestStore_SetsUpdatedAt(t *testing.T) {
	st := newRun("a")
	if err := (&Store{Dir: t.TempDir()}).Save(st); err != nil {
		t.Fatal(err)
	}
	if time.Since(st.UpdatedAt) > time.Minute || st.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt %v", st.UpdatedAt)
	}
}

func TestLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := newRun("a")
	st.PR = 7
	if err := (&Store{Dir: dir}).Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Run != st.Run || got.PR != 7 || got.Current != "a" {
		t.Fatalf("got %+v", got)
	}
}

func TestLoad_UnknownVersion(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"version":9}`), 0o600)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("err %v", err)
	}
}

func TestLoad_Missing(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("want error")
	}
}

func TestNewRunID_Format(t *testing.T) {
	got := NewRunID(160, time.Date(2026, 10, 5, 12, 3, 1, 0, time.UTC))
	if got != "20261005-120301-160" {
		t.Fatal(got)
	}
}

func TestLatestRun_PicksNewest(t *testing.T) {
	home := t.TempDir()
	for _, id := range []string{"20261005-120000-200", "20261005-130000-5", "20261004-100000-900"} {
		if err := os.MkdirAll(RunDir(home, "r", id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LatestRun(home, "r")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "20261005-130000-5" {
		t.Fatal(got)
	}
	if _, err := LatestRun(home, "none"); err == nil {
		t.Fatal("want error")
	}
}

func TestRunner_WritesStateAfterEveryTransition(t *testing.T) {
	wf := &Workflow{Start: "a", States: map[string]State{
		"a":   {Check: "x.sh", On: map[string]string{"go": "b"}},
		"b":   {Check: "y.sh", On: map[string]string{"go": "end"}},
		"end": {End: true},
	}}
	var currents, statuses []string
	cs := &recStore{fn: func(st *RunState) {
		currents = append(currents, st.Current)
		statuses = append(statuses, st.Status)
	}}
	r := &Runner{WF: wf, Checks: &fakeChecks{keys: map[string][]string{"x.sh": {"go"}, "y.sh": {"go"}}}, Store: cs}
	if err := r.Run(context.Background(), newRun("")); err != nil {
		t.Fatal(err)
	}
	// entry+transition for a and b, then done
	want := []string{"a", "b", "b", "end", "end"}
	if strings.Join(currents, ",") != strings.Join(want, ",") {
		t.Fatalf("currents %v", currents)
	}
	if len(currents) != 5 || statuses[0] != "running" || statuses[4] != "done" {
		t.Fatalf("saves %v %v", currents, statuses)
	}
}

type recStore struct{ fn func(*RunState) }

func (r *recStore) Save(st *RunState) error { r.fn(st); return nil }

func TestRunner_ReadsPRFile(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, "pr"), []byte("171\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wf := &Workflow{Start: "a", States: map[string]State{
		"a":   {Check: "x.sh", On: map[string]string{"go": "end"}},
		"end": {End: true},
	}}
	st := newRun("")
	r := &Runner{WF: wf, Checks: &fakeChecks{keys: map[string][]string{"x.sh": {"go"}}}, Store: &memStore{}, RunDir: runDir}
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if st.PR != 171 {
		t.Fatalf("PR %d", st.PR)
	}
}

func TestRecentRuns(t *testing.T) {
	home := t.TempDir()
	for _, id := range []string{"20260101-a", "20260103-c", "20260102-b"} {
		st := &RunState{Version: 1, Run: id, Status: "done"}
		if err := (&Store{Dir: RunDir(home, "r", id)}).Save(st); err != nil {
			t.Fatal(err)
		}
	}
	got := RecentRuns(home, "r", 2)
	if len(got) != 2 || got[0].Run != "20260103-c" || got[1].Run != "20260102-b" {
		t.Fatalf("got %+v", got)
	}
}
