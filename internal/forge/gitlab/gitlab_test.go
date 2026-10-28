package gitlab_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/forge/forgetest"
	"github.com/crazy-goat/tyci-agent/internal/forge/gitlab"
)

const (
	token   = "glpat-test-token-0123456789"
	project = "/api/v4/projects/o%2Fr/"
	shaA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type item = map[string]any

// server is a fake GitLab. Routes are keyed by "METHOD path", where path is
// the escaped path after /api/v4/projects/o%2Fr/.
type server struct {
	t      *testing.T
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	calls  []string
}

func newServer(t *testing.T) (*server, *gitlab.GitLab) {
	t.Helper()
	s := &server{t: t, routes: map[string]http.HandlerFunc{}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ok := strings.CutPrefix(r.URL.EscapedPath(), project)
		key := r.Method + " " + path
		s.mu.Lock()
		s.calls = append(s.calls, key+"?"+r.URL.RawQuery)
		h := s.routes[key]
		s.mu.Unlock()
		if !ok || h == nil || r.Header.Get("PRIVATE-TOKEN") != token {
			http.Error(w, `{"message":"404 Not Found"}`, http.StatusNotFound)
			return
		}
		h(w, r)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("TYCI_GITLAB_TOKEN", token)
	g, err := gitlab.New("o/r", ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return s, g
}

func (s *server) handle(key string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[key] = h
}

func (s *server) called(prefix string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}

func TestGitLabContract(t *testing.T) {
	forgetest.RunContract(t, func(t *testing.T, fx forgetest.Fixture) forge.Forge {
		s, g := newServer(t)
		s.handle("GET milestones", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("state") != "active" {
				t.Errorf("milestones query %q", r.URL.RawQuery)
			}
			ms := []item{}
			for _, m := range fx.Milestones {
				ms = append(ms, item{"id": 1000 + m.Number, "iid": m.Number, "title": m.Title})
			}
			writeJSON(t, w, ms)
		})
		s.handle("GET issues", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			out := []item{}
			for _, i := range fx.Issues {
				state := "closed"
				if i.State == "open" {
					state = "opened"
				}
				if q.Get("state") != "all" && q.Get("state") != state {
					continue
				}
				if want := q.Get("milestone"); (want == "None" && i.Milestone != "") || (want != "None" && want != i.Milestone) {
					continue
				}
				it := item{"id": 5000 + i.Number, "iid": i.Number, "title": i.Title, "state": state,
					"description": i.Body, "labels": append([]string{}, i.Labels...), "author": item{"username": i.Author}}
				if i.Milestone != "" {
					it["milestone"] = item{"title": i.Milestone}
				}
				out = append(out, it)
			}
			writeJSON(t, w, out)
		})
		s.handle("GET members/all", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query().Get("query")
			out := []item{}
			// "xnobodyx" is a maintainer whose name only contains "nobody".
			members := map[string]int{"xnobodyx": 40}
			for u, ok := range fx.Writers {
				members[u] = 20
				if ok {
					members[u] = 30
				}
			}
			for u, lvl := range members {
				if strings.Contains(u, q) {
					out = append(out, item{"username": u, "access_level": lvl})
				}
			}
			writeJSON(t, w, out)
		})
		return g
	})
}

func TestGitLabPagination(t *testing.T) {
	s, g := newServer(t)
	s.handle("GET issues", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 3 {
			w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		}
		writeJSON(t, w, []item{{"iid": page, "state": "opened", "author": item{"username": "a"}}})
	})
	is, err := g.Issues(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(is) != 3 || is[0].Number != 1 || is[2].Number != 3 || is[0].State != "open" {
		t.Fatalf("got %+v", is)
	}
}

func TestGitLabPaginationStops(t *testing.T) {
	s, g := newServer(t)
	s.handle("GET issues", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Next-Page", "2") // never ends
		writeJSON(t, w, []item{})
	})
	if _, err := g.Issues(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "pages") {
		t.Fatalf("got %v", err)
	}
}

// mr serves merge request 5 with head shaA and the given pipeline status.
func mr(s *server, t *testing.T, state, pipeline string) {
	s.handle("GET merge_requests/5", func(w http.ResponseWriter, _ *http.Request) {
		v := item{"iid": 5, "state": state, "sha": shaA}
		if pipeline != "" {
			v["head_pipeline"] = item{"status": pipeline}
		}
		writeJSON(t, w, v)
	})
}

func TestGitLabMergeRejectsMovedHead(t *testing.T) {
	s, g := newServer(t)
	// The GET still shows head A as green, but the branch moved to B before
	// the PUT. GitLab answers 409 when sha is not the head.
	mr(s, t, "opened", "success")
	var merged atomic.Bool
	s.handle("PUT merge_requests/5/merge", func(w http.ResponseWriter, r *http.Request) {
		if sha := r.URL.Query().Get("sha"); sha != "" && sha != shaB {
			http.Error(w, `{"message":"SHA does not match HEAD of source branch"}`, http.StatusConflict)
			return
		}
		merged.Store(true)
		writeJSON(t, w, item{"state": "merged"})
	})
	err := g.Merge(context.Background(), 5, shaA)
	if !errors.Is(err, gitlab.ErrHeadMoved) || merged.Load() {
		t.Fatalf("err %v, merged %v", err, merged.Load())
	}

	// A head that already moved at the GET is refused without a PUT.
	s2, g2 := newServer(t)
	mr(s2, t, "opened", "success")
	if err := g2.Merge(context.Background(), 5, shaB); !errors.Is(err, gitlab.ErrHeadMoved) {
		t.Fatalf("got %v", err)
	}
	if s2.called("PUT") {
		t.Fatal("PUT sent for a moved head")
	}
}

