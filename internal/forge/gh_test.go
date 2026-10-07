//go:build unix

package forge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/forge/forgetest"
)

// route makes the stub gh answer when the argument string contains match.
type route struct {
	match  string
	stdout string
	stderr string
	exit   int
}

// newStub writes a gh stub into a temp dir and returns a forge that uses it,
// plus the path of the counter file (one line per stub call).
func newStub(t *testing.T, routes ...route) (*forge.GitHub, string) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "counter")
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\necho call >> '" + counter + "'\ncase \"$*\" in\n")
	for i, r := range routes {
		out := filepath.Join(dir, fmt.Sprintf("out%d", i))
		errf := filepath.Join(dir, fmt.Sprintf("err%d", i))
		if err := os.WriteFile(out, []byte(r.stdout), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(errf, []byte(r.stderr), 0o600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "*%s*) cat '%s'; cat '%s' >&2; exit %d;;\n", r.match, out, errf, r.exit)
	}
	sb.WriteString("esac\necho unexpected >&2\nexit 99\n")
	bin := filepath.Join(dir, "gh")
	if err := os.WriteFile(bin, []byte(sb.String()), 0o700); err != nil {
		t.Fatal(err)
	}
	g, err := forge.NewGitHub("o/r")
	if err != nil {
		t.Fatal(err)
	}
	g.SetBin(bin)
	return g, counter
}

