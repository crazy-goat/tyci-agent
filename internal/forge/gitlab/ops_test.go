package gitlab_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/forge/gitlab"
)

// body decodes the JSON request body of r.
func body(t *testing.T, r *http.Request) item {
	t.Helper()
	var v item
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		t.Error(err)
	}
	return v
}

// asJSON is v as compact JSON, to compare the gh shape.
func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var ctx = context.Background()

func TestGitLabIssue(t *testing.T) {
	s, g := newServer(t)
	s.handle("GET issues/7", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, item{"iid": 7, "title": "T", "description": "B", "state": "opened",
			"labels": []string{"accepted", "risky"}, "author": item{"username": "a.b"},
			"assignees": []item{{"username": "bot_1"}}})
	})
	is, err := g.Issue(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"state":"OPEN","title":"T","body":"B","labels":[{"name":"accepted"},{"name":"risky"}],"author":{"login":"a.b"},"assignees":[{"login":"bot_1"}]}`
	if got := asJSON(t, is); got != want {
		t.Fatalf("got %s", got)
	}
	s.handle("GET issues/7", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, item{"iid": 7, "state": "closed"})
	})
	if is, err := g.Issue(ctx, 7); err != nil || is.State != "CLOSED" || is.Labels == nil || is.Assignees == nil {
		t.Fatalf("got %+v, %v", is, err)
	}
}

func TestGitLabComments(t *testing.T) {
	s, g := newServer(t)
	notes := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sort") != "asc" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		writeJSON(t, w, []item{
			{"id": 11, "body": "hi", "system": false, "author": item{"username": "alice"}},
			{"id": 12, "body": "changed the description", "system": true, "author": item{"username": "alice"}},
		})
	}
	s.handle("GET merge_requests/5/notes", notes)
	s.handle("GET issues/7/notes", notes)
	s.handle("GET merge_requests/5", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, item{"iid": 5, "web_url": "https://g.example/o/r/-/merge_requests/5"})
	})
	cs, err := g.PRComments(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"id":11,"user":{"login":"alice"},"html_url":"https://g.example/o/r/-/merge_requests/5#note_11","body":"hi"}]`
	if got := asJSON(t, cs); got != want {
		t.Fatalf("got %s", got)
	}
	if cs, err := g.IssueComments(ctx, 7); err != nil || len(cs) != 1 || cs[0].Body != "hi" {
		t.Fatalf("got %+v, %v", cs, err)
	}
}

func TestGitLabAssignSelf(t *testing.T) {
	s, g := newServer(t)
	s.handle("GET /user", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, item{"id": 3, "username": "tyci"})
	})
	s.handle("GET issues/7", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, item{"iid": 7, "assignees": []item{{"id": 9, "username": "bob"}, {"id": 3, "username": "tyci"}}})
	})
	var sent atomic.Value
	s.handle("PUT issues/7", func(w http.ResponseWriter, r *http.Request) {
		sent.Store(asJSON(t, body(t, r)))
		writeJSON(t, w, item{})
	})
	if err := g.AssignSelf(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if got := sent.Load(); got != `{"assignee_ids":[3,9]}` {
		t.Fatalf("sent %v", got)
	}
	if u, err := g.Whoami(ctx); err != nil || u != "tyci" {
		t.Fatalf("whoami %q, %v", u, err)
	}
}

