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
  "pr review"*) cat "${@: -1}" > "$CTL/review_body" ;;
  "api user"*) echo bot ;;
  "api --paginate"*) if [ -e "$CTL/comments.json" ]; then cat "$CTL/comments.json"; else echo '[]'; fi ;;
  "pr list"*)
    case "$*" in
      *--jq*) if [ -e "$CTL/pr" ]; then echo 42; fi ;;
      *) if [ -e "$CTL/pr" ]; then echo '[{"number":42,"headRefName":"issue-7","isCrossRepository":false,"closingIssuesReferences":[{"number":7}]}]'; else echo '[]'; fi ;;
    esac ;;
  "pr create"*) touch "$CTL/pr"; echo https://example/pull/42 ;;
  "pr checks"*)
    b=$(cat "$CTL/ci_bucket")
    case "$*" in
      *--jq*) echo "$b" ;;
      *) echo "[{\"name\":\"ci-ok\",\"state\":\"x\",\"bucket\":\"$b\"}]" ;;
    esac ;;
  "pr view"*headRefOid*) git ls-remote origin "refs/heads/$(git branch --show-current)" | cut -f1 ;;
  "pr view"*mergeable*) echo "MERGEABLE CLEAN" ;;
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
	if rc.ArtifactDir != "" {
		e2eWrite(f.T, filepath.Join(rc.ArtifactDir, "report.md"), q[0]+"\n")
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
	agentRunner   AgentRunner // replaces agents when set
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
	e.runner = e.newRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return e.runner.Run(ctx, e.st)
}

// newRunner builds a runner with the real checks and the fake agents.
func (e *e2e) newRunner() *Runner {
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
	var agents AgentRunner = e.agents
	if e.agentRunner != nil {
		agents = e.agentRunner
	}
	return &Runner{
		WF: e.wf, Checks: e2eChecker{checks, extra}, Agents: agents,
		Store: &Store{Dir: runDir}, RunDir: runDir, DefaultBranch: "main",
		OnSkip: removeWorktreeHook(e.work),
	}
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

const happyStates = "check_done, open_pr, code, review, lock, update, post_review, ci, comments, merge, findings"

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
	var want []string
	for _, h := range e.st.History {
		if h.Kind == "ask" {
			continue
		}
		name := fmt.Sprintf("%03d-%s", h.Seq, h.State)
		if h.Artifact != name {
			t.Errorf("step %d artifact = %q, want %q", h.Seq, h.Artifact, name)
		}
		want = append(want, name)
	}
	if got := artifactDirs(t, e.runDir); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("artifact dirs = %v, want %v", got, want)
	}
}

func TestE2E_ReviewChangesThenAccept(t *testing.T) {
	e := newE2E(t, map[string][]string{
		"code":     {"done", "done"},
		"review":   {"CHANGES", "ACCEPT", "done"},
		"findings": {"done"},
	})
	e.mustFinish()
	e.wantStates("check_done, open_pr, code, review, code, review, lock, update, post_review, ci, comments, merge, findings")
	e.wantMerged()
	// #340: post_review.sh posts the newest review report.
	body, _ := os.ReadFile(filepath.Join(e.ctl, "review_body"))
	if !strings.Contains(string(body), "ACCEPT") || strings.Contains(string(body), "CHANGES") {
		t.Errorf("posted review = %q", body)
	}
}

// #356: the review state may have any name; post_review.sh posts the report of
// the review step, not of a state called review.
func TestE2E_ReviewStateWithOtherName_PostsReview(t *testing.T) {
	e := newE2E(t, map[string][]string{
		"code":     {"done"},
		"judge":    {"ACCEPT"},
		"findings": {"done"},
	})
	renameState(e.wf, "review", "judge")
	e.mustFinish()
	e.wantStates("check_done, open_pr, code, judge, lock, update, post_review, ci, comments, merge, findings")
	e.wantMerged()
	body, _ := os.ReadFile(filepath.Join(e.ctl, "review_body"))
	if !strings.Contains(string(body), "ACCEPT") {
		t.Errorf("posted review = %q", body)
	}
}

