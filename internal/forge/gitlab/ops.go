package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"

	"github.com/crazy-goat/tyci-agent/internal/redact"
)

// The ops below serve the hidden `tyci forge` command and the GitLab path of
// the flow code. Their results have the JSON shape of the gh fields that the
// check scripts read (gh pr view --json ..., gh pr checks --json ...), so a
// script keeps one jq program for both forges.

// Login is a gh user object.
type Login struct {
	Login string `json:"login"`
}

// Label is a gh label object.
type Label struct {
	Name string `json:"name"`
}

// IssueInfo is gh issue view --json state,title,body,labels,author,assignees.
type IssueInfo struct {
	State     string  `json:"state"` // OPEN or CLOSED
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Labels    []Label `json:"labels"`
	Author    Login   `json:"author"`
	Assignees []Login `json:"assignees"`
}

type userJSON struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
}

type issueJSON struct {
	IID         int        `json:"iid"`
	Title       string     `json:"title"`
	State       string     `json:"state"`
	Description string     `json:"description"`
	WebURL      string     `json:"web_url"`
	Labels      []string   `json:"labels"`
	Author      userJSON   `json:"author"`
	Assignees   []userJSON `json:"assignees"`
}

func (g *GitLab) get(ctx context.Context, path string, q url.Values, v any) error {
	body, _, err := g.c.do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("gitlab: bad JSON: %w", err)
	}
	return nil
}

func (g *GitLab) send(ctx context.Context, method, path string, in, v any) error {
	body, _, err := g.c.do(ctx, method, path, nil, in)
	if err != nil || v == nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("gitlab: bad JSON: %w", err)
	}
	return nil
}

func (g *GitLab) issuePath(iid int) string {
	return "projects/" + g.id + "/issues/" + strconv.Itoa(iid)
}

func (g *GitLab) getIssue(ctx context.Context, iid int) (issueJSON, error) {
	var r issueJSON
	return r, g.get(ctx, g.issuePath(iid), nil, &r)
}

// Issue returns the issue iid.
func (g *GitLab) Issue(ctx context.Context, iid int) (IssueInfo, error) {
	r, err := g.getIssue(ctx, iid)
	if err != nil {
		return IssueInfo{}, err
	}
	is := IssueInfo{State: "CLOSED", Title: r.Title, Body: r.Description, Author: Login{r.Author.Username},
		Labels: []Label{}, Assignees: []Login{}}
	if r.State == "opened" {
		is.State = "OPEN"
	}
	for _, l := range r.Labels {
		is.Labels = append(is.Labels, Label{l})
	}
	for _, a := range r.Assignees {
		is.Assignees = append(is.Assignees, Login{a.Username})
	}
	return is, nil
}

// Comment is a gh comment: a GitLab note that is not a system note.
type Comment struct {
	ID   int    `json:"id"`
	User Login  `json:"user"`
	URL  string `json:"html_url"`
	Body string `json:"body"`
}

// notes returns the notes of an issue or merge request, oldest first, without
// system notes ("changed the description" and similar). webURL is the page of
// the issue or merge request.
func (g *GitLab) notes(ctx context.Context, path, webURL string) ([]Comment, error) {
	raw, err := list[struct {
		ID     int      `json:"id"`
		Body   string   `json:"body"`
		System bool     `json:"system"`
		Author userJSON `json:"author"`
	}](ctx, g, path+"/notes", url.Values{"sort": {"asc"}, "order_by": {"created_at"}})
	if err != nil {
		return nil, err
	}
	out := []Comment{}
	for _, n := range raw {
		if !n.System {
			out = append(out, Comment{ID: n.ID, User: Login{n.Author.Username}, URL: webURL + "#note_" + strconv.Itoa(n.ID), Body: n.Body})
		}
	}
	return out, nil
}

// IssueComments returns the comments of the issue iid, oldest first.
func (g *GitLab) IssueComments(ctx context.Context, iid int) ([]Comment, error) {
	return g.notes(ctx, "issues/"+strconv.Itoa(iid), "")
}

// PRComments returns the comments of the merge request iid (also the diff
// comments), oldest first.
func (g *GitLab) PRComments(ctx context.Context, iid int) ([]Comment, error) {
	m, err := g.getMR(ctx, iid)
	if err != nil {
		return nil, err
	}
	return g.notes(ctx, "merge_requests/"+strconv.Itoa(iid), m.WebURL)
}

