package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/worktree"
)

// End-to-end runner tests: the REAL ExecChecker, the REAL builtin check scripts,
// a stub gh, a temp git repo with a bare origin and a FAKE AgentRunner.

const (
	e2eRepo  = "o/r"
	e2eIssue = 7
	e2ePR    = 42
)

// e2eGH is the stub gh. Check scripts get a minimal env, so the log and the
// answers live in files under CTL (baked in), not in env vars.
const e2eGH = `#!/usr/bin/env bash
CTL='%s'
echo "$*" >> "$CTL/gh.log"
if [ -e "$CTL/down" ]; then echo "gh down, token $GH_TOKEN" >&2; exit 1; fi
case "$*" in
  "issue view"*) cat "$CTL/issue.json" ;;
  "api -i"*) printf 'HTTP/2.0 200 OK\r\n\r\n{"permission":"%%s"}\n' "$(cat "$CTL/perm")" ;;
  "pr review"*) exit 0 ;;
  "api user"*) echo bot ;;
  "api --paginate"*) if [ -e "$CTL/comments.json" ]; then cat "$CTL/comments.json"; else echo '[]'; fi ;;
  "pr list"*) if [ -e "$CTL/pr" ]; then echo 42; fi ;;
  "pr create"*) touch "$CTL/pr"; echo https://example/pull/42 ;;
  "pr checks"*)
    b=$(cat "$CTL/ci_bucket")
    case "$*" in
      *--jq*) echo "$b" ;;
      *) echo "[{\"name\":\"ci-ok\",\"state\":\"x\",\"bucket\":\"$b\"}]" ;;
    esac ;;
  "pr view"*headRefOid*) git ls-remote origin "refs/heads/$(git branch --show-current)" | cut -f1 ;;
  "pr view"*mergeStateStatus*)
    if [ -e "$CTL/behind_once" ]; then rm "$CTL/behind_once"; echo BEHIND; else echo CLEAN; fi ;;
  "pr merge"*)
    [ -e "$CTL/merge_fail" ] && exit 1
    if [ -e "$CTL/merge_fail_once" ]; then rm "$CTL/merge_fail_once"; exit 1; fi
    exit 0 ;;
  *) exit 2 ;;
esac
`

func e2eGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func e2eWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// e2eCommit writes a file in dir and commits it.
func e2eCommit(t *testing.T, dir, name, content string) {
	t.Helper()
	e2eWrite(t, filepath.Join(dir, name), content)
	e2eGit(t, dir, "add", "-A")
	e2eGit(t, dir, "commit", "-qm", "add "+name)
}

// newRepo returns a work clone and a bare origin with a commit on main.
func newRepo(t *testing.T) (work, origin string) {
	t.Helper()
	root := t.TempDir()
	origin = filepath.Join(root, "origin.git")
	work = filepath.Join(root, "r")
	e2eGit(t, root, "init", "-q", "--bare", "-b", "main", origin)
	e2eGit(t, root, "clone", "-q", origin, work)
	e2eGit(t, work, "checkout", "-q", "-b", "main")
	e2eCommit(t, work, "x", "x")
	e2eGit(t, work, "push", "-q", "-u", "origin", "main")
	return work, origin
}

// FakeAgents is an AgentRunner with scripted keys. OnRun hooks run before the key
// is returned; the default worker hook makes a real commit.
type FakeAgents struct {
	Script map[string][]string
	OnRun  map[string]func(wt string)
	RunDir string
	T      *testing.T
	calls  int
}

func (f *FakeAgents) Run(_ context.Context, _, _ string, rc RunContext) (string, string, error) {
	f.calls++
	q := f.Script[rc.StateName]
	if len(q) == 0 {
		return "", "", errors.New("FakeAgents: no scripted key for " + rc.StateName)
	}
	f.Script[rc.StateName] = q[1:]
	if hook := f.OnRun[rc.StateName]; hook != nil {
		hook(rc.Worktree)
	}
	if rc.StateName == "review" {
		e2eWrite(f.T, filepath.Join(f.RunDir, "review.md"), q[0]+"\n")
	}
	return q[0], fmt.Sprintf("sess-%s-%d", rc.StateName, f.calls), nil
}

