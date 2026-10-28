package flow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/forge/gitlab"
)

func TestGitLabRemote(t *testing.T) {
	repos := map[string]flowconfig.Repo{
		"g/sub/p": {Forge: "gitlab", URL: "https://gitlab.example.com"},
		"o/r":     {Forge: "github"},
	}
	for remote, want := range map[string]string{
		"git@gitlab.example.com:g/sub/p.git":              "g/sub/p",
		"ssh://git@gitlab.example.com:2222/g/sub/p.git\n": "g/sub/p",
		"https://gitlab.example.com/g/sub/p.git":          "g/sub/p",
		"https://gitlab.example.com/g/sub/p/":             "g/sub/p",
		"https://GitLab.example.com/g/sub/p":              "g/sub/p",
		"git@other.example.com:g/sub/p.git":               "", // another host
		"https://gitlab.example.com/g/sub/q.git":          "", // not in repos
		"git@gitlab.example.com:o/r.git":                  "", // a GitHub entry
		"http://gitlab.example.com/g/sub/p.git":           "", // no TLS
		"/srv/git/g/sub/p.git":                            "",
	} {
		got, url := gitlabRemote(remote, repos)
		if got != want || (want != "" && url != "https://gitlab.example.com") || (want == "" && url != "") {
			t.Errorf("gitlabRemote(%q) = %q, %q", remote, got, url)
		}
	}
}

func TestDetectRepoAtGitLab(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".tyci"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"repos":{"g/sub/p":{"forge":"gitlab","url":"https://gitlab.example.com"}}}`
	if err := os.WriteFile(filepath.Join(home, ".tyci", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin", "git@gitlab.example.com:g/sub/p.git"},
		{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	info, err := DetectRepoAt(repo)
	if err != nil {
		t.Fatal(err)
	}
	if info.Repo != "g/sub/p" || info.GitLabURL != "https://gitlab.example.com" || info.DefaultBranch != "main" || info.Name() != "p" {
		t.Fatalf("got %+v", info)
	}
}

// fakeGitLab serves o/r: issue 1 with notes, members and the merge request
// create, which answers mrStatus. It returns the forge and the server URL.
func fakeGitLab(t *testing.T, mrStatus int) (*gitlab.GitLab, string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/projects/o%2Fr/")
		switch {
		case p == "issues/1":
			_, _ = w.Write([]byte(`{"iid":1,"title":"T","description":"BODY","state":"opened","author":{"username":"owner"}}`))
		case p == "issues/1/notes":
			_, _ = w.Write([]byte(`[{"id":1,"body":"GOOD","author":{"username":"alice"}},{"id":2,"body":"EVIL","author":{"username":"mallory"}},` +
				`{"id":3,"body":"SYSTEM","system":true,"author":{"username":"owner"}}]`))
		case p == "members/all":
			q := r.URL.Query().Get("query")
			lvl := 10
			if q == "owner" || q == "alice" {
				lvl = 30
			}
			_, _ = w.Write([]byte(`[{"username":"` + q + `","access_level":` + strconv.Itoa(lvl) + `}]`))
		case p == "merge_requests" && r.Method == http.MethodPost:
			w.WriteHeader(mrStatus)
			_, _ = w.Write([]byte(`{"web_url":"https://g.example/o/r/-/merge_requests/9"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("TYCI_GITLAB_TOKEN", "glpat-wire-test-0123456789")
	g, err := gitlab.New("o/r", ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return g, ts.URL
}

func TestGitLabIssueContext(t *testing.T) {
	g, _ := fakeGitLab(t, http.StatusCreated)
	out, err := gitlabIssueContext(g)(context.Background(), "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# T\n\nBODY") || !strings.Contains(out, "GOOD") || strings.Contains(out, "EVIL") || strings.Contains(out, "SYSTEM") {
		t.Fatalf("out = %q", out)
	}
}

func TestGitLabProposalMR(t *testing.T) {
	body := filepath.Join(t.TempDir(), "proposal.md")
	if err := os.WriteFile(body, []byte("why"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusCreated, http.StatusForbidden} {
		_, url := fakeGitLab(t, status)
		info := RepoInfo{Repo: "o/r", DefaultBranch: "main", GitLabURL: url}
		undone := false
		got, err := createGitLabProposal(context.Background(), info, "tyci/proposal-x", "chore: x", body, func() { undone = true })
		if status == http.StatusCreated && (err != nil || got != "https://g.example/o/r/-/merge_requests/9" || undone) {
			t.Errorf("created: url %q, err %v, undone %v", got, err, undone)
		}
		if status != http.StatusCreated && (err == nil || !undone) {
			t.Errorf("refused: err %v, undone %v", err, undone)
		}
	}
}

// #216: a GitLab repo gets the GitLab check env even when the forge cannot be
// built (no token): the scripts must never fall back to gh.
func TestNewRunnerGitLab(t *testing.T) {
	m := NewManager(func(string) {}, nil)
	wf := &Workflow{Name: "demo", Source: t.TempDir()}
	st := &RunState{Run: "r1"}
	info := RepoInfo{Home: t.TempDir(), Root: t.TempDir(), Repo: "g/p", GitLabURL: "https://gitlab.example.com"}

	t.Setenv("TYCI_GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")
	if r := m.NewRunner(info, wf, st); !r.GitLab {
		t.Fatal("no token: GitLab env missing")
	}
	t.Setenv("TYCI_GITLAB_TOKEN", "glpat-wire-test-0123456789")
	r := m.NewRunner(info, wf, st)
	sub, ok := r.Agents.(*SubagentRunner)
	if !r.GitLab || !ok || sub.IssueContext == nil {
		t.Fatalf("GitLab %v, agents %T", r.GitLab, r.Agents)
	}
	if tt, ok := sub.Render.(TaskTemplates); !ok || !strings.HasSuffix(tt.ForgeCmd, "' forge --repo g/p") {
		t.Fatalf("render %#v", sub.Render)
	}
	info.GitLabURL = ""
	if r := m.NewRunner(info, wf, st); r.GitLab {
		t.Fatal("GitHub repo got the GitLab env")
	}
}
