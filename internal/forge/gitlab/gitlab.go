// Package gitlab is a forge.Forge for one GitLab project over the REST v4 API.
//
// GitLab names map to forge names: a merge request is a change, a pipeline is
// the check of a change, a project milestone is a milestone, and the issue iid
// is the issue number. The token comes from TYCI_GITLAB_TOKEN or GITLAB_TOKEN.
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/redact"
)

// developer is the lowest GitLab access level that can push.
const developer = 30

var (
	projectRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)
	userRe    = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	shaRe     = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

var (
	// ErrHeadMoved means the head of the change is not the given commit.
	ErrHeadMoved = errors.New("gitlab: the head of the merge request moved")
	// ErrNotGreen means the pipeline of the change did not pass.
	ErrNotGreen = errors.New("gitlab: the pipeline of the merge request did not pass")
	// ErrNotOpen means the change is merged or closed.
	ErrNotOpen = errors.New("gitlab: the merge request is not open")
)

// GitLab is a forge.Forge for one GitLab project.
type GitLab struct {
	project string // "group/project"
	id      string // project path with "/" escaped, for API paths
	c       *client
	mu      sync.Mutex
	perm    map[string]bool
}

var _ forge.Forge = (*GitLab)(nil)

// New returns the forge for project ("group/project") on the GitLab server
// at baseURL. The token comes from TYCI_GITLAB_TOKEN, else GITLAB_TOKEN.
func New(project, baseURL string) (*GitLab, error) {
	if !projectRe.MatchString(project) {
		return nil, fmt.Errorf("gitlab: invalid project %q", project)
	}
	// http:// is accepted only for tests (httptest); the config and the
	// README require https://.
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("gitlab: invalid url %q", baseURL)
	}
	token := os.Getenv("TYCI_GITLAB_TOKEN")
	if token == "" {
		token = os.Getenv("GITLAB_TOKEN")
	}
	if token == "" {
		return nil, errors.New("gitlab: set TYCI_GITLAB_TOKEN or GITLAB_TOKEN")
	}
	redact.Add(token)
	return &GitLab{
		project: project,
		id:      strings.ReplaceAll(project, "/", "%2F"),
		c:       newClient(baseURL, token),
		perm:    map[string]bool{},
	}, nil
}

// Repo returns "group/project".
func (g *GitLab) Repo() string { return g.project }

// list sends GET for every page of path and decodes the JSON arrays.
func list[T any](ctx context.Context, g *GitLab, path string, q url.Values) ([]T, error) {
	pages, err := g.c.getAll(ctx, "projects/"+g.id+"/"+path, q)
	if err != nil {
		return nil, err
	}
	var all []T
	for _, p := range pages {
		var page []T
		if err := json.Unmarshal(p, &page); err != nil {
			return nil, fmt.Errorf("gitlab: bad JSON: %w", err)
		}
		all = append(all, page...)
	}
	return all, nil
}

type milestone struct {
	IID   int    `json:"iid"`
	Title string `json:"title"`
}

func (g *GitLab) activeMilestones(ctx context.Context) ([]milestone, error) {
	return list[milestone](ctx, g, "milestones", url.Values{"state": {"active"}})
}

// Milestones returns the active project milestones. GitLab sends no issue
// counts, so it counts the issues of each milestone.
func (g *GitLab) Milestones(ctx context.Context) ([]forge.Milestone, error) {
	raw, err := g.activeMilestones(ctx)
	if err != nil {
		return nil, err
	}
	ms := make([]forge.Milestone, 0, len(raw))
	for _, r := range raw {
		is, err := list[struct {
			State string `json:"state"`
		}](ctx, g, "issues", url.Values{"milestone": {r.Title}, "state": {"all"}})
		if err != nil {
			return nil, err
		}
		m := forge.Milestone{Number: r.IID, Title: r.Title}
		for _, i := range is {
			if i.State == "opened" {
				m.OpenCount++
			} else {
				m.ClosedCount++
			}
		}
		ms = append(ms, m)
	}
	return ms, nil
}

// Issues returns the open issues of a milestone title, or those without a
// milestone for "".
func (g *GitLab) Issues(ctx context.Context, title string) ([]forge.Issue, error) {
	ms := "None"
	if title != "" {
		raw, err := g.activeMilestones(ctx)
		if err != nil {
			return nil, err
		}
		found := false
		for _, m := range raw {
			found = found || m.Title == title
		}
		if !found {
			return nil, forge.ErrUnknownMilestone
		}
		ms = title
	}
	raw, err := list[struct {
		IID         int      `json:"iid"`
		Title       string   `json:"title"`
		State       string   `json:"state"`
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
		Author      struct {
			Username string `json:"username"`
		} `json:"author"`
		Milestone *milestone `json:"milestone"`
	}](ctx, g, "issues", url.Values{"state": {"opened"}, "milestone": {ms}})
	if err != nil {
		return nil, err
	}
	issues := []forge.Issue{}
	for _, r := range raw {
		is := forge.Issue{Number: r.IID, Title: r.Title, State: issueState(r.State), Body: r.Description, Author: r.Author.Username, Labels: r.Labels}
		if r.Milestone != nil {
			is.Milestone = r.Milestone.Title
		}
		issues = append(issues, is)
	}
	return issues, nil
}