func (g *GitLab) self(ctx context.Context) (userJSON, error) {
	var u userJSON
	return u, g.get(ctx, "user", nil, &u)
}

// Whoami returns the user name of the token.
func (g *GitLab) Whoami(ctx context.Context) (string, error) {
	u, err := g.self(ctx)
	return u.Username, err
}

// AssignSelf adds the user of the token to the assignees of the issue iid. The
// other assignees stay.
func (g *GitLab) AssignSelf(ctx context.Context, iid int) error {
	me, err := g.self(ctx)
	if err != nil {
		return err
	}
	r, err := g.getIssue(ctx, iid)
	if err != nil {
		return err
	}
	ids := []int{me.ID}
	for _, a := range r.Assignees {
		if a.ID != me.ID {
			ids = append(ids, a.ID)
		}
	}
	return g.send(ctx, http.MethodPut, g.issuePath(iid), map[string]any{"assignee_ids": ids}, nil)
}

// PR is gh pr list --json number,headRefName,isCrossRepository,closingIssuesReferences.
// closingIssuesReferences is always empty: GitLab needs one more call per
// merge request for it.
type PR struct {
	Number        int    `json:"number"`
	HeadRefName   string `json:"headRefName"`
	IsCrossRepo   bool   `json:"isCrossRepository"`
	ClosingIssues []any  `json:"closingIssuesReferences"`
}

// PRs returns the open merge requests from the branch head.
func (g *GitLab) PRs(ctx context.Context, head string) ([]PR, error) {
	raw, err := list[struct {
		IID          int    `json:"iid"`
		SourceBranch string `json:"source_branch"`
		Source       int    `json:"source_project_id"`
		Target       int    `json:"target_project_id"`
	}](ctx, g, "merge_requests", url.Values{"state": {"opened"}, "source_branch": {head}})
	if err != nil {
		return nil, err
	}
	out := []PR{}
	for _, r := range raw {
		out = append(out, PR{Number: r.IID, HeadRefName: r.SourceBranch, IsCrossRepo: r.Source != r.Target, ClosingIssues: []any{}})
	}
	return out, nil
}

// CreatePR opens a merge request from head into base and returns its URL. The
// source branch is deleted at the merge.
func (g *GitLab) CreatePR(ctx context.Context, base, head, title, body string) (string, error) {
	var r struct {
		WebURL string `json:"web_url"`
	}
	in := map[string]any{"source_branch": head, "target_branch": base, "title": title, "description": body, "remove_source_branch": true}
	err := g.send(ctx, http.MethodPost, "projects/"+g.id+"/merge_requests", in, &r)
	return r.WebURL, err
}

// CommentPR adds a comment to the merge request iid.
func (g *GitLab) CommentPR(ctx context.Context, iid int, body string) error {
	return g.send(ctx, http.MethodPost, g.mrPath(iid)+"/notes", map[string]any{"body": body}, nil)
}

// PRInfo is gh pr view --json state,mergeable,mergeStateStatus,headRefOid.
type PRInfo struct {
	State            string `json:"state"`            // OPEN, MERGED or CLOSED
	Mergeable        string `json:"mergeable"`        // MERGEABLE, CONFLICTING or UNKNOWN
	MergeStateStatus string `json:"mergeStateStatus"` // CLEAN, DIRTY, BEHIND, BLOCKED or UNKNOWN
	HeadRefOid       string `json:"headRefOid"`
}

// PR returns the merge request iid.
func (g *GitLab) PR(ctx context.Context, iid int) (PRInfo, error) {
	m, err := g.getMR(ctx, iid)
	if err != nil {
		return PRInfo{}, err
	}
	p := PRInfo{State: "CLOSED", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", HeadRefOid: m.SHA}
	switch m.State {
	case "opened":
		p.State = "OPEN"
	case "merged":
		p.State = "MERGED"
	}
	switch m.DetailedMergeStatus {
	case "conflict":
		p.Mergeable, p.MergeStateStatus = "CONFLICTING", "DIRTY"
	case "need_rebase":
		p.MergeStateStatus = "BEHIND"
	case "mergeable":
		p.MergeStateStatus = "CLEAN"
	case "unchecked", "checking", "preparing", "approvals_syncing":
		p.Mergeable, p.MergeStateStatus = "UNKNOWN", "UNKNOWN"
	}
	if m.HasConflicts {
		p.Mergeable, p.MergeStateStatus = "CONFLICTING", "DIRTY"
	}
	return p, nil
}

// Check is gh pr checks --json name,state,bucket,link.
type Check struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Bucket string `json:"bucket"` // pass, fail, cancel, skipping or pending
	Link   string `json:"link"`
}

