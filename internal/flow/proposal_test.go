package flow

import (
	"context"
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

// proposingFixer writes a proposal into its artifact dir and answers "failed".
type proposingFixer struct{ patch string }

func (f proposingFixer) Run(_ context.Context, _, _ string, rc RunContext) (string, string, error) {
	_ = os.WriteFile(filepath.Join(rc.ArtifactDir, "proposal.md"), []byte("# Handle non-fast-forward\n\nwhy\n"), 0o600)
	_ = os.WriteFile(filepath.Join(rc.ArtifactDir, "proposal.patch"), []byte(f.patch), 0o600)
	return "failed", "", nil
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