func issueState(s string) string {
	if s == "opened" {
		return "open"
	}
	return s
}

// CanWrite reports whether user is a project member (also by group) with
// Developer access or more. A user who is not a member gets false. Results are
// cached per user.
func (g *GitLab) CanWrite(ctx context.Context, user string) (bool, error) {
	if !userRe.MatchString(user) {
		return false, fmt.Errorf("gitlab: invalid user %q", user)
	}
	g.mu.Lock()
	v, ok := g.perm[user]
	g.mu.Unlock()
	if ok {
		return v, nil
	}
	members, err := list[struct {
		Username    string `json:"username"`
		AccessLevel int    `json:"access_level"`
	}](ctx, g, "members/all", url.Values{"query": {user}})
	if err != nil {
		return false, err
	}
	allowed := false
	for _, m := range members {
		// query matches by substring; only the exact name counts.
		if m.Username == user && m.AccessLevel >= developer {
			allowed = true
		}
	}
	g.mu.Lock()
	g.perm[user] = allowed
	g.mu.Unlock()
	return allowed, nil
}

// Change is a merge request.
type Change struct {
	// Number is the merge request iid.
	Number int
	// State is "open", "merged" or "closed".
	State string
	// HeadSHA is the last commit of the source branch.
	HeadSHA string
	// Checks is the head pipeline: "pass", "fail" or "pending" (also no
	// pipeline, or a pipeline of an older commit).
	Checks string
}

// Change returns the merge request iid.
func (g *GitLab) Change(ctx context.Context, iid int) (Change, error) {
	body, _, err := g.c.do(ctx, http.MethodGet, g.mrPath(iid), nil)
	if err != nil {
		return Change{}, err
	}
	var r struct {
		IID          int    `json:"iid"`
		State        string `json:"state"`
		SHA          string `json:"sha"`
		HeadPipeline *struct {
			Status string `json:"status"`
			SHA    string `json:"sha"`
		} `json:"head_pipeline"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Change{}, fmt.Errorf("gitlab: bad JSON: %w", err)
	}
	ch := Change{Number: r.IID, State: issueState(r.State), HeadSHA: r.SHA, Checks: "pending"}
	// Right after a push, head_pipeline can still be the pipeline of the old head.
	if r.HeadPipeline != nil && r.HeadPipeline.SHA == r.SHA {
		ch.Checks = checks(r.HeadPipeline.Status)
	}
	return ch, nil
}

// checks maps a GitLab pipeline status to "pass", "fail" or "pending".
func checks(status string) string {
	switch status {
	case "success":
		return "pass"
	case "failed", "canceled":
		return "fail"
	}
	return "pending"
}

// Merge squash-merges the merge request iid and deletes its source branch.
// It merges only an open change whose head is sha and whose pipeline passed.
// It sends sha to GitLab, so a head that moves after the check fails with
// ErrHeadMoved. It never uses "merge when pipeline succeeds".
func (g *GitLab) Merge(ctx context.Context, iid int, sha string) error {
	if !shaRe.MatchString(sha) {
		return fmt.Errorf("gitlab: invalid sha %q", sha)
	}
	ch, err := g.Change(ctx, iid)
	if err != nil {
		return err
	}
	switch {
	case ch.State != "open":
		return fmt.Errorf("%w: !%d is %s", ErrNotOpen, iid, ch.State)
	case ch.HeadSHA != sha:
		return fmt.Errorf("%w: !%d is at %s, not %s", ErrHeadMoved, iid, ch.HeadSHA, sha)
	case ch.Checks != "pass":
		return fmt.Errorf("%w: !%d is %s", ErrNotGreen, iid, ch.Checks)
	}
	q := url.Values{"sha": {sha}, "squash": {"true"}, "should_remove_source_branch": {"true"}}
	_, _, err = g.c.do(ctx, http.MethodPut, g.mrPath(iid)+"/merge", q)
	if isStatus(err, http.StatusConflict) {
		return fmt.Errorf("%w: %v", ErrHeadMoved, err)
	}
	return err
}

func (g *GitLab) mrPath(iid int) string {
	return "projects/" + g.id + "/merge_requests/" + strconv.Itoa(iid)
}
