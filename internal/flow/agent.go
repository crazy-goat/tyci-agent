package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/ledger"
	"github.com/crazy-goat/tyci-agent/internal/pricing"
	"github.com/crazy-goat/tyci-agent/tools"
)

// TaskRenderer turns a task template name into task text.
// TaskTemplates is the production implementation.
type TaskRenderer interface {
	Render(name string, rc RunContext) (string, error)
}

// NewSubagentRunner returns a runner that renders the embedded task templates.
func NewSubagentRunner(cfg *flowconfig.Config, spawn func(ctx context.Context, s tools.TaskSpec) (string, string, error)) *SubagentRunner {
	return &SubagentRunner{Cfg: cfg, Render: TaskTemplates{}, Spawn: spawn}
}

// SubagentRunner runs an agent state as a subagent in the run worktree.
type SubagentRunner struct {
	Cfg    *flowconfig.Config
	Render TaskRenderer
	Spawn  func(ctx context.Context, s tools.TaskSpec) (result, sessionID string, err error)
	// Warn receives warnings such as a bad verdict. Optional.
	Warn func(string)
	// IssueContext fetches the issue text for the worker. Nil means a gh fetch
	// that caches collaborator permissions for the life of this runner.
	IssueContext func(ctx context.Context, repo string, issue int) (string, error)

	once    sync.Once
	fetcher *issueFetcher
}

// Run implements AgentRunner. The key depends on the role and the task,
// not on the state name.
func (r *SubagentRunner) Run(ctx context.Context, role, task string, rc RunContext) (string, string, error) {
	out, session, err := r.Text(ctx, role, task, rc)
	if err != nil {
		return "", session, err
	}
	switch {
	case role == "review" && task == "":
		return r.verdict(ctx, rc), session, nil
	case role == "merge_decision":
		return mergeKey(out), session, nil
	}
	return "done", session, nil
}

// Text runs the agent and returns its final text.
func (r *SubagentRunner) Text(ctx context.Context, role, task string, rc RunContext) (string, string, error) {
	rl, err := r.Cfg.Role(role)
	if err != nil {
		return "", "", err
	}
	model, err := r.Cfg.ResolveModel(rl)
	if err != nil {
		return "", "", err
	}
	text := rl.Prompt
	if task != "" {
		if r.Render == nil {
			return "", "", errors.New("no task renderer")
		}
		if text, err = r.Render.Render(task, rc); err != nil {
			return "", "", err
		}
	}
	if role == "worker" {
		fetch := r.IssueContext
		if fetch == nil {
			r.once.Do(func() { r.fetcher = newIssueFetcher() })
			fetch = r.fetcher.fetch
		}
		issueText, err := fetch(ctx, rc.Repo, rc.Issue)
		if err != nil {
			return "", "", err
		}
		text += "\n\n" + issueText
		if rc.Note != "" {
			text += "\n\n## Note from the orchestrator\n\n" + rc.Note + "\n"
		}
		if b, err := os.ReadFile(filepath.Join(rc.RunDir, "comments.md")); err == nil && len(bytes.TrimSpace(b)) > 0 {
			text += "\n\n## New comments from team members on the PR\n\n" + string(b)
		}
	}
	name := ""
	if rc.Run != "" {
		name = rc.Run + "/" + role
	}
	spec := tools.TaskSpec{Task: text, Model: model, SystemPrompt: rl.Prompt, Dir: rc.Worktree, Name: name, SoftLimit: rl.CompactSoftLimit, HardLimit: rl.CompactHardLimit}
	if rc.Stats != nil {
		spec.OnDone = func(ts tools.TaskStats) { *rc.Stats = stepStats(model, ts) }
	}
	return r.Spawn(ctx, spec)
}

// stepStats converts the usage of a subagent run, priced with the model catalog.
func stepStats(model string, ts tools.TaskStats) StepStats {
	provider, name, _ := strings.Cut(model, "/")
	rates, _ := pricing.Lookup(provider, name)
	return StepStats{
		Model: model, Input: ts.Usage.Input, Output: ts.Usage.Output,
		CacheRead: ts.Usage.CacheRead, CacheWrite: ts.Usage.CacheWrite,
		CostUSD: ledger.Cost(rates, ts.Usage), Turns: ts.Turns, ToolCalls: ts.ToolCalls,
	}
}

