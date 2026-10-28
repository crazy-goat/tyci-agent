package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/forge/gitlab"
)

const forgeTestToken = "glpat-forgecmd-token-0123456789"

// forgeHome is a home with o/r as a GitLab repo. The forge talks to h.
func forgeHome(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	old := newGitLab
	newGitLab = func(repo, _ string) (*gitlab.GitLab, error) { return gitlab.New(repo, ts.URL) }
	t.Cleanup(func() { newGitLab = old })
	t.Setenv("TYCI_GITLAB_TOKEN", forgeTestToken)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".tyci"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"repos":{"o/r":{"forge":"gitlab","url":"https://gitlab.example.com"}}}`
	if err := os.WriteFile(filepath.Join(home, ".tyci", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func runForgeT(home string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := runForge(context.Background(), home, "o/r", args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestForgeCmdHidden(t *testing.T) {
	if !forgeCmd.Hidden {
		t.Fatal("tyci forge is in the help")
	}
}

func TestForgeCmdPrintsGhShape(t *testing.T) {
	home := forgeHome(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/o%2Fr/merge_requests/5":
			_, _ = w.Write([]byte(`{"iid":5,"state":"opened","sha":"abc","detailed_merge_status":"need_rebase"}`))
		case "/api/v4/projects/o%2Fr/members/all":
			_, _ = w.Write([]byte(`[{"username":"a.b","access_level":30}]`))
		default:
			http.NotFound(w, r)
		}
	})
	code, out, errs := runForgeT(home, "pr-view", "5")
	if code != 0 || out != `{"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"BEHIND","headRefOid":"abc"}`+"\n" {
		t.Fatalf("code %d, out %q, err %q", code, out, errs)
	}
	if code, out, _ := runForgeT(home, "can-write", "a.b"); code != 0 || out != "write\n" {
		t.Fatalf("can-write: %d %q", code, out)
	}
	if code, out, _ := runForgeT(home, "can-write", "c"); code != 0 || out != "none\n" {
		t.Fatalf("can-write: %d %q", code, out)
	}
}

func TestForgeCmdErrors(t *testing.T) {
	home := forgeHome(t, func(w http.ResponseWriter, r *http.Request) {
		// A server that echoes the token.
		http.Error(w, "401 "+r.Header.Get("PRIVATE-TOKEN"), http.StatusUnauthorized)
	})
	for _, args := range [][]string{
		{"whoami"}, {"pr-view", "5"}, {"pr-view"}, {"pr-view", "x"}, {"nope"}, {},
	} {
		code, out, errs := runForgeT(home, args...)
		if code != 1 || out != "" || !strings.HasPrefix(errs, "tyci forge: ") || strings.Contains(errs, forgeTestToken) {
			t.Errorf("%v: code %d, out %q, err %q", args, code, out, errs)
		}
	}
	var out, errb bytes.Buffer
	if code := runForge(context.Background(), home, "x/y", []string{"whoami"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "not a GitLab repo") {
		t.Fatalf("x/y: %d %q", code, errb.String())
	}
}