func calls(t *testing.T, counter string) int {
	t.Helper()
	b, err := os.ReadFile(counter)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

const msJSON = `[{"number":3,"title":"v0.4.0","open_issues":1,"closed_issues":2}]`

func TestParseRemote(t *testing.T) {
	for _, u := range []string{"git@github.com:o/r.git", "git@github.com:o/r", "https://github.com/o/r.git", "https://github.com/o/r"} {
		o, n, err := forge.ParseRemote(u)
		if err != nil || o != "o" || n != "r" {
			t.Errorf("%s: %q %q %v", u, o, n, err)
		}
	}
	if _, _, err := forge.ParseRemote("http://example.com/o/r"); err == nil {
		t.Error("want error")
	}
}

func TestNewGitHub_BadRepo(t *testing.T) {
	for _, r := range []string{"bad repo", "a/b/c"} {
		if _, err := forge.NewGitHub(r); err == nil {
			t.Errorf("%q: want error", r)
		}
	}
}

func TestGitHub_IssuesSkipsPullRequests(t *testing.T) {
	g, _ := newStub(t,
		route{match: "milestones?", stdout: msJSON},
		route{match: "milestone=3", stdout: `[{"number":7,"title":"feat: a","state":"open","user":{"login":"alice"},"labels":[{"name":"accepted"}],"milestone":{"title":"v0.4.0"},"body":"Depends on #3"},
 {"number":8,"title":"a PR","pull_request":{"url":"x"},"user":{"login":"bob"},"labels":[]}]`})
	is, err := g.Issues(context.Background(), "v0.4.0")
	if err != nil || len(is) != 1 {
		t.Fatalf("got %+v, %v", is, err)
	}
	i := is[0]
	if i.Number != 7 || i.Author != "alice" || len(i.Labels) != 1 || i.Labels[0] != "accepted" || i.Milestone != "v0.4.0" || i.Body != "Depends on #3" {
		t.Fatalf("got %+v", i)
	}
}

func TestGitHub_IssuesUnknownMilestone(t *testing.T) {
	g, _ := newStub(t, route{match: "milestones?", stdout: msJSON})
	if _, err := g.Issues(context.Background(), "v9.9.9"); !errors.Is(err, forge.ErrUnknownMilestone) {
		t.Fatalf("got %v", err)
	}
}

func TestGitHub_Pagination(t *testing.T) {
	g, _ := newStub(t, route{match: "milestone=none", stdout: `[{"number":1,"title":"a","state":"open"}]
[{"number":2,"title":"b","state":"open"}]
`})
	is, err := g.Issues(context.Background(), "")
	if err != nil || len(is) != 2 || is[1].Number != 2 {
		t.Fatalf("got %+v, %v", is, err)
	}
}

func TestGitHub_CanWrite(t *testing.T) {
	stderr404, err := os.ReadFile("testdata/perm_404.stderr")
	if err != nil {
		t.Fatal(err)
	}
	g, _ := newStub(t,
		route{match: "/w/", stdout: `{"permission":"write"}`},
		route{match: "/ad/", stdout: `{"permission":"admin"}`},
		route{match: "/rd/", stdout: `{"permission":"read"}`},
		route{match: "/no/", stderr: string(stderr404), exit: 1},
		route{match: "/forb/", stderr: "gh: Forbidden (HTTP 403)", exit: 1},
	)
	ctx := context.Background()
	for user, want := range map[string]bool{"w": true, "ad": true, "rd": false, "no": false} {
		got, err := g.CanWrite(ctx, user)
		if err != nil || got != want {
			t.Errorf("%s: %v, %v", user, got, err)
		}
	}
	if _, err := g.CanWrite(ctx, "forb"); err == nil {
		t.Error("403: want error")
	}
}

func TestGitHub_CanWriteCached(t *testing.T) {
	g, counter := newStub(t, route{match: "/w/", stdout: `{"permission":"write"}`})
	for range 2 {
		if ok, err := g.CanWrite(context.Background(), "w"); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	if n := calls(t, counter); n != 1 {
		t.Fatalf("stub ran %d times", n)
	}
}

func TestGitHub_Timeout(t *testing.T) {
	g, _ := newStub(t)
	slow := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nexec sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	g.SetBin(slow)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := g.Milestones(ctx); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call hung")
	}
}

func TestGitHub_NoShell(t *testing.T) {
	g, counter := newStub(t)
	if _, err := g.CanWrite(context.Background(), "a;touch x"); err == nil {
		t.Fatal("want error")
	}
	if n := calls(t, counter); n != 0 {
		t.Fatalf("stub ran %d times", n)
	}
}

func TestGitHub_MissingBinary(t *testing.T) {
	g, _ := newStub(t)
	g.SetBin(filepath.Join(t.TempDir(), "missing"))
	ctx := context.Background()
	if _, err := g.Milestones(ctx); err == nil {
		t.Error("Milestones")
	}
	if _, err := g.Issues(ctx, ""); err == nil {
		t.Error("Issues")
	}
	if _, err := g.CanWrite(ctx, "a"); err == nil {
		t.Error("CanWrite")
	}
}

func TestGitHub_Contract(t *testing.T) {
	forgetest.RunContract(t, func(t *testing.T, fx forgetest.Fixture) forge.Forge {
		type item = map[string]any
		var routes []route
		var ms []item
		for _, m := range fx.Milestones {
			ms = append(ms, item{"number": m.Number, "title": m.Title, "open_issues": m.OpenCount, "closed_issues": m.ClosedCount})
		}
		routes = append(routes, route{match: "milestones?", stdout: mustJSON(t, ms)})
		byMs := map[string][]item{"none": {}}
		for _, m := range fx.Milestones {
			byMs[fmt.Sprint(m.Number)] = []item{}
		}
		for _, i := range fx.Issues {
			if i.State != "open" {
				continue
			}
			labels := []item{}
			for _, l := range i.Labels {
				labels = append(labels, item{"name": l})
			}
			it := item{"number": i.Number, "title": i.Title, "state": i.State, "body": i.Body, "labels": labels, "user": item{"login": i.Author}}
			key := "none"
			if i.Milestone != "" {
				it["milestone"] = item{"title": i.Milestone}
				for _, m := range fx.Milestones {
					if m.Title == i.Milestone {
						key = fmt.Sprint(m.Number)
					}
				}
			}
			byMs[key] = append(byMs[key], it)
		}
		for k, v := range byMs {
			v = append(v, item{"number": 99, "title": "a PR", "pull_request": item{"url": "x"}, "labels": []item{}})
			routes = append(routes, route{match: "milestone=" + k, stdout: mustJSON(t, v)})
		}
		for u, w := range fx.Writers {
			p := "read"
			if w {
				p = "write"
			}
			routes = append(routes, route{match: "/" + u + "/", stdout: mustJSON(t, item{"permission": p})})
		}
		stderr404, err := os.ReadFile("testdata/perm_404.stderr")
		if err != nil {
			t.Fatal(err)
		}
		routes = append(routes, route{match: "/collaborators/", stderr: string(stderr404), exit: 1})
		g, _ := newStub(t, routes...)
		return g
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