// renameState renames a state of wf and every transition that points to it.
func renameState(wf *Workflow, from, to string) {
	wf.States[to] = wf.States[from]
	delete(wf.States, from)
	for name, s := range wf.States {
		for key, next := range s.On {
			if next == from {
				s.On[key] = to
			}
		}
		wf.States[name] = s
	}
	if wf.Start == from {
		wf.Start = to
	}
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

func TestE2E_MainMovedBeforeLock_UpdatesOnceThenOneCIRound(t *testing.T) {
	e := newE2E(t, happy())
	e2eCommit(t, e.work, "main2.txt", "m")
	e2eGit(t, e.work, "push", "-q", "origin", "main")
	e.mustFinish()
	e.wantStates(happyStates)
	e.wantMerged()
	if e.st.Visits["ci"] != 1 {
		t.Errorf("ci visits = %d, want 1", e.st.Visits["ci"])
	}
}

func TestE2E_Behind_RebasesThenCIThenMerge(t *testing.T) {
	e := newE2E(t, happy())
	e.ctlSet("behind_once", "")
	e2eCommit(t, e.work, "main2.txt", "m")
	e2eGit(t, e.work, "push", "-q", "origin", "main")
	e.mustFinish()
	e.wantStates("check_done, open_pr, code, review, lock, update, post_review, ci, comments, merge, rebase, ci, comments, merge, findings")
	e.wantMerged()
	if k := e.st.History[10].Key; k != "ok" {
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
		content := fmt.Sprintf("branch%d\n", e.commitCounter)
		if e.commitCounter > 1 {
			content = "main\n" // the coder resolves the clash: same text as main
		}
		e2eCommit(t, wt, "clash.txt", content)
	}
	e2eCommit(t, e.work, "clash.txt", "main\n")
	e2eGit(t, e.work, "push", "-q", "origin", "main")
	e.mustFinish()
	e.wantStates("check_done, open_pr, code, review, lock, update, code, review, lock, update, post_review, ci, comments, merge, findings")
	if k := e.st.History[5].Key; k != "conflict" {
		t.Errorf("rebase key = %q", k)
	}
}

const toMerge = "check_done, open_pr, code, review, lock, update, post_review, ci, comments, merge"

// #369: a failed merge goes to the fixer; "ok" runs the same step again.
func TestE2E_MergeFail_FixerOkRunsMergeAgain(t *testing.T) {
	s := happy()
	s["fixer"] = []string{"ok"}
	e := newE2E(t, s)
	e.ctlSet("merge_fail_once", "")
	e.mustFinish()
	e.wantStates(toMerge + ", fixer, merge, findings")
	e.wantMerged()
	if h := e.st.History[10]; h.Role != "fixer" || h.To != "merge" {
		t.Errorf("fixer step = %+v", h)
	}
}

// #369: the fixer fails, the oracle sends the run to ci with a note.
func TestE2E_FixerFailed_OracleGotoCI(t *testing.T) {
	s := happy()
	s["fixer"] = []string{"failed"}
	s["oracle"] = []string{"goto:ci the PR head moved, wait for CI again"}
	e := newE2E(t, s)
	e.ctlSet("merge_fail_once", "")
	e.mustFinish()
	e.wantStates(toMerge + ", fixer, oracle, ci, comments, merge, findings")
	e.wantMerged()
	if h := e.st.History[11]; h.Key != "goto:ci" || h.To != "ci" || h.Note != "the PR head moved, wait for CI again" {
		t.Errorf("oracle step = %+v", h)
	}
}

// #369: the oracle answers ask; the run pauses with the oracle's reason.
func TestE2E_OracleAsk_PausesWithReason(t *testing.T) {
	s := happy()
	s["fixer"] = []string{"failed"}
	s["oracle"] = []string{"ask branch protection rejects the merge; a human must check the ruleset"}
	e := newE2E(t, s)
	e.ctlSet("merge_fail", "")
	e.mustPause()
	e.wantStates(toMerge + ", fixer, oracle")
	reason := "branch protection rejects the merge; a human must check the ruleset"
	if e.st.Current != "ask" || e.st.Ask == nil || e.st.Ask.Reason != reason || !strings.Contains(e.st.Ask.Message, "(oracle: "+reason+")") {
		t.Errorf("current %q, ask %+v", e.st.Current, e.st.Ask)
	}
}

// #369: a step that keeps failing gets the fixer twice, then the oracle (once), then a human.
func TestE2E_StepKeepsFailing_FixerTwiceOracleOnceThenAsk(t *testing.T) {
	s := happy()
	s["fixer"] = []string{"ok", "ok"}
	s["oracle"] = []string{"goto:merge try once more"}
	e := newE2E(t, s)
	e.ctlSet("merge_fail", "")
	e.mustPause()
	e.wantStates(toMerge + ", fixer, merge, fixer, merge, fixer, oracle, merge, fixer, oracle")
	if e.agents.calls != 2+2+1 { // code, review, two fixer runs, one oracle run
		t.Errorf("agent calls = %d, want 5", e.agents.calls)
	}
	if e.st.Ask == nil || !strings.Contains(e.st.Ask.Reason, "oracle already ran 1 time(s) for the failed step merge") {
		t.Errorf("ask = %+v", e.st.Ask)
	}
}

func TestE2E_ProtectedPath_StopsBeforeMerge(t *testing.T) {
	e := newE2E(t, happy())
	e.agents.OnRun["code"] = func(wt string) { e2eCommit(t, wt, ".github/workflows/ci.yml", "on: push\n") }
	e.mustPause()
	e.wantStates("check_done, open_pr, code, review, lock, update, post_review, ci, comments, merge")
	if k := e.st.History[9].Key; k != "protected" {
		t.Errorf("merge key = %q", k)
	}
	e.wantNoMerge()
}

// #369: update is rejected (non-fast-forward: an earlier run pushed the branch). The
// fixer follows the SUGGESTED line of the block: it resets to the PR branch and merges
// main. update runs again and the run merges.
func TestE2E_PushFail_FixerResetsToPRBranch(t *testing.T) {
	s := happy()
	s["fixer"] = []string{"ok"}
	e := newE2E(t, s)
	// Another clone already pushed a different commit to the issue branch.
	other := filepath.Join(t.TempDir(), "other")
	e2eGit(t, filepath.Dir(other), "clone", "-q", e.origin, other)
	e2eGit(t, other, "checkout", "-q", "-b", e.st.Branch)
	e2eCommit(t, other, "other.txt", "o")
	e2eGit(t, other, "push", "-q", "origin", e.st.Branch)
	e.agents.OnRun["fixer"] = func(wt string) {
		out, _ := os.ReadFile(filepath.Join(e.runDir, "artifacts", "006-update", "output.log"))
		if !strings.Contains(string(out), "SUGGESTED: compare") || !strings.Contains(string(out), "git reset --hard origin/"+e.st.Branch) {
			t.Errorf("update output.log has no SUGGESTED line:\n%s", out)
		}
		e2eGit(t, wt, "fetch", "-q", "origin", e.st.Branch)
		e2eGit(t, wt, "reset", "-q", "--hard", "FETCH_HEAD")
		e2eGit(t, wt, "merge", "-q", "origin/main")
	}
	e.mustFinish()
	e.wantStates("check_done, open_pr, code, review, lock, update, fixer, update, post_review, ci, comments, merge, findings")
	e.wantMerged()
	if h := e.st.History[5]; h.Key != "fail" || h.To != "fixer" {
		t.Errorf("update step = %+v", h)
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
	e.wantStates("check_done, open_pr, code, review, lock, update, post_review, ci")
	if h := e.st.History[7]; h.Key != "timeout" || h.To != "ask" {
		t.Errorf("ci step = %+v", h)
	}
}

func TestE2E_GhDown_AtGate_FixerThenAsk(t *testing.T) {
	e := newE2E(t, map[string][]string{"fixer": {"failed"}, "oracle": {"ask gh is down"}})
	e.ctlSet("down", "")
	e.mustPause()
	e.wantStates("check_done, fixer, oracle")
	if e.st.History[0].To != "fixer" || e.agents.calls != 2 {
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

// earlierPR simulates an earlier run that stopped before the merge: the issue branch on
// origin has a commit that changes name, and its PR is still open.
func (e *e2e) earlierPR(name, content string) {
	e.t.Helper()
	other := filepath.Join(e.t.TempDir(), "other")
	e2eGit(e.t, filepath.Dir(other), "clone", "-q", e.origin, other)
	e2eGit(e.t, other, "checkout", "-q", "-b", e.st.Branch)
	e2eCommit(e.t, other, name, content)
	e2eGit(e.t, other, "push", "-q", "origin", e.st.Branch)
	e.ctlSet("pr", "")
}

// #368: a new run for an issue with an open PR continues that PR and does not code again.
func TestE2E_OpenPR_ContinuesWithoutCode(t *testing.T) {
	e := newE2E(t, map[string][]string{"findings": {"done"}})
	e.earlierPR("earlier.txt", "e")
	prHead := e2eGit(t, e.origin, "rev-parse", "refs/heads/"+e.st.Branch)
	e2eCommit(t, e.work, "main2.txt", "m")
	e2eGit(t, e.work, "push", "-q", "origin", "main")
	e.mustFinish()
	e.wantStates("check_done, open_pr, lock, update, post_review, ci, comments, merge, findings")
	e.wantMerged()
	if e.st.Visits["code"] != 0 || e.st.PR != e2ePR {
		t.Errorf("code visits = %d, PR = %d", e.st.Visits["code"], e.st.PR)
	}
	if k := e.st.History[4].Key; k != "skip" {
		t.Errorf("post_review key = %q, want skip", k)
	}
	if err := exec.Command("git", "-C", e.origin, "merge-base", "--is-ancestor", prHead, "refs/heads/"+e.st.Branch).Run(); err != nil {
		t.Errorf("the pushed branch does not contain the earlier PR head: %v", err)
	}
}

// #368: an open PR whose only conflict with main is CHANGELOG.md is merged without a human.
func TestE2E_OpenPR_ChangelogConflictMerges(t *testing.T) {
	e := newE2E(t, map[string][]string{"findings": {"done"}})
	e2eCommit(t, e.work, "CHANGELOG.md", "# Changelog\n\nold\n")
	e2eGit(t, e.work, "push", "-q", "origin", "main")
	e.earlierPR("CHANGELOG.md", "# Changelog\nmine\n\nold\n")
	e2eCommit(t, e.work, "CHANGELOG.md", "# Changelog\ntheirs\n\nold\n")
	e2eGit(t, e.work, "push", "-q", "origin", "main")
	e.mustFinish()
	e.wantStates("check_done, open_pr, lock, update, post_review, ci, comments, merge, findings")
	e.wantMerged()
	got := e2eGit(t, e.origin, "show", "refs/heads/"+e.st.Branch+":CHANGELOG.md")
	if !strings.Contains(got, "mine") || !strings.Contains(got, "theirs") {
		t.Errorf("CHANGELOG.md on the PR branch = %q, want both entries", got)
	}
}

// #368: red CI on the continued PR goes to code with the run so far, then merges.
func TestE2E_OpenPR_RedCIGoesToCode(t *testing.T) {
	e := newE2E(t, map[string][]string{"code": {"done"}, "review": {"ACCEPT"}, "findings": {"done"}})
	e.earlierPR("earlier.txt", "e")
	e.ctlSet("ci_bucket", "fail")
	e.agents.OnRun["code"] = func(wt string) {
		e2eCommit(t, wt, "fix.txt", "f")
		e.ctlSet("ci_bucket", "pass")
	}
	e.mustFinish()
	e.wantStates("check_done, open_pr, lock, update, post_review, ci, code, review, lock, update, post_review, ci, comments, merge, findings")
	e.wantMerged()
	files := e2eGit(t, e.origin, "ls-tree", "--name-only", "refs/heads/"+e.st.Branch)
	if !strings.Contains(files, "earlier.txt") || !strings.Contains(files, "fix.txt") {
		t.Errorf("PR branch files = %q, want the earlier work and the fix", files)
	}
}
