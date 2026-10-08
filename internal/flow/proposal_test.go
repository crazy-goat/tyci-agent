package flow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

const goodPatch = `diff --git a/.tyci/checks/extra.sh b/.tyci/checks/extra.sh
new file mode 100644
--- /dev/null
+++ b/.tyci/checks/extra.sh
@@ -0,0 +1 @@
+echo ok
`

const outsidePatch = `diff --git a/main.go b/main.go
new file mode 100644
--- /dev/null
+++ b/main.go
@@ -0,0 +1 @@
+package main
`

// renamePatch moves the repo file x (outside .tyci/) into .tyci/.
const renamePatch = `diff --git a/x b/.tyci/x
similarity index 100%
rename from x
rename to .tyci/x
`

// writeProposal writes a proposal.md and a proposal.patch into dir.
func writeProposal(dir, patch string) {
	_ = os.WriteFile(filepath.Join(dir, "proposal.md"), []byte("# Handle non-fast-forward\n\nwhy\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "proposal.patch"), []byte(patch), 0o600)
}

// proposingFixer writes a proposal into its artifact dir and answers "failed".
type proposingFixer struct{ patch string }

func (f proposingFixer) Run(_ context.Context, _, _ string, rc RunContext) (string, string, error) {
	writeProposal(rc.ArtifactDir, f.patch)
	return "failed", "", nil
}

// okFixer writes a proposal and answers "ok": the run goes on without a pause.
type okFixer struct{ patch string }

func (f okFixer) Run(_ context.Context, _, _ string, rc RunContext) (string, string, error) {
	writeProposal(rc.ArtifactDir, f.patch)
	return "ok", "", nil
}

func proposalWF() *Workflow {
	return &Workflow{Name: "issue-to-merge", Start: "fixer", States: map[string]State{
		"fixer": {Agent: "fixer", On: map[string]string{"default": "ask"}},
		"ask":   {Ask: "Need a decision.", On: map[string]string{"retry": "fixer", "stop": "end"}},
		"end":   {End: true},
	}}
}

type proposalEnv struct {
	m       *Manager
	info    RepoInfo
	origin  string
	ghLog   string
	notices chan string
}

func newProposalEnv(t *testing.T, patch string) *proposalEnv {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	work, origin := newRepo(t)
	e := &proposalEnv{origin: origin, notices: make(chan string, 8),
		info: RepoInfo{Home: t.TempDir(), Root: work, Repo: "o/r", DefaultBranch: "main", Trusted: true}}
	e.ghLog = testutil.StubGH(t, `case "$*" in "pr create"*) echo https://example/pull/9 ;; *) exit 2 ;; esac`)
	runDir := func(id string) string { return RunDir(e.info.Home, "r", id) }
	e.m = &Manager{
		Info:   func() (RepoInfo, error) { return e.info, nil },
		Notify: func(s string) { e.notices <- s },
		Prepare: func(_ context.Context, _ RepoInfo, req StartRequest) (*RunState, *Workflow, []string, error) {
			id := NewRunID(req.Issue, time.Now())
			st := &RunState{Version: 1, Run: id, Workflow: "issue-to-merge", Repo: "o/r", Issue: req.Issue,
				Status: "running", Current: "fixer", Visits: map[string]int{}, History: []Step{}}
			return st, proposalWF(), nil, (&Store{Dir: runDir(id)}).Save(st)
		},
		Workflow: func(RepoInfo, string) (*Workflow, error) { return proposalWF(), nil },
		NewRunner: func(_ RepoInfo, wf *Workflow, st *RunState) *Runner {
			return &Runner{WF: wf, Agents: proposingFixer{patch}, Store: &Store{Dir: runDir(st.Run)}, RunDir: runDir(st.Run)}
		},
	}
	return e
}

func (e *proposalEnv) notice(t *testing.T) string {
	t.Helper()
	select {
	case s := <-e.notices:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("no notice")
		return ""
	}
}

func (e *proposalEnv) start(t *testing.T) string {
	t.Helper()
	id, _, err := e.m.Start(context.Background(), StartRequest{Issue: 3})
	if err != nil {
		t.Fatal(err)
	}
	got := e.notice(t)
	if !strings.Contains(got, "A workflow proposal waits: Handle non-fast-forward") || !strings.Contains(got, "apply|reject|retry|stop") {
		t.Fatalf("notice = %q", got)
	}
	waitIdle(t, e.m)
	return id
}

// assertUntouched checks that the user checkout has no .tyci/ and no changes.
func (e *proposalEnv) assertUntouched(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(e.info.Root, ".tyci")); err == nil {
		t.Fatal(".tyci/ appeared in the checkout")
	}
	if s := e2eGit(t, e.info.Root, "status", "--porcelain"); s != "" {
		t.Fatalf("checkout changed: %s", s)
	}
}

func TestProposal_StatusShowsItAndApplyOpensPR(t *testing.T) {
	e := newProposalEnv(t, goodPatch)
	id := e.start(t)
	e.assertUntouched(t)

	out, err := ChatTools{e.m}.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := out.(map[string]any)["proposal"].(map[string]any)
	if p == nil || !strings.Contains(p["patch"].(string), "extra.sh") || !strings.Contains(p["summary"].(string), "non-fast-forward") {
		t.Fatalf("status proposal = %v", out)
	}

	if err := e.m.Resume(id, "apply"); err != nil {
		t.Fatal(err)
	}
	if got := e.notice(t); !strings.Contains(got, "Workflow proposal applied: https://example/pull/9") || strings.Contains(got, "apply|") {
		t.Fatalf("notice = %q", got)
	}
	e.assertUntouched(t)
	branches := e2eGit(t, e.origin, "branch", "--list", "tyci/proposal-*")
	if branches == "" {
		t.Fatal("no proposal branch on origin")
	}
	branch := strings.TrimSpace(strings.TrimPrefix(branches, "*"))
	names := e2eGit(t, e.origin, "diff", "--name-only", "main", branch)
	for _, n := range strings.Split(names, "\n") {
		if !strings.HasPrefix(n, ".tyci/") {
			t.Fatalf("change outside .tyci/: %s", n)
		}
	}
	if !strings.Contains(names, ".tyci/checks/extra.sh") || !strings.Contains(names, ".tyci/workflows/issue-to-merge.json") {
		t.Fatalf("branch changes = %s", names)
	}
	if b, _ := os.ReadFile(e.ghLog); !strings.Contains(string(b), "pr create -R o/r --base main --head "+branch) {
		t.Fatalf("gh log = %s", b)
	}
	st, _ := e.m.Status(id)
	if st.Status != "paused" || st.Ask.Proposal != "" {
		t.Fatalf("state = %s, proposal %q", st.Status, st.Ask.Proposal)
	}
	if err := e.m.Resume(id, "apply"); err == nil {
		t.Fatal("apply accepted twice")
	}
}

func TestProposal_RejectChangesNothingAndHidesIt(t *testing.T) {
	e := newProposalEnv(t, goodPatch)
	id := e.start(t)
	if err := e.m.Resume(id, "reject"); err != nil {
		t.Fatal(err)
	}
	if got := e.notice(t); !strings.Contains(got, "Workflow proposal rejected.") {
		t.Fatalf("notice = %q", got)
	}
	e.assertUntouched(t)
	if b := e2eGit(t, e.origin, "branch", "--list", "tyci/*"); b != "" {
		t.Fatalf("branch pushed: %s", b)
	}
	if b, _ := os.ReadFile(e.ghLog); len(b) != 0 {
		t.Fatalf("gh called: %s", b)
	}
	// The same proposal is not shown again.
	if err := e.m.Resume(id, "retry"); err != nil {
		t.Fatal(err)
	}
	if got := e.notice(t); strings.Contains(got, "proposal") {
		t.Fatalf("rejected proposal shown again: %q", got)
	}
}

func TestProposal_ApplyRefusesFilesOutsideTyci(t *testing.T) {
	e := newProposalEnv(t, outsidePatch)
	id := e.start(t)
	err := e.m.Resume(id, "apply")
	if err == nil || !strings.Contains(err.Error(), "outside .tyci/") {
		t.Fatalf("err = %v", err)
	}
	e.assertUntouched(t)
	if b := e2eGit(t, e.origin, "branch", "--list", "tyci/*"); b != "" {
		t.Fatalf("branch pushed: %s", b)
	}
	if b := e2eGit(t, e.info.Root, "branch", "--list", "tyci/*"); b != "" {
		t.Fatalf("local branch left: %s", b)
	}
	if st, _ := e.m.Status(id); st.Ask.Proposal == "" {
		t.Fatal("failed apply dropped the proposal")
	}
}

func TestProposal_ApplyRefusesRenameFromOutsideTyci(t *testing.T) {
	e := newProposalEnv(t, renamePatch)
	id := e.start(t)
	err := e.m.Resume(id, "apply")
	if err == nil || !strings.Contains(err.Error(), "changes x, outside .tyci/") {
		t.Fatalf("err = %v", err)
	}
	e.assertUntouched(t)
	if b := e2eGit(t, e.origin, "branch", "--list", "tyci/*"); b != "" {
		t.Fatalf("branch pushed: %s", b)
	}
}

func TestProposal_ApplyKeepsLocalOverrides(t *testing.T) {
	e := newProposalEnv(t, goodPatch)
	e2eCommit(t, e.info.Root, ".tyci/checks/merge.sh", "mine\n")
	e2eWrite(t, filepath.Join(e.info.Root, ".tyci", "config.json"), `{"roles":{"worker":{"prompt":"custom"}}}`)
	e2eGit(t, e.info.Root, "add", "-A")
	e2eGit(t, e.info.Root, "commit", "-q", "-m", "overrides")
	e2eGit(t, e.info.Root, "push", "-q", "origin", "main")
	id := e.start(t)
	if err := e.m.Resume(id, "apply"); err != nil {
		t.Fatal(err)
	}
	if got := e.notice(t); !strings.Contains(got, "Workflow proposal applied") {
		t.Fatalf("notice = %q", got)
	}
	branch := strings.TrimSpace(e2eGit(t, e.origin, "branch", "--list", "tyci/proposal-*"))
	if got := e2eGit(t, e.origin, "show", branch+":.tyci/checks/merge.sh"); got != "mine" {
		t.Fatalf("merge.sh = %q", got)
	}
	if got := e2eGit(t, e.origin, "show", branch+":.tyci/config.json"); !strings.Contains(got, `"custom"`) {
		t.Fatalf("config.json = %s", got)
	}
	names := e2eGit(t, e.origin, "diff", "--name-only", "main", branch)
	if !strings.Contains(names, ".tyci/workflows/issue-to-merge.json") || strings.Contains(names, "merge.sh") {
		t.Fatalf("branch changes = %s", names)
	}
}

func TestProposal_GHFailureRemovesPushedBranch(t *testing.T) {
	e := newProposalEnv(t, goodPatch)
	testutil.StubGH(t, `exit 1`)
	id := e.start(t)
	if err := e.m.Resume(id, "apply"); err == nil || !strings.Contains(err.Error(), "gh pr create") {
		t.Fatalf("err = %v", err)
	}
	if b := e2eGit(t, e.origin, "branch", "--list", "tyci/*"); b != "" {
		t.Fatalf("branch left on origin: %s", b)
	}
}

func TestProposal_ApplyRefusesHomeWorkflow(t *testing.T) {
	e := newProposalEnv(t, goodPatch)
	id := e.start(t)
	e.m.Workflow = func(RepoInfo, string) (*Workflow, error) {
		wf := proposalWF()
		wf.Source = filepath.Join(e.info.Home, ".tyci", "workflows", "issue-to-merge.json")
		return wf, nil
	}
	if err := e.m.Resume(id, "apply"); err == nil || !strings.Contains(err.Error(), "outside the repository") {
		t.Fatalf("err = %v", err)
	}
	if b := e2eGit(t, e.origin, "branch", "--list", "tyci/*"); b != "" {
		t.Fatalf("branch pushed: %s", b)
	}
}

func TestProposal_ApplyReservesTheRun(t *testing.T) {
	e := newProposalEnv(t, goodPatch)
	gate := filepath.Join(t.TempDir(), "go")
	testutil.StubGH(t, `while [ ! -e "`+gate+`" ]; do sleep 0.05; done; echo https://example/pull/9`)
	id := e.start(t)
	done := make(chan error, 1)
	go func() { done <- e.m.Resume(id, "apply") }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.m.mu.Lock()
		_, busy := e.m.active[id]
		e.m.mu.Unlock()
		if busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("apply did not reserve the run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := e.m.Resume(id, "retry"); !errors.Is(err, ErrBusy) {
		t.Fatalf("retry during apply: %v", err)
	}
	e2eWrite(t, gate, "")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitIdle(t, e.m)
	if st, _ := e.m.Status(id); st.Status != "paused" || st.Ask.Proposal != "" {
		t.Fatalf("state = %s, proposal %q", st.Status, st.Ask.Proposal)
	}
}

// runDoneWithProposal runs a workflow whose fixer answers ok and then ends.
func runDoneWithProposal(t *testing.T, runDir string) (*RunState, []string) {
	t.Helper()
	wf := &Workflow{Name: "issue-to-merge", Start: "fixer", States: map[string]State{
		"fixer": {Agent: "fixer", On: map[string]string{"ok": "end"}},
		"end":   {End: true},
	}}
	var notices []string
	r := &Runner{WF: wf, Agents: okFixer{goodPatch}, Store: &Store{Dir: runDir}, RunDir: runDir,
		Warn: func(s string) { notices = append(notices, s) }}
	st := &RunState{Version: 1, Run: "r1", Workflow: wf.Name, Repo: "o/r", Issue: 3,
		Status: "running", Current: "fixer", Visits: map[string]int{}, History: []Step{}}
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	return st, notices
}

// A fixer that answers ok ends the run without a pause; the done notice still
// names its proposal.
func TestProposal_DoneRunNamesPendingProposal(t *testing.T) {
	st, notices := runDoneWithProposal(t, filepath.Join(t.TempDir(), "r1"))
	if st.Status != "done" || len(notices) != 1 || !strings.Contains(notices[0], "Handle non-fast-forward") {
		t.Fatalf("status = %s, notices = %q", st.Status, notices)
	}
}

// A rejected proposal is not named when the run ends.
func TestProposal_DoneRunSkipsRejectedProposal(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "r1")
	st, _ := runDoneWithProposal(t, runDir)
	if err := RejectProposal(runDir, filepath.Join(runDir, "artifacts", st.History[0].Artifact)); err != nil {
		t.Fatal(err)
	}
	if _, notices := runDoneWithProposal(t, runDir); len(notices) != 0 {
		t.Fatalf("rejected proposal named: %q", notices)
	}
}

