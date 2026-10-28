package flow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/tools"
)

// OpenRepo returns the RepoInfo of the registry entry repo: the checkout at
// repos.<repo>.path of ~/.tyci/config.json under home. The origin remote of the
// checkout must be repo.
func OpenRepo(home, repo string) (RepoInfo, error) {
	cfg, err := flowconfig.Load(home, "", false)
	if err != nil {
		return RepoInfo{}, err
	}
	r, ok := cfg.Repos[repo]
	if !ok || r.Path == "" {
		return RepoInfo{}, fmt.Errorf("repository %s has no path: add repos.%s.path to ~/.tyci/config.json", repo, repo)
	}
	info, err := DetectRepoAt(r.Path)
	if err != nil {
		return RepoInfo{}, fmt.Errorf("repos.%s.path %s: %w", repo, r.Path, err)
	}
	if info.Repo != repo {
		return RepoInfo{}, fmt.Errorf("repos.%s.path %s: the origin remote is %s, not %s", repo, r.Path, info.Repo, repo)
	}
	return info, nil
}

// RegistryRepos returns the repositories of ~/.tyci/config.json under home that
// have a path, sorted.
func RegistryRepos(home string) ([]string, error) {
	cfg, err := flowconfig.Load(home, "", false)
	if err != nil {
		return nil, err
	}
	var out []string
	for name, r := range cfg.Repos {
		if r.Path != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// qualifiedRef matches "owner/name#N" (also a GitLab path with groups).
var qualifiedRef = regexp.MustCompile(`^([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+)#(.+)$`)

// splitRef splits "owner/name#rest" into the repo and rest. A bare ref ("#12",
// "12") has no repo.
func splitRef(s string) (repo, rest string) {
	if m := qualifiedRef.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		return m[1], m[2]
	}
	return "", strings.TrimSpace(s)
}

// pickRepo returns repo when it is open, or the only open repo when repo is
// empty. what names the ref in the errors.
func pickRepo(repo string, open []string, what string) (string, error) {
	if len(open) == 0 {
		return "", errors.New("no repository is open: start tyci in a git repository, or add repos.<owner/name>.path to ~/.tyci/config.json")
	}
	if repo == "" {
		if len(open) > 1 {
			return "", fmt.Errorf("%s is ambiguous: %d repositories are open (%s); name the repository, for example %s#12",
				what, len(open), strings.Join(open, ", "), open[0])
		}
		return open[0], nil
	}
	if !slices.Contains(open, repo) {
		return "", fmt.Errorf("repository %s is not open (open: %s); add repos.%s.path to ~/.tyci/config.json", repo, strings.Join(open, ", "), repo)
	}
	return repo, nil
}

// ParseIssueRef reads an issue ref of the chat: "owner/name#N", or a bare "#N"
// or "N". A bare ref is allowed only when exactly one repository is open.
func ParseIssueRef(s string, open []string) (repo string, issue int, err error) {
	repo, rest := splitRef(s)
	n, err := strconv.Atoi(strings.TrimPrefix(rest, "#"))
	if err != nil || n <= 0 {
		return "", 0, fmt.Errorf("bad issue ref %q: use owner/name#N or #N", s)
	}
	repo, err = pickRepo(repo, open, "issue #"+strconv.Itoa(n))
	return repo, n, err
}

// Repos routes the chat workflow tools to one Manager per open repository
// (#218). The open repositories are the repository of Primary (the current
// directory) and every repository of ~/.tyci/config.json with a path. All
// Managers share one worker limit (Pool).
type Repos struct {
	// Primary is the Manager of the current directory. Its Info fails outside a
	// git repository.
	Primary *Manager
	// Registry returns the repositories with a path (production: RegistryRepos).
	Registry func() ([]string, error)
	// New builds the Manager of a registry repository.
	New func(repo string) (*Manager, error)

	mu   sync.Mutex
	pool *Pool
	ms   map[string]*Manager
	base context.Context
}

// Open returns the open repositories, sorted.
func (r *Repos) Open() []string {
	set := map[string]bool{}
	if info, err := r.Primary.Info(); err == nil {
		set[info.Repo] = true
	}
	if r.Registry != nil {
		names, _ := r.Registry()
		for _, n := range names {
			set[n] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// For returns the Manager of the open repository repo.
func (r *Repos) For(repo string) (*Manager, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pool == nil {
		r.pool = &Pool{}
		r.pool.Join(r.Primary)
		r.Primary.mu.Lock()
		r.pool.workers.Store(int32(r.Primary.workers))
		r.Primary.mu.Unlock()
	}
	if info, err := r.Primary.Info(); err == nil && info.Repo == repo {
		return r.Primary, nil
	}
	if m, ok := r.ms[repo]; ok {
		return m, nil
	}
	if r.New == nil {
		return nil, fmt.Errorf("repository %s is not open", repo)
	}
	m, err := r.New(repo)
	if err != nil {
		return nil, err
	}
	r.pool.Join(m)
	if r.base != nil {
		m.SetBase(r.base)
	}
	if r.ms == nil {
		r.ms = map[string]*Manager{}
	}
	r.ms[repo] = m
	return m, nil
}

// SetBase sets the context that cancels the runs of every repository.
func (r *Repos) SetBase(ctx context.Context) {
	r.mu.Lock()
	r.base = ctx
	ms := r.managers()
	r.mu.Unlock()
	for _, m := range ms {
		m.SetBase(ctx)
	}
}

// Shutdown shuts down the Managers of every repository.
func (r *Repos) Shutdown(wait time.Duration) {
	r.mu.Lock()
	ms := r.managers()
	r.mu.Unlock()
	for _, m := range ms {
		m.Shutdown(wait)
	}
}

// managers returns Primary and the registry Managers. r.mu must be held.
func (r *Repos) managers() []*Manager {
	out := []*Manager{r.Primary}
	for _, k := range slices.Sorted(maps.Keys(r.ms)) {
		out = append(out, r.ms[k])
	}
	return out
}

// pick returns the Manager of ref ("owner/name#rest" or a bare ref) and rest.
func (r *Repos) pick(ref, what string) (*Manager, string, string, error) {
	repo, rest := splitRef(ref)
	repo, err := pickRepo(repo, r.Open(), what)
	if err != nil {
		return nil, "", "", err
	}
	m, err := r.For(repo)
	return m, repo, rest, err
}

// qualify returns run as "owner/name#N" when several repositories are open,
// so a later call names the right repository.
func (r *Repos) qualify(repo, run string) string {
	if run == "" || len(r.Open()) < 2 {
		return run
	}
	return repo + ShowRunID(run)
}

// Start implements tools.WorkflowManager. The repository is repo, or the repo of
// an "owner/name#N" param (the param becomes N), or the only open repository.
func (r *Repos) Start(ctx context.Context, repo, workflow string, params []string) (string, []string, error) {
	params = slices.Clone(params)
	for i, p := range params {
		if q, rest := splitRef(p); q != "" {
			if repo != "" && repo != q {
				return "", nil, fmt.Errorf("param %q names %s, but repo is %s", p, q, repo)
			}
			repo, params[i] = q, strings.TrimPrefix(rest, "#")
		}
	}
	repo, err := pickRepo(repo, r.Open(), "the repository of the run")
	if err != nil {
		return "", nil, err
	}
	m, err := r.For(repo)
	if err != nil {
		return "", nil, err
	}
	run, warnings, err := ChatTools{M: m}.Start(ctx, repo, workflow, params)
	return r.qualify(repo, run), warnings, err
}

// Workflows implements tools.WorkflowManager: the workflows of the current
// directory, or of the only open repository.
func (r *Repos) Workflows() []tools.WorkflowInfo {
	if _, err := r.Primary.Info(); err == nil {
		return ChatTools{M: r.Primary}.Workflows()
	}
	m, _, _, err := r.pick("", "")
	if err != nil {
		return nil
	}
	return ChatTools{M: m}.Workflows()
}

// Status implements tools.WorkflowManager.
func (r *Repos) Status(run string) (any, error) {
	m, _, id, err := r.pick(run, "run "+run)
	if err != nil {
		return nil, err
	}
	return ChatTools{M: m}.Status(id)
}

// List implements tools.WorkflowManager. With several open repositories it
// returns one page per repository, by name.
func (r *Repos) List(archived bool, limit, offset int) (any, error) {
	open := r.Open()
	if len(open) < 2 {
		m, _, _, err := r.pick("", "")
		if err != nil {
			return nil, err
		}
		return ChatTools{M: m}.List(archived, limit, offset)
	}
	out := map[string]any{}
	for _, repo := range open {
		m, err := r.For(repo)
		if err != nil {
			out[repo] = map[string]string{"error": err.Error()}
			continue
		}
		page, err := ChatTools{M: m}.List(archived, limit, offset)
		if err != nil {
			return nil, err
		}
		out[repo] = page
	}
	return map[string]any{"repos": out}, nil
}

// Resume implements tools.WorkflowManager.
func (r *Repos) Resume(run, answer string) error {
	m, _, id, err := r.pick(run, "run "+run)
	if err != nil {
		return err
	}
	return ChatTools{M: m}.Resume(id, answer)
}

// Stop implements tools.WorkflowManager.
func (r *Repos) Stop(run, reason string) (any, error) {
	m, _, id, err := r.pick(run, "run "+run)
	if err != nil {
		return nil, err
	}
	return ChatTools{M: m}.Stop(id, reason)
}

// Replace implements tools.WorkflowManager. agentID is <run>/<role>; the run may
// be "owner/name#N".
func (r *Repos) Replace(agentID, role, reason string) error {
	i := strings.LastIndex(agentID, "/")
	if i < 0 {
		return ChatTools{M: r.Primary}.Replace(agentID, role, reason)
	}
	m, _, id, err := r.pick(agentID[:i], "run "+agentID[:i])
	if err != nil {
		return err
	}
	return ChatTools{M: m}.Replace(id+agentID[i:], role, reason)
}
