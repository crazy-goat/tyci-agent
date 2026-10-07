package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const cmdTimeout = 30 * time.Second

var (
	repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	userRe = regexp.MustCompile(`^[A-Za-z0-9-]+(\[bot\])?$`)
	sshRe  = regexp.MustCompile(`^git@github\.com:([^/]+)/([^/]+?)(\.git)?$`)
	httpRe = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+?)(\.git)?/?$`)
)

// GitHub is a Forge backed by the gh command line tool.
type GitHub struct {
	repo string
	bin  string
	mu   sync.Mutex
	perm map[string]bool
}

// ParseRemote extracts owner and name from a git remote URL.
func ParseRemote(u string) (owner, name string, err error) {
	u = strings.TrimSpace(u)
	for _, re := range []*regexp.Regexp{sshRe, httpRe} {
		if m := re.FindStringSubmatch(u); m != nil {
			return m[1], m[2], nil
		}
	}
	return "", "", fmt.Errorf("forge: unsupported remote URL %q", u)
}

// NewGitHub returns a GitHub forge for repo ("owner/name"). "auto" or ""
// reads the origin remote of the current directory.
func NewGitHub(repo string) (*GitHub, error) {
	g := &GitHub{bin: "gh", perm: map[string]bool{}}
	if repo == "auto" || repo == "" {
		out, err := g.run(context.Background(), "git", "remote", "get-url", "origin")
		if err != nil {
			return nil, err
		}
		o, n, err := ParseRemote(string(out))
		if err != nil {
			return nil, err
		}
		repo = o + "/" + n
	}
	if !repoRe.MatchString(repo) {
		return nil, fmt.Errorf("forge: invalid repo %q", repo)
	}
	g.repo = repo
	return g, nil
}

// Repo returns "owner/name".
func (g *GitHub) Repo() string { return g.repo }

func (g *GitHub) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, &runError{err: err, stderr: msg}
	}
	return stdout.Bytes(), nil
}

type runError struct {
	err    error
	stderr string
}

func (e *runError) Error() string {
	return fmt.Sprintf("forge: %v: %s", e.err, strings.TrimSpace(e.stderr))
}

func (e *runError) Unwrap() error { return e.err }

func (g *GitHub) api(ctx context.Context, path string) ([]byte, error) {
	return g.run(ctx, g.bin, "api", "--paginate", "repos/"+g.repo+"/"+path)
}

// decodeAll decodes the concatenated JSON arrays that gh --paginate prints,
// one array per page, with a json.Decoder loop until io.EOF.
func decodeAll[T any](data []byte) ([]T, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var all []T
	for {
		var page []T
		err := dec.Decode(&page)
		if errors.Is(err, io.EOF) {
			return all, nil
		}
		if err != nil {
			return nil, fmt.Errorf("forge: bad JSON: %w", err)
		}
		all = append(all, page...)
	}
}

// Milestones returns the open milestones.
func (g *GitHub) Milestones(ctx context.Context) ([]Milestone, error) {
	out, err := g.api(ctx, "milestones?state=open&per_page=100")
	if err != nil {
		return nil, err
	}
	raw, err := decodeAll[struct {
		Number       int    `json:"number"`
		Title        string `json:"title"`
		OpenIssues   int    `json:"open_issues"`
		ClosedIssues int    `json:"closed_issues"`
	}](out)
	if err != nil {
		return nil, err
	}
	ms := make([]Milestone, 0, len(raw))
	for _, r := range raw {
		ms = append(ms, Milestone{Number: r.Number, Title: r.Title, OpenCount: r.OpenIssues, ClosedCount: r.ClosedIssues})
	}
	return ms, nil
}

func (g *GitHub) milestoneNumber(ctx context.Context, title string) (int, error) {
	ms, err := g.Milestones(ctx)
	if err != nil {
		return 0, err
	}
	for _, m := range ms {
		if m.Title == title {
			return m.Number, nil
		}
	}
	return 0, ErrUnknownMilestone
}

// Issues returns the open issues of a milestone title, or those without a
// milestone for "". Items that are not issues are skipped.
func (g *GitHub) Issues(ctx context.Context, milestone string) ([]Issue, error) {
	ms := "none"
	if milestone != "" {
		n, err := g.milestoneNumber(ctx, milestone)
		if err != nil {
			return nil, err
		}
		ms = strconv.Itoa(n)
	}
	out, err := g.api(ctx, "issues?state=open&per_page=100&milestone="+ms)
	if err != nil {
		return nil, err
	}
	raw, err := decodeAll[struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
		Body   string `json:"body"`
		User   struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Milestone *struct {
			Title string `json:"title"`
		} `json:"milestone"`
		PullRequest json.RawMessage `json:"pull_request"`
	}](out)
	if err != nil {
		return nil, err
	}
	issues := []Issue{}
	for _, r := range raw {
		if len(r.PullRequest) > 0 {
			continue
		}
		is := Issue{Number: r.Number, Title: r.Title, State: r.State, Body: r.Body, Author: r.User.Login}
		for _, l := range r.Labels {
			is.Labels = append(is.Labels, l.Name)
		}
		if r.Milestone != nil {
			is.Milestone = r.Milestone.Title
		}
		issues = append(issues, is)
	}
	return issues, nil
}

// CanWrite reports whether user has write or admin permission. A user who is
// not a collaborator (HTTP 404) gets false. Results are cached per user.
func (g *GitHub) CanWrite(ctx context.Context, user string) (bool, error) {
	if !userRe.MatchString(user) {
		return false, fmt.Errorf("forge: invalid user %q", user)
	}
	g.mu.Lock()
	v, ok := g.perm[user]
	g.mu.Unlock()
	if ok {
		return v, nil
	}
	out, err := g.run(ctx, g.bin, "api", "repos/"+g.repo+"/collaborators/"+url.PathEscape(user)+"/permission")
	allowed := false
	if err != nil {
		var re *runError
		var ee *exec.ExitError
		if !errors.As(err, &re) || !errors.As(err, &ee) || ee.ExitCode() != 1 || !strings.Contains(re.stderr, "HTTP 404") {
			return false, err
		}
	} else {
		var p struct {
			Permission string `json:"permission"`
		}
		if err := json.Unmarshal(out, &p); err != nil {
			return false, fmt.Errorf("forge: bad JSON: %w", err)
		}
		allowed = p.Permission == "admin" || p.Permission == "write"
	}
	g.mu.Lock()
	g.perm[user] = allowed
	g.mu.Unlock()
	return allowed, nil
}