// PRChecks returns the pipeline of the head commit of the merge request iid
// as one check named "ci-ok", or no check when that commit has no pipeline.
func (g *GitLab) PRChecks(ctx context.Context, iid int) ([]Check, error) {
	m, err := g.getMR(ctx, iid)
	if err != nil {
		return nil, err
	}
	p := m.pipeline()
	if p == nil {
		return []Check{}, nil
	}
	bucket := "pending"
	switch p.Status {
	case "success":
		bucket = "pass"
	case "failed":
		bucket = "fail"
	case "canceled":
		bucket = "cancel"
	case "skipped":
		bucket = "skipping"
	}
	return []Check{{Name: "ci-ok", State: p.Status, Bucket: bucket, Link: p.WebURL}}, nil
}

// maxTrace is the end of each failed job log that CILog writes.
const maxTrace = 256 << 10

// CILog writes the logs of the failed jobs of the head pipeline of the merge
// request iid to w, at most the last maxTrace bytes of each.
func (g *GitLab) CILog(ctx context.Context, iid int, w io.Writer) error {
	m, err := g.getMR(ctx, iid)
	if err != nil {
		return err
	}
	p := m.pipeline()
	if p == nil {
		return fmt.Errorf("gitlab: !%d has no pipeline of its head", iid)
	}
	jobs, err := list[struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Stage string `json:"stage"`
	}](ctx, g, "pipelines/"+strconv.Itoa(p.ID)+"/jobs", url.Values{"scope[]": {"failed"}})
	if err != nil {
		return err
	}
	for _, j := range jobs {
		trace, _, err := g.c.do(ctx, http.MethodGet, "projects/"+g.id+"/jobs/"+strconv.Itoa(j.ID)+"/trace", nil, nil)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "=== job %s (stage %s) ===\n%s\n", j.Name, j.Stage, tail(redact.Redact(string(trace)))); err != nil {
			return err
		}
	}
	return nil
}

// tail is the last maxTrace bytes of s, cut at a rune start. Redact s first:
// a cut through a token would leave a part that the patterns do not match.
func tail(s string) string {
	if len(s) <= maxTrace {
		return s
	}
	i := len(s) - maxTrace
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

// IssueRef is gh issue list --json number,title,state,url.
type IssueRef struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"` // OPEN or CLOSED
	URL    string `json:"url"`
}

// SearchIssues returns up to 30 issues (open and closed) that match words.
func (g *GitLab) SearchIssues(ctx context.Context, words string) ([]IssueRef, error) {
	var raw []issueJSON
	q := url.Values{"search": {words}, "state": {"all"}, "per_page": {"30"}}
	if err := g.get(ctx, "projects/"+g.id+"/issues", q, &raw); err != nil {
		return nil, err
	}
	out := []IssueRef{}
	for _, r := range raw {
		ref := IssueRef{Number: r.IID, Title: r.Title, State: "CLOSED", URL: r.WebURL}
		if r.State == "opened" {
			ref.State = "OPEN"
		}
		out = append(out, ref)
	}
	return out, nil
}

// CommentIssue adds a comment to the issue iid.
func (g *GitLab) CommentIssue(ctx context.Context, iid int, body string) error {
	return g.send(ctx, http.MethodPost, g.issuePath(iid)+"/notes", map[string]any{"body": body}, nil)
}

// CreateIssue opens an issue without a milestone and labels and returns its URL.
func (g *GitLab) CreateIssue(ctx context.Context, title, body string) (string, error) {
	var r struct {
		WebURL string `json:"web_url"`
	}
	err := g.send(ctx, http.MethodPost, "projects/"+g.id+"/issues", map[string]any{"title": title, "description": body}, &r)
	return r.WebURL, err
}