func (r *SubagentRunner) warn(msg string) {
	if r.Warn != nil {
		r.Warn(msg)
	}
}

// verdict reads review.md, then forces CHANGES when the reviewer left the worktree dirty.
func (r *SubagentRunner) verdict(ctx context.Context, rc RunContext) string {
	v, warning := readVerdict(rc.RunDir)
	if warning != "" {
		r.warn(warning)
	}
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = rc.Worktree
	out, err := cmd.Output()
	if err != nil {
		r.warn("git status failed: " + err.Error())
		return "CHANGES"
	}
	if len(bytes.TrimSpace(out)) > 0 {
		r.warn("reviewer modified the worktree")
		return "CHANGES"
	}
	return v
}

// readVerdict reads the first line of review.md in runDir: exactly ACCEPT or CHANGES.
// Anything else gives CHANGES and a warning.
func readVerdict(runDir string) (verdict, warning string) {
	b, err := os.ReadFile(filepath.Join(runDir, "review.md"))
	if err != nil {
		return "CHANGES", "review.md not readable: " + err.Error()
	}
	first, _, _ := strings.Cut(string(b), "\n")
	switch first = strings.TrimSpace(first); first {
	case "ACCEPT", "CHANGES":
		return first, ""
	}
	return "CHANGES", fmt.Sprintf("review.md first line %q is not ACCEPT or CHANGES", first)
}

// mergeKey maps the first word of the final answer to retry, code or ask.
func mergeKey(answer string) string {
	f := strings.Fields(strings.ToLower(answer))
	if len(f) == 0 {
		return "ask"
	}
	switch w := strings.Trim(f[0], ".,;:!?\"'`*"); w {
	case "retry", "code", "ask":
		return w
	}
	return "ask"
}

// fetchIssueContext returns the issue text. Text from authors without write
// or admin permission is dropped. Any gh error is returned.
func fetchIssueContext(ctx context.Context, repo string, issue int) (string, error) {
	return newIssueFetcher().fetch(ctx, repo, issue)
}

type issueFetcher struct {
	mu    sync.Mutex
	perms map[string]string
}

func newIssueFetcher() *issueFetcher { return &issueFetcher{perms: map[string]string{}} }

func (f *issueFetcher) canWrite(ctx context.Context, repo, user string) (bool, error) {
	if user == "" {
		return false, nil
	}
	f.mu.Lock()
	p, ok := f.perms[user]
	f.mu.Unlock()
	if !ok {
		out, err := exec.CommandContext(ctx, "gh", "api", "repos/"+repo+"/collaborators/"+user+"/permission", "--jq", ".permission").Output()
		if err != nil {
			return false, fmt.Errorf("gh api permission for %s: %w", user, err)
		}
		p = strings.TrimSpace(string(out))
		f.mu.Lock()
		f.perms[user] = p
		f.mu.Unlock()
	}
	return p == "write" || p == "admin", nil
}

func (f *issueFetcher) fetch(ctx context.Context, repo string, issue int) (string, error) {
	out, err := exec.CommandContext(ctx, "gh", "issue", "view", strconv.Itoa(issue), "-R", repo, "--json", "title,body,author,comments").Output()
	if err != nil {
		return "", fmt.Errorf("gh issue view: %w", err)
	}
	var v struct {
		Title    string `json:"title"`
		Body     string `json:"body"`
		Author   struct{ Login string }
		Comments []struct {
			Author struct{ Login string }
			Body   string
		}
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", fmt.Errorf("gh issue view: %w", err)
	}
	var b strings.Builder
	ok, err := f.canWrite(ctx, repo, v.Author.Login)
	if err != nil {
		return "", err
	}
	if ok {
		fmt.Fprintf(&b, "# %s\n\n%s\n", v.Title, v.Body)
	} else {
		b.WriteString("(issue text omitted: its author has no write access)\n")
	}
	for _, c := range v.Comments {
		ok, err := f.canWrite(ctx, repo, c.Author.Login)
		if err != nil {
			return "", err
		}
		if ok {
			fmt.Fprintf(&b, "\n## Comment by %s\n\n%s\n", c.Author.Login, c.Body)
		}
	}
	return b.String(), nil
}
