package flow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/tools"
)

type fakeRender struct{}

func (fakeRender) Render(name string, _ RunContext) (string, error) { return "rendered " + name, nil }

func testCfg() *flowconfig.Config {
	return &flowconfig.Config{
		Models:       map[string]string{"m": "prov/model"},
		DefaultModel: "m",
		Roles: map[string]flowconfig.Role{
			"worker": {Prompt: "W"}, "review": {Prompt: "R"}, "fixer": {Prompt: "F"}, "oracle": {Prompt: "O"},
		},
	}
}

type spawnRec struct {
	specs  []tools.TaskSpec
	result string
	err    error
	n      int
	hook   func()
}

func (s *spawnRec) spawn(_ context.Context, sp tools.TaskSpec) (string, string, error) {
	s.specs = append(s.specs, sp)
	s.n++
	if s.hook != nil {
		s.hook()
	}
	return s.result, "sess-" + string(rune('a'+s.n)), s.err
}

func newRunner(s *spawnRec) *SubagentRunner {
	return &SubagentRunner{
		Cfg: testCfg(), Render: fakeRender{}, Spawn: s.spawn,
		IssueContext: func(context.Context, string, int) (string, error) { return "ISSUE", nil },
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "i"}} {
		c := exec.Command("git", args...)
		c.Dir = d
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return d
}