func TestGitLabPRs(t *testing.T) {
	s, g := newServer(t)
	s.handle("GET merge_requests", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("source_branch") != "issue-7" || q.Get("state") != "opened" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		writeJSON(t, w, []item{
			{"iid": 5, "source_branch": "issue-7", "source_project_id": 1, "target_project_id": 1},
			{"iid": 6, "source_branch": "issue-7", "source_project_id": 2, "target_project_id": 1},
		})
	})
	prs, err := g.PRs(ctx, "issue-7")
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"number":5,"headRefName":"issue-7","isCrossRepository":false,"closingIssuesReferences":[]},` +
		`{"number":6,"headRefName":"issue-7","isCrossRepository":true,"closingIssuesReferences":[]}]`
	if got := asJSON(t, prs); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestGitLabWrites(t *testing.T) {
	s, g := newServer(t)
	var sent atomic.Value
	record := func(url string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			sent.Store(asJSON(t, body(t, r)))
			writeJSON(t, w, item{"web_url": url})
		}
	}
	s.handle("POST merge_requests", record("https://g.example/mr/5"))
	s.handle("POST merge_requests/5/notes", record(""))
	s.handle("POST issues", record("https://g.example/issues/8"))
	s.handle("POST issues/7/notes", record(""))

	for _, tc := range []struct {
		name, want, url string
		op              func() (string, error)
	}{
		{"create pr", `{"description":"Closes #7","remove_source_branch":true,"source_branch":"issue-7","target_branch":"main","title":"feat: x"}`, "https://g.example/mr/5",
			func() (string, error) { return g.CreatePR(ctx, "main", "issue-7", "feat: x", "Closes #7") }},
		{"comment pr", `{"body":"review"}`, "", func() (string, error) { return "", g.CommentPR(ctx, 5, "review") }},
		{"create issue", `{"description":"B","title":"T"}`, "https://g.example/issues/8",
			func() (string, error) { return g.CreateIssue(ctx, "T", "B") }},
		{"comment issue", `{"body":"seen"}`, "", func() (string, error) { return "", g.CommentIssue(ctx, 7, "seen") }},
	} {
		sent.Store("")
		url, err := tc.op()
		if err != nil || url != tc.url || sent.Load() != tc.want {
			t.Errorf("%s: url %q, err %v, sent %v", tc.name, url, err, sent.Load())
		}
	}
}

func TestGitLabPRView(t *testing.T) {
	for _, tc := range []struct {
		state, status string
		conflicts     bool
		want          string
	}{
		{"opened", "mergeable", false, "OPEN MERGEABLE CLEAN"},
		{"opened", "need_rebase", false, "OPEN MERGEABLE BEHIND"},
		{"opened", "conflict", false, "OPEN CONFLICTING DIRTY"},
		{"opened", "ci_still_running", true, "OPEN CONFLICTING DIRTY"},
		{"opened", "checking", false, "OPEN UNKNOWN UNKNOWN"},
		{"opened", "ci_must_pass", false, "OPEN MERGEABLE BLOCKED"},
		{"merged", "", false, "MERGED MERGEABLE BLOCKED"},
		{"closed", "", false, "CLOSED MERGEABLE BLOCKED"},
	} {
		s, g := newServer(t)
		s.handle("GET merge_requests/5", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, item{"iid": 5, "state": tc.state, "sha": shaA, "detailed_merge_status": tc.status, "has_conflicts": tc.conflicts})
		})
		p, err := g.PR(ctx, 5)
		if got := p.State + " " + p.Mergeable + " " + p.MergeStateStatus; err != nil || got != tc.want || p.HeadRefOid != shaA {
			t.Errorf("%+v: got %+v, %v", tc, p, err)
		}
	}
}

func TestGitLabPRChecks(t *testing.T) {
	for _, tc := range []struct{ status, sha, want string }{
		{"success", shaA, "pass"}, {"failed", shaA, "fail"}, {"canceled", shaA, "cancel"},
		{"skipped", shaA, "skipping"}, {"running", shaA, "pending"}, {"manual", shaA, "pending"},
		{"success", shaB, ""}, // a pipeline of an older head: no check yet
		{"", "", ""},          // no pipeline
	} {
		s, g := newServer(t)
		mrAt(s, t, "opened", tc.status, tc.sha)
		cs, err := g.PRChecks(ctx, 5)
		got := ""
		if len(cs) == 1 && cs[0].Name == "ci-ok" {
			got = cs[0].Bucket
		}
		if err != nil || got != tc.want || cs == nil || len(cs) > 1 {
			t.Errorf("%+v: got %+v, %v", tc, cs, err)
		}
	}
}

func TestGitLabCILog(t *testing.T) {
	s, g := newServer(t)
	s.handle("GET merge_requests/5", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, item{"iid": 5, "sha": shaA, "head_pipeline": item{"id": 40, "status": "failed", "sha": shaA}})
	})
	s.handle("GET pipelines/40/jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope[]") != "failed" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		writeJSON(t, w, []item{{"id": 41, "name": "test", "stage": "check"}})
	})
	s.handle("GET jobs/41/trace", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 300<<10) + "FAIL TestX token=" + token + "\n"))
	})
	var out bytes.Buffer
	if err := g.CILog(ctx, 5, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "=== job test (stage check) ===\n") || !strings.Contains(got, "FAIL TestX") ||
		strings.Contains(got, token) || out.Len() > 260<<10 {
		t.Fatalf("got %d bytes: %.80q ... %q", out.Len(), got, got[max(0, len(got)-80):])
	}

	s2, g2 := newServer(t)
	mrAt(s2, t, "opened", "failed", shaB)
	if err := g2.CILog(ctx, 5, &out); err == nil {
		t.Fatal("log of an older pipeline")
	}
}

func TestGitLabSearchIssues(t *testing.T) {
	s, g := newServer(t)
	s.handle("GET issues", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("search") != "race lock" || q.Get("state") != "all" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		writeJSON(t, w, []item{{"iid": 3, "title": "fix lock", "state": "closed", "web_url": "u3"}})
	})
	is, err := g.SearchIssues(ctx, "race lock")
	if err != nil || !reflect.DeepEqual(is, []gitlab.IssueRef{{Number: 3, Title: "fix lock", State: "CLOSED", URL: "u3"}}) {
		t.Fatalf("got %+v, %v", is, err)
	}
}