// A fixer that answers ok and then a failing step fails the run; the failed
// run still names the proposal.
func TestProposal_FailedRunNamesPendingProposal(t *testing.T) {
	cases := []struct {
		name   string
		checks *fakeChecks
	}{
		{"unknown key", &fakeChecks{keys: map[string][]string{"c.sh": {"bad"}}}},
		{"check error", &fakeChecks{errs: map[string]error{"c.sh": errors.New("exec failed")}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runDir := filepath.Join(t.TempDir(), "r1")
			wf := &Workflow{Name: "issue-to-merge", Start: "fixer", States: map[string]State{
				"fixer": {Agent: "fixer", On: map[string]string{"ok": "check"}},
				"check": {Check: "c.sh", On: map[string]string{"ok": "end"}},
				"end":   {End: true},
			}}
			var notices []string
			r := &Runner{WF: wf, Agents: okFixer{goodPatch}, Checks: c.checks, Store: &Store{Dir: runDir}, RunDir: runDir,
				Warn: func(s string) { notices = append(notices, s) }}
			st := &RunState{Version: 1, Run: "r1", Workflow: wf.Name, Repo: "o/r", Issue: 3,
				Status: "running", Current: "fixer", Visits: map[string]int{}, History: []Step{}}
			if err := r.Run(context.Background(), st); err == nil {
				t.Fatal("run did not fail")
			}
			if st.Status != "failed" || len(notices) != 1 || !strings.Contains(notices[0], "Handle non-fast-forward") {
				t.Fatalf("status = %s, notices = %q", st.Status, notices)
			}
		})
	}
}