type e2eChecker struct {
	*ExecChecker
	extra []string
}

func (c e2eChecker) Run(ctx context.Context, s State, env []string, dir string) (string, CheckResult, error) {
	return c.ExecChecker.Run(ctx, s, append(env, c.extra...), dir)
}

type e2e struct {
	t             *testing.T
	home, work    string
	origin, ctl   string
	runDir        string
	st            *RunState
	wf            *Workflow
	agents        *FakeAgents
	runner        *Runner
	extraCheckEnv []string
	commitCounter int
}

// newE2E builds the repo, the worktree, the stub gh and the runner. Agents are
// scripted with script; the default worker hook commits a new file.
func newE2E(t *testing.T, script map[string][]string) *e2e {
	t.Helper()
	for _, bin := range []string{"git", "bash"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	e := &e2e{t: t, home: t.TempDir(), ctl: t.TempDir()}
	t.Setenv("HOME", e.home)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	for _, k := range []string{"GIT_AUTHOR", "GIT_COMMITTER"} {
		t.Setenv(k+"_NAME", "t")
		t.Setenv(k+"_EMAIL", "t@t")
	}
	e.work, e.origin = newRepo(t)

	gh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(gh, []byte(fmt.Sprintf(e2eGH, e.ctl)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(gh)+string(os.PathListSeparator)+os.Getenv("PATH"))
	e.ctlSet("issue.json", `{"state":"OPEN","author":{"login":"alice"},"labels":[{"name":"accepted"}]}`)
	e.ctlSet("perm", "write")
	e.ctlSet("ci_bucket", "pass")

	wt, err := worktree.AddIssue(context.Background(), e.home, e.work, e2eIssue, "main")
	if err != nil {
		t.Fatal(err)
	}
	e.wf, _, err = Lookup("issue-to-merge", e.home, "", false)
	if err != nil {
		t.Fatal(err)
	}
	runID := NewRunID(e2eIssue, time.Now())
	e.runDir = RunDir(e.home, "r", runID)
	e.st = &RunState{
		Version: 1, Run: runID, Workflow: e.wf.Name, Repo: e2eRepo, Issue: e2eIssue,
		Branch: wt.Branch, Worktree: wt.Dir, Status: "running", Current: e.wf.Start,
		StartedAt: time.Now().UTC(), Visits: map[string]int{}, History: []Step{},
	}
	e.agents = &FakeAgents{Script: script, RunDir: e.runDir, T: t, OnRun: map[string]func(string){
		"code": func(wt string) {
			e.commitCounter++
			e2eCommit(t, wt, fmt.Sprintf("work%d.txt", e.commitCounter), "w")
		},
	}}
	return e
}

func (e *e2e) ctlSet(name, content string) {
	e.t.Helper()
	e2eWrite(e.t, filepath.Join(e.ctl, name), content)
}

func (e *e2e) ghLog() string {
	b, _ := os.ReadFile(filepath.Join(e.ctl, "gh.log"))
	return string(b)
}

// run builds the runner (so tests can edit e.wf and e.extraCheckEnv first) and runs it.
func (e *e2e) run() error {
	e.t.Helper()
	runDir := e.runDir
	checks := &ExecChecker{
		DefaultTimeout: 60 * time.Second,
		Resolve: func(rel string) (string, error) {
			return ResolveCheck(rel, "", e.home, Embedded(), runDir)
		},
	}
	extra := append([]string{
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"TYCI_CI_POLL_SEC=0", "TYCI_CI_APPEAR_SEC=2",
	}, e.extraCheckEnv...)
	e.runner = &Runner{
		WF: e.wf, Checks: e2eChecker{checks, extra}, Agents: e.agents,
		Store: &Store{Dir: runDir}, RunDir: runDir, DefaultBranch: "main",
		OnSkip: removeWorktreeHook(e.work),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return e.runner.Run(ctx, e.st)
}

// mustFinish runs and requires a clean end (status done).
func (e *e2e) mustFinish() {
	e.t.Helper()
	if err := e.run(); err != nil {
		e.t.Fatalf("run: %v (status %s, reason %q, history %v)", err, e.st.Status, e.st.Reason, e.trail())
	}
	if e.st.Status != "done" {
		e.t.Fatalf("status %q, want done; history %v", e.st.Status, e.trail())
	}
}

// mustPause runs and requires ErrPaused.
func (e *e2e) mustPause() {
	e.t.Helper()
	if err := e.run(); !errors.Is(err, ErrPaused) {
		e.t.Fatalf("run err = %v, want ErrPaused (status %s, reason %q, history %v)", err, e.st.Status, e.st.Reason, e.trail())
	}
	if e.st.Status != "paused" {
		e.t.Fatalf("status %q, want paused", e.st.Status)
	}
}

// trail returns "state:key" per step.
func (e *e2e) trail() []string {
	var out []string
	for _, h := range e.st.History {
		out = append(out, h.State+":"+h.Key)
	}
	return out
}

func (e *e2e) states() string {
	var out []string
	for _, h := range e.st.History {
		out = append(out, h.State)
	}
	return strings.Join(out, ", ")
}

func (e *e2e) wantStates(want string) {
	e.t.Helper()
	if got := e.states(); got != want {
		e.t.Errorf("history = %s\nwant      %s", got, want)
	}
}

func (e *e2e) wantMerged() {
	e.t.Helper()
	sha := strings.TrimSpace(e2eGit(e.t, e.origin, "rev-parse", "refs/heads/"+e.st.Branch))
	want := fmt.Sprintf("pr merge %d -R %s --squash --delete-branch --match-head-commit %s", e2ePR, e2eRepo, sha)
	if !strings.Contains(e.ghLog(), want) {
		e.t.Errorf("gh log lacks %q:\n%s", want, e.ghLog())
	}
}

func (e *e2e) wantNoMerge() {
	e.t.Helper()
	if strings.Contains(e.ghLog(), "pr merge") {
		e.t.Errorf("unexpected gh pr merge:\n%s", e.ghLog())
	}
}

func (e *e2e) wantNoWorktree() {
	e.t.Helper()
	if _, err := os.Stat(e.st.Worktree); !os.IsNotExist(err) {
		e.t.Errorf("worktree %s should not exist (err %v)", e.st.Worktree, err)
	}
	if _, err := os.Stat(filepath.Join(e.runDir, "state.json")); err != nil {
		e.t.Errorf("state.json missing: %v", err)
	}
}

func (e *e2e) session(state string) string {
	for _, h := range e.st.History {
		if h.State == state {
			return h.Session
		}
	}
	return ""
}

// happy is the agent script of a run that merges at once.
func happy() map[string][]string {
	return map[string][]string{
		"code":     {"done"},
		"review":   {"ACCEPT", "done"},
		"findings": {"done"},
	}
}

const happyStates = "check_done, code, review, push, post_review, ci, comments, merge, findings"

func TestE2E_Gate_NoLabel_NoAgentStarted(t *testing.T) {
	e := newE2E(t, nil)
	e.ctlSet("issue.json", `{"state":"OPEN","author":{"login":"alice"},"labels":[]}`)
	e.mustFinish()
	e.wantStates("check_done")
	if e.st.History[0].To != "end" || e.agents.calls != 0 {
		t.Errorf("to = %q, agent calls = %d", e.st.History[0].To, e.agents.calls)
	}
	e.wantNoWorktree()
}

func TestE2E_Gate_NonMember_NoAgentStarted(t *testing.T) {
	e := newE2E(t, nil)
	e.ctlSet("perm", "read")
	e.mustFinish()
	e.wantStates("check_done")
	if e.st.History[0].To != "end" || e.agents.calls != 0 {
		t.Errorf("to = %q, agent calls = %d", e.st.History[0].To, e.agents.calls)
	}
	e.wantNoWorktree()
}

func TestE2E_HappyPath_Merges(t *testing.T) {
	e := newE2E(t, happy())
	e.mustFinish()
	e.wantStates(happyStates)
	if last := e.st.History[len(e.st.History)-1]; last.To != "end" {
		t.Errorf("last step to = %q", last.To)
	}
	e.wantMerged()
	e.wantNoWorktree()
	if e.st.PR != e2ePR {
		t.Errorf("PR = %d", e.st.PR)
	}
	if w, r := e.session("code"), e.session("review"); w == "" || w == r {
		t.Errorf("worker session %q and review session %q must differ", w, r)
	}
}

func TestE2E_ReviewChangesThenAccept(t *testing.T) {
	e := newE2E(t, map[string][]string{
		"code":     {"done", "done"},
		"review":   {"CHANGES", "ACCEPT", "done"},
		"findings": {"done"},
	})
	e.mustFinish()
	e.wantStates("check_done, code, review, code, review, push, post_review, ci, comments, merge, findings")
	e.wantMerged()
}

func TestE2E_RedCIThreeTimes_EndsInAsk(t *testing.T) {
	e := newE2E(t, map[string][]string{
		"code":   {"done", "done", "done"},
		"review": {"ACCEPT", "ACCEPT", "ACCEPT"},
	})
	e.ctlSet("ci_bucket", "fail")
	e.mustPause()
	if e.st.Ask == nil || e.st.Ask.Reason != "max_visits:code" {
		t.Errorf("ask = %+v", e.st.Ask)
	}
	if e.st.Visits["ci"] != 3 || e.st.Visits["code"] != 3 {
		t.Errorf("visits = %v", e.st.Visits)
	}
	e.wantNoMerge()
}

func TestE2E_Behind_RebasesThenCIThenMerge(t *testing.T) {
	e := newE2E(t, happy())
	e.ctlSet("behind_once", "")
	e2eCommit(t, e.work, "main2.txt", "m")
	e2eGit(t, e.work, "push", "-q", "origin", "main")
	e.mustFinish()
	e.wantStates("check_done, code, review, push, post_review, ci, comments, merge, rebase, ci, comments, merge, findings")
	e.wantMerged()
	if k := e.st.History[8].Key; k != "ok" {
		t.Errorf("rebase key = %q, want ok", k)
	}
	if m := strings.TrimSpace(e2eGit(t, e.origin, "log", "--format=%s", "-1", "--merges", "refs/heads/"+e.st.Branch)); m == "" {
		t.Error("origin issue branch has no merge commit of origin/main")
	}
}

func TestE2E_RebaseConflict_GoesToCode(t *testing.T) {
	e := newE2E(t, map[string][]string{
		"code":     {"done", "done"},
		"review":   {"ACCEPT", "ACCEPT", "done"},
		"findings": {"done"},
	})
	e.agents.OnRun["code"] = func(wt string) {
		e.commitCounter++
		e2eCommit(t, wt, "clash.txt", fmt.Sprintf("branch%d\n", e.commitCounter))
	}
	e.ctlSet("behind_once", "")
	e2eCommit(t, e.work, "clash.txt", "main\n")
	e2eGit(t, e.work, "push", "-q", "origin", "main")
	e.mustFinish()
	e.wantStates("check_done, code, review, push, post_review, ci, comments, merge, rebase, code, review, push, post_review, ci, comments, merge, findings")
	if k := e.st.History[8].Key; k != "conflict" {
		t.Errorf("rebase key = %q", k)
	}
}

func TestE2E_MergeFail_MergeDecisionRetry(t *testing.T) {
	s := happy()
	s["merge_decision"] = []string{"retry"}
	e := newE2E(t, s)
	e.ctlSet("merge_fail_once", "")
	e.mustFinish()
	e.wantStates("check_done, code, review, push, post_review, ci, comments, merge, merge_decision, merge, findings")
	e.wantMerged()
}

func TestE2E_MergeFail_MergeDecisionAsk(t *testing.T) {
	s := happy()
	s["merge_decision"] = []string{"ask"}
	e := newE2E(t, s)
	e.ctlSet("merge_fail", "")
	e.mustPause()
	e.wantStates("check_done, code, review, push, post_review, ci, comments, merge, merge_decision")
	if e.st.Current != "ask" {
		t.Errorf("current = %q", e.st.Current)
	}
}

func TestE2E_ProtectedPath_StopsBeforeMerge(t *testing.T) {
	e := newE2E(t, happy())
	e.agents.OnRun["code"] = func(wt string) { e2eCommit(t, wt, ".github/workflows/ci.yml", "on: push\n") }
	e.mustPause()
	e.wantStates("check_done, code, review, push, post_review, ci, comments, merge")
	if k := e.st.History[7].Key; k != "protected" {
		t.Errorf("merge key = %q", k)
	}
	e.wantNoMerge()
}

func TestE2E_PushFail_GoesToAsk(t *testing.T) {
	e := newE2E(t, map[string][]string{"code": {"done"}, "review": {"ACCEPT"}})
	// Another clone already pushed a different commit to the issue branch.
	other := filepath.Join(t.TempDir(), "other")
	e2eGit(t, filepath.Dir(other), "clone", "-q", e.origin, other)
	e2eGit(t, other, "checkout", "-q", "-b", e.st.Branch)
	e2eCommit(t, other, "other.txt", "o")
	e2eGit(t, other, "push", "-q", "origin", e.st.Branch)
	e.mustPause()
	e.wantStates("check_done, code, review, push")
	if h := e.st.History[3]; h.Key != "fail" || h.To != "ask" {
		t.Errorf("push step = %+v", h)
	}
}

func TestE2E_CITimeout_GoesToAsk(t *testing.T) {
	e := newE2E(t, map[string][]string{"code": {"done"}, "review": {"ACCEPT"}})
	e.ctlSet("ci_bucket", "pending")
	e.extraCheckEnv = []string{"TYCI_CI_POLL_SEC=0.1"}
	ci := e.wf.States["ci"]
	ci.TimeoutSec = 1
	e.wf.States["ci"] = ci
	e.mustPause()
	e.wantStates("check_done, code, review, push, post_review, ci")
	if h := e.st.History[5]; h.Key != "timeout" || h.To != "ask" {
		t.Errorf("ci step = %+v", h)
	}
}

func TestE2E_GhDown_AtGate_GoesToAsk(t *testing.T) {
	e := newE2E(t, nil)
	e.ctlSet("down", "")
	e.mustPause()
	e.wantStates("check_done")
	if e.st.History[0].To != "ask" || e.agents.calls != 0 {
		t.Errorf("to = %q, agent calls = %d", e.st.History[0].To, e.agents.calls)
	}
	if _, err := os.Stat(e.st.Worktree); err != nil {
		t.Errorf("worktree must stay when the run asks: %v", err)
	}
}

func TestE2E_StateJsonHasEveryTransition(t *testing.T) {
	e := newE2E(t, happy())
	e.mustFinish()
	st, err := Load(e.runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.History) != len(e.st.History) || len(st.History) == 0 {
		t.Fatalf("history length %d, want %d", len(st.History), len(e.st.History))
	}
	for i, h := range st.History {
		if h.Seq != i+1 {
			t.Errorf("step %d has seq %d", i, h.Seq)
		}
		next := "end"
		if i+1 < len(st.History) {
			next = st.History[i+1].State
		}
		if h.To != next {
			t.Errorf("step %d (%s) to = %q, next state %q", i, h.State, h.To, next)
		}
	}
	if st.Status != "done" || st.Current != "end" {
		t.Errorf("status %q current %q", st.Status, st.Current)
	}
}

func TestE2E_NoSecretsInStateJson(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_secret")
	e := newE2E(t, nil)
	e.ctlSet("down", "") // the stub prints $GH_TOKEN to stderr
	e.mustPause()
	b, err := os.ReadFile(filepath.Join(e.runDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ghp_secret") {
		t.Errorf("state.json contains the token:\n%s", b)
	}
	if !strings.Contains(string(b), "gh down, token ***") {
		t.Errorf("stderr tail not recorded and masked:\n%s", b)
	}
}