func TestGitLabMergeOnlyGreen(t *testing.T) {
	for _, tc := range []struct{ state, pipeline string }{
		{"opened", "running"}, {"opened", "failed"}, {"opened", ""}, {"merged", "success"}, {"closed", "success"},
	} {
		s, g := newServer(t)
		mr(s, t, tc.state, tc.pipeline)
		err := g.Merge(context.Background(), 5, shaA)
		if !errors.Is(err, gitlab.ErrNotGreen) && !errors.Is(err, gitlab.ErrNotOpen) {
			t.Errorf("%+v: got %v", tc, err)
		}
		if s.called("PUT") {
			t.Errorf("%+v: PUT sent", tc)
		}
	}
}

func TestGitLabMergeSendsSha(t *testing.T) {
	s, g := newServer(t)
	mr(s, t, "opened", "success")
	var query atomic.Value
	s.handle("PUT merge_requests/5/merge", func(w http.ResponseWriter, r *http.Request) {
		query.Store(r.URL.RawQuery)
		writeJSON(t, w, item{"state": "merged"})
	})
	if err := g.Merge(context.Background(), 5, shaA); err != nil {
		t.Fatal(err)
	}
	q, _ := query.Load().(string)
	if !strings.Contains(q, "sha="+shaA) || strings.Contains(q, "merge_when_pipeline_succeeds") {
		t.Fatalf("query %q", q)
	}
}

func TestGitLabChangeStates(t *testing.T) {
	for _, tc := range []struct{ state, pipeline, wantState, wantChecks string }{
		{"opened", "success", "open", "pass"},
		{"opened", "failed", "open", "fail"},
		{"opened", "running", "open", "pending"},
		{"opened", "", "open", "pending"},
		{"merged", "success", "merged", "pass"},
		{"closed", "canceled", "closed", "fail"},
	} {
		s, g := newServer(t)
		mr(s, t, tc.state, tc.pipeline)
		ch, err := g.Change(context.Background(), 5)
		if err != nil || ch.Number != 5 || ch.HeadSHA != shaA || ch.State != tc.wantState || ch.Checks != tc.wantChecks {
			t.Errorf("%+v: got %+v, %v", tc, ch, err)
		}
	}
}

func TestGitLabTokenNotInError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A server that echoes the token it got.
		http.Error(w, `{"message":"401 Unauthorized `+r.Header.Get("PRIVATE-TOKEN")+`"}`, http.StatusUnauthorized)
	}))
	defer ts.Close()
	t.Setenv("TYCI_GITLAB_TOKEN", token)
	g, err := gitlab.New("o/r", ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Milestones(context.Background())
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("got %v", err)
	}
}

func TestGitLabNoRedirect(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("PRIVATE-TOKEN") != "")
		writeJSON(t, w, []item{})
	}))
	defer other.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer ts.Close()
	t.Setenv("TYCI_GITLAB_TOKEN", token)
	g, err := gitlab.New("o/r", ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Milestones(context.Background()); err == nil || leaked.Load() {
		t.Fatalf("err %v, leaked %v", err, leaked.Load())
	}
}

func TestGitLabTokenEnv(t *testing.T) {
	t.Setenv("TYCI_GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")
	if _, err := gitlab.New("o/r", "https://gitlab.example.com"); err == nil {
		t.Fatal("no token accepted")
	}
	var got atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("PRIVATE-TOKEN"))
		writeJSON(t, w, []item{})
	}))
	defer ts.Close()
	t.Setenv("GITLAB_TOKEN", "fallback-token-123")
	g, err := gitlab.New("o/r", ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Milestones(context.Background()); err != nil || got.Load() != "fallback-token-123" {
		t.Fatalf("err %v, token %v", err, got.Load())
	}
}

func TestGitLabNewRejects(t *testing.T) {
	t.Setenv("TYCI_GITLAB_TOKEN", token)
	for _, c := range [][2]string{{"noslash", "https://g.example"}, {"a/b c", "https://g.example"}, {"o/r", "ftp://g.example"}, {"o/r", "gitlab.example.com"}} {
		if _, err := gitlab.New(c[0], c[1]); err == nil {
			t.Errorf("%v accepted", c)
		}
	}
	if g, err := gitlab.New("group/sub/project", "https://g.example"); err != nil || g.Repo() != "group/sub/project" {
		t.Fatalf("nested group: %v", err)
	}
}

func TestGitLabCanWriteCached(t *testing.T) {
	s, g := newServer(t)
	var n atomic.Int32
	s.handle("GET members/all", func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		writeJSON(t, w, []item{{"username": "alice", "access_level": 50}})
	})
	for range 2 {
		if ok, err := g.CanWrite(context.Background(), "alice"); err != nil || !ok {
			t.Fatalf("%v, %v", ok, err)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("%d calls", n.Load())
	}
	if _, err := g.CanWrite(context.Background(), "a/b"); err == nil {
		t.Fatal("bad user accepted")
	}
}