func writeReview(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSubagentRunner_UsesRunWorktree(t *testing.T) {
	s := &spawnRec{}
	wt := t.TempDir()
	key, _, err := newRunner(s).Run(context.Background(), "worker", "", RunContext{Worktree: wt})
	if err != nil || key != "done" {
		t.Fatalf("key=%q err=%v", key, err)
	}
	sp := s.specs[0]
	if sp.Dir != wt || sp.Model != "prov/model" || sp.SystemPrompt != "W" || !strings.Contains(sp.Task, "ISSUE") {
		t.Errorf("spec = %+v", sp)
	}
}

// #304: the role limits reach the child.
func TestSubagentRunner_PassesRoleCompactLimits(t *testing.T) {
	s := &spawnRec{}
	r := newRunner(s)
	r.Cfg.Roles["worker"] = flowconfig.Role{Prompt: "W", CompactSoftLimit: 100000, CompactHardLimit: 150000}
	if _, _, err := r.Run(context.Background(), "worker", "", RunContext{Worktree: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if sp := s.specs[0]; sp.SoftLimit != 100000 || sp.HardLimit != 150000 {
		t.Fatalf("spec limits = %d, %d", sp.SoftLimit, sp.HardLimit)
	}
}

func TestSubagentRunner_ErrorGivesErrorKey(t *testing.T) {
	s := &spawnRec{err: errors.New("boom")}
	_, _, err := newRunner(s).Run(context.Background(), "worker", "", RunContext{})
	if err == nil {
		t.Fatal("want error (the runner maps it to key error)")
	}
	wf := &Workflow{Start: "w", States: map[string]State{
		"w": {Agent: "worker", On: map[string]string{"done": "end", "error": "end"}}, "end": {End: true}}}
	rn := &Runner{WF: wf, Agents: newRunner(s)}
	st := &RunState{Version: 1, Current: "w", Status: "running", Visits: map[string]int{}}
	_ = rn.Run(context.Background(), st)
	if len(st.History) == 0 || st.History[0].Key != "error" {
		t.Fatalf("history = %+v", st.History)
	}
}

func TestSubagentRunner_SessionIDsDifferBetweenWorkerAndReview(t *testing.T) {
	s := &spawnRec{}
	r := newRunner(s)
	run := t.TempDir()
	writeReview(t, run, "ACCEPT\n")
	_, w, _ := r.Run(context.Background(), "worker", "", RunContext{Worktree: gitRepo(t)})
	_, v, _ := r.Run(context.Background(), "review", "", RunContext{Worktree: gitRepo(t), ArtifactDir: run})
	if w == "" || v == "" || w == v {
		t.Errorf("sessions %q %q", w, v)
	}
}

func TestVerdict_AcceptChangesAndGarbage(t *testing.T) {
	cases := []struct {
		text, want string
		warn       bool
	}{
		{"ACCEPT\nmore", "ACCEPT", false},
		{"CHANGES", "CHANGES", false},
		{"", "CHANGES", true},
		{"accept\n", "CHANGES", true},
	}
	for _, c := range cases {
		d := t.TempDir()
		writeReview(t, d, c.text)
		v, w := readVerdict(filepath.Join(d, "report.md"))
		if v != c.want || (w != "") != c.warn {
			t.Errorf("%q: v=%q w=%q", c.text, v, w)
		}
	}
	if v, w := readVerdict(filepath.Join(t.TempDir(), "report.md")); v != "CHANGES" || w == "" {
		t.Errorf("missing: v=%q w=%q", v, w)
	}
}

func TestVerdict_ReviewerDirtyWorktreeForcesChanges(t *testing.T) {
	run, wt := t.TempDir(), gitRepo(t)
	writeReview(t, run, "ACCEPT\n")
	s := &spawnRec{hook: func() { _ = os.WriteFile(filepath.Join(wt, "x"), []byte("x"), 0o644) }}
	var warns []string
	r := newRunner(s)
	r.Warn = func(m string) { warns = append(warns, m) }
	key, _, err := r.Run(context.Background(), "review", "", RunContext{Worktree: wt, ArtifactDir: run})
	if err != nil || key != "CHANGES" || len(warns) != 1 || !strings.Contains(warns[0], "modified the worktree") {
		t.Fatalf("key=%q err=%v warns=%v", key, err, warns)
	}
}

func TestVerdict_CleanWorktreeAccepts(t *testing.T) {
	run := t.TempDir()
	writeReview(t, run, "ACCEPT\n")
	key, _, _ := newRunner(&spawnRec{}).Run(context.Background(), "review", "", RunContext{Worktree: gitRepo(t), ArtifactDir: run})
	if key != "ACCEPT" {
		t.Errorf("key=%q", key)
	}
}

func TestVerdict_DirtyCheckOnlyForReviewState(t *testing.T) {
	wt := gitRepo(t)
	_ = os.WriteFile(filepath.Join(wt, "x"), []byte("x"), 0o644)
	key, _, err := newRunner(&spawnRec{}).Run(context.Background(), "review", "findings_to_issues", RunContext{Worktree: wt})
	if err != nil || key != "done" {
		t.Errorf("key=%q err=%v", key, err)
	}
}

// #369: the fixer key is ok only for an "ok" answer without failed.log.
func TestFixer_Key(t *testing.T) {
	for in, want := range map[string]string{"ok": "ok", "OK.": "ok", "failed": "failed", "banana": "failed", "": "failed"} {
		s := &spawnRec{result: in}
		key, _, _ := newRunner(s).Run(context.Background(), "fixer", "fixer", RunContext{})
		if key != want {
			t.Errorf("%q -> %q, want %q", in, key, want)
		}
	}
	art := t.TempDir()
	if err := os.WriteFile(filepath.Join(art, "failed.log"), []byte("tried a fetch, origin is gone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if k := fixerKey("ok", RunContext{ArtifactDir: art}); k != "failed" {
		t.Errorf("ok with failed.log -> %q, want failed", k)
	}
}

// #369: the oracle answer gives goto:<state> with a note, stop, or ask with a reason.
func TestOracle_Key(t *testing.T) {
	for in, want := range map[string]string{
		"goto:ci the fixer pushed, wait for CI": "goto:ci the fixer pushed, wait for CI",
		"`goto:code`\nmore text":                "goto:code",
		"stop.":                                 "stop",
		"ask origin is gone\nsecond line":       "ask origin is gone",
		"ASK":                                   "ask",
		"\n\nbanana split":                      "ask the oracle answer is not goto:<state>, stop or ask: banana split",
		"goto:":                                 "ask the oracle answer is not goto:<state>, stop or ask: goto:",
	} {
		s := &spawnRec{result: in}
		key, _, _ := newRunner(s).Run(context.Background(), "oracle", "recover", RunContext{})
		if key != want {
			t.Errorf("%q -> %q, want %q", in, key, want)
		}
	}
	if key, _, _ := newRunner(&spawnRec{result: "{}"}).Run(context.Background(), "oracle", "roadmap", RunContext{}); key != "done" {
		t.Errorf("roadmap oracle key = %q, want done", key)
	}
}

func stubGh(t *testing.T, script string) {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "gh"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestFetchIssueContext_DropsCommentsFromNonWriteUsers(t *testing.T) {
	stubGh(t, `case "$1" in
issue) echo '{"title":"T","body":"BODY","author":{"login":"owner"},"comments":[{"author":{"login":"alice"},"body":"GOOD"},{"author":{"login":"mallory"},"body":"EVIL"}]}';;
api) case "$2" in *owner*|*alice*) echo write;; *) echo read;; esac;;
esac`)
	out, err := fetchIssueContext(context.Background(), "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "BODY") || !strings.Contains(out, "GOOD") || strings.Contains(out, "EVIL") {
		t.Errorf("out = %q", out)
	}
}

func TestFetchIssueContext_GhErrorReturnsError(t *testing.T) {
	stubGh(t, "exit 1")
	if _, err := fetchIssueContext(context.Background(), "o/r", 1); err == nil {
		t.Fatal("want error")
	}
}

func TestSubagentRunner_NamesJobAndAddsNote(t *testing.T) {
	s := &spawnRec{}
	rc := RunContext{Worktree: t.TempDir(), Run: "20261007-102102-527", Note: "fix the conflict"}
	if _, _, err := newRunner(s).Run(context.Background(), "worker", "", rc); err != nil {
		t.Fatal(err)
	}
	sp := s.specs[0]
	if sp.Name != "20261007-102102-527/worker" {
		t.Fatalf("name = %q", sp.Name)
	}
	if !strings.Contains(sp.Task, "fix the conflict") {
		t.Fatalf("task lacks the note: %q", sp.Task)
	}
}

// #186: the role effort (or default_effort) reaches the child.
func TestSubagentRunner_PassesRoleEffort(t *testing.T) {
	s := &spawnRec{}
	r := newRunner(s)
	r.Cfg.DefaultEffort = "medium"
	r.Cfg.Roles["worker"] = flowconfig.Role{Prompt: "W", Effort: "low"}
	for _, role := range []string{"worker", "review"} {
		if _, _, err := r.Run(context.Background(), role, "", RunContext{Worktree: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
	}
	if s.specs[0].Effort != "low" || s.specs[1].Effort != "medium" {
		t.Fatalf("efforts = %q, %q", s.specs[0].Effort, s.specs[1].Effort)
	}
}

func TestSubagentRunner_URIEffortWinsInStats(t *testing.T) {
	s := &spawnRec{}
	r := newRunner(s)
	r.Cfg.Roles["worker"] = flowconfig.Role{Prompt: "W", Effort: "low"}
	r.URIEffort = func(string) string { return "xhigh" }
	var st StepStats
	if _, _, err := r.Run(context.Background(), "worker", "", RunContext{Worktree: t.TempDir(), Stats: &st}); err != nil {
		t.Fatal(err)
	}
	s.specs[0].OnDone(tools.TaskStats{})
	if st.Effort != "xhigh" {
		t.Fatalf("effort = %q", st.Effort)
	}
}

func TestStepStats_HasEffort(t *testing.T) {
	if got := stepStats("p/m", "low", tools.TaskStats{}); got.Effort != "low" {
		t.Fatalf("effort = %q", got.Effort)
	}
}
