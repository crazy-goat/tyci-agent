package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/trust"
	"github.com/crazy-goat/tyci-agent/internal/worktree"
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/tools"
)

var remoteRe = regexp.MustCompile(`^(?:git@github\.com:|ssh://git@github\.com/|https://github\.com/)([^/\s]+/[^/\s]+?)(?:\.git)?/?$`)

// ParseRemote returns owner/name from a GitHub ssh or https remote URL.
func ParseRemote(url string) (string, error) {
	m := remoteRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", fmt.Errorf("cannot read owner/name from remote %q", url)
	}
	return m[1], nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// DetectRepo finds the repository of the current directory: owner/name from
// origin and the default branch from refs/remotes/origin/HEAD.
func DetectRepo() (RepoInfo, error) {
	wd, err := os.Getwd()
	if err != nil {
		return RepoInfo{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return RepoInfo{}, err
	}
	root, err := gitOut(wd, "rev-parse", "--show-toplevel")
	if err != nil {
		return RepoInfo{}, errors.New("not in a git repository")
	}
	url, err := gitOut(root, "remote", "get-url", "origin")
	if err != nil {
		return RepoInfo{}, errors.New("no origin remote")
	}
	repo, err := ParseRemote(url)
	if err != nil {
		return RepoInfo{}, err
	}
	ref, err := gitOut(root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil || !strings.HasPrefix(ref, "origin/") {
		return RepoInfo{}, errors.New("default branch unknown: run `git remote set-head origin --auto`")
	}
	projectRoot, _ := session.ProjectKey(root)
	trusted, _, terr := trust.Decide(projectRoot, false, nil)
	if terr != nil {
		trusted = false
	}
	return RepoInfo{Home: home, Root: root, Repo: repo, DefaultBranch: strings.TrimPrefix(ref, "origin/"), Trusted: trusted}, nil
}

func projectDir(i RepoInfo) string {
	if i.Trusted {
		return i.Root
	}
	return ""
}

// NewManager returns the production Manager. notify receives the notices;
// spawn runs one subagent (tools.RunSubagentTask).
func NewManager(notify func(string), spawn func(context.Context, tools.TaskSpec) (string, string, error)) *Manager {
	lookup := func(info RepoInfo, name string) (*Workflow, error) {
		wf, _, err := Lookup(name, info.Home, info.Root, info.Trusted)
		return wf, err
	}
	m := &Manager{Info: DetectRepo, Workflow: lookup, Notify: notify}
	m.Prepare = func(ctx context.Context, info RepoInfo, req StartRequest) (*RunState, *Workflow, []string, error) {
		tmp, err := os.MkdirTemp("", "tyci-validate-")
		if err != nil {
			return nil, nil, nil, err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		d := PrepareDeps{
			Lookup: func(name string) (*Workflow, string, error) {
				return Lookup(name, info.Home, info.Root, info.Trusted)
			},
			Config: func() (*flowconfig.Config, error) { return flowconfig.Load(info.Home, info.Root, info.Trusted) },
			Resolve: func(rel string) (string, error) {
				return ResolveCheck(rel, projectDir(info), info.Home, Embedded(), tmp)
			},
			AddIssue: func(ctx context.Context) (*worktree.Worktree, error) {
				return worktree.AddIssue(ctx, info.Home, info.Root, req.Issue, info.DefaultBranch)
			},
			NewStore: func(runID string) (*Store, error) {
				return &Store{Dir: RunDir(info.Home, info.Name(), runID)}, nil
			},
		}
		return PrepareRun(ctx, d, PrepareReq{Workflow: req.Workflow, Repo: info.Repo, DefaultBranch: info.DefaultBranch, Issue: req.Issue})
	}
	m.NewRunner = func(info RepoInfo, wf *Workflow, st *RunState) *Runner {
		runDir := RunDir(info.Home, info.Name(), st.Run)
		cfg, err := flowconfig.Load(info.Home, info.Root, info.Trusted)
		resolve := func(rel string) (string, error) {
			return ResolveCheck(rel, projectDir(info), info.Home, Embedded(), runDir)
		}
		r := &Runner{
			WF:            wf,
			Store:         &Store{Dir: runDir},
			RunDir:        runDir,
			DefaultBranch: info.DefaultBranch,
			// The run dir with state.json stays; only the worktree goes.
			OnSkip: removeWorktreeHook(info.Root),
		}
		if err != nil {
			r.Agents = failingAgents{err}
			r.Checks = &ExecChecker{Resolve: resolve}
			return r
		}
		r.Checks = &ExecChecker{DefaultTimeout: cfg.CheckTimeout(), Resolve: resolve}
		r.Agents = NewSubagentRunner(cfg, spawn)
		return r
	}
	m.Text = func(ctx context.Context, info RepoInfo, wf *Workflow, input string) (string, error) {
		s, ok := wf.States[wf.Start]
		if !ok || s.Agent == "" {
			return "", fmt.Errorf("workflow %q must start with an agent state", wf.Name)
		}
		cfg, err := flowconfig.Load(info.Home, info.Root, info.Trusted)
		if err != nil {
			return "", err
		}
		out, _, err := NewSubagentRunner(cfg, spawn).Text(ctx, s.Agent, s.Task, RunContext{
			Repo: info.Repo, DefaultBranch: info.DefaultBranch, Worktree: info.Root, Input: input,
		})
		return out, err
	}
	return m
}

type failingAgents struct{ err error }

func (f failingAgents) Run(context.Context, string, string, RunContext) (string, string, error) {
	return "", "", f.err
}

// ChatTools adapts a Manager to tools.WorkflowManager.
type ChatTools struct{ M *Manager }

// Start implements tools.WorkflowManager.
func (c ChatTools) Start(ctx context.Context, workflow string, issue int) (string, []string, error) {
	return c.M.Start(ctx, StartRequest{Workflow: workflow, Issue: issue})
}

// Resume implements tools.WorkflowManager.
func (c ChatTools) Resume(run, answer string) error { return c.M.Resume(run, answer) }

// Status implements tools.WorkflowManager. It returns a JSON-ready summary.
func (c ChatTools) Status(run string) (any, error) {
	st, err := c.M.Status(run)
	if err != nil {
		return nil, err
	}
	hist := st.History
	if len(hist) > 5 {
		hist = hist[len(hist)-5:]
	}
	type step struct {
		State string `json:"state"`
		Key   string `json:"key"`
		To    string `json:"to"`
	}
	steps := make([]step, 0, len(hist))
	for _, h := range hist {
		steps = append(steps, step{h.State, h.Key, h.To})
	}
	out := map[string]any{
		"run": st.Run, "status": st.Status, "state": st.Current, "issue": st.Issue,
		"visits": st.Visits, "history": steps, "updated_at": st.UpdatedAt.Format(time.RFC3339),
	}
	if st.PR > 0 {
		out["pr"] = st.PR
		out["pr_url"] = prURL(st)
	}
	if st.Reason != "" {
		out["reason"] = st.Reason
	}
	if st.Ask != nil {
		out["ask"] = st.Ask.Message
	}
	return out, nil
}

// removeWorktreeHook returns the OnSkip hook (skipped or merged run): it removes the issue worktree
// and its branch. The run dir with state.json stays.
func removeWorktreeHook(root string) func(*RunState) {
	return func(st *RunState) {
		_ = worktree.ForIssue(root, st.Worktree, st.Branch).Remove(context.Background())
	}
}
