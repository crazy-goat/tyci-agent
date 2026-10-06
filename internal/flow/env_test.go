package flow

import (
	"os"
	"strings"
	"testing"
)

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if i := strings.Index(kv, "="); i >= 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

func TestBuildCheckEnv_ContainsAllTyciVars(t *testing.T) {
	t.Setenv("GH_TOKEN", "tok123")
	t.Setenv("GITHUB_TOKEN", "ghtok456")
	st := &RunState{
		Repo:     "o/r",
		Issue:    160,
		Branch:   "issue-160",
		Worktree: "/tmp/wt",
		PR:       171,
		Current:  "ci",
		Visits:   map[string]int{"ci": 2},
	}
	env := envMap(buildCheckEnv(st, State{}, "/tmp/run", "main"))
	for k, want := range map[string]string{
		"TYCI_REPO":           "o/r",
		"TYCI_ISSUE":          "160",
		"TYCI_BRANCH":         "issue-160",
		"TYCI_DEFAULT_BRANCH": "main",
		"TYCI_WORKTREE":       "/tmp/wt",
		"TYCI_RUN_DIR":        "/tmp/run",
		"TYCI_STATE":          "ci",
		"TYCI_VISIT":          "2",
		"TYCI_PR":             "171",
		"GH_TOKEN":            "tok123",
		"GITHUB_TOKEN":        "ghtok456",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q (env %v)", k, env[k], want, env)
		}
	}
	for _, k := range []string{"PATH", "HOME", "LANG"} {
		if _, ok := env[k]; !ok {
			t.Errorf("expected %s in env", k)
		}
	}
	if _, ok := env["TYCI_DEFAULT_BRANCH"]; !ok {
		t.Error("expected TYCI_DEFAULT_BRANCH in env")
	}
}

func TestBuildCheckEnv_EmptyPRWhenZero(t *testing.T) {
	st := &RunState{Current: "a", Visits: map[string]int{"a": 1}}
	env := envMap(buildCheckEnv(st, State{}, "/tmp/run", "main"))
	v, ok := env["TYCI_PR"]
	if !ok {
		t.Fatal("TYCI_PR missing")
	}
	if v != "" {
		t.Fatalf("TYCI_PR = %q, want empty when PR == 0", v)
	}
}

func TestBuildCheckEnv_DropsOtherParentVars(t *testing.T) {
	t.Setenv("SECRET_X", "s3cr3t")
	st := &RunState{Current: "a", Visits: map[string]int{"a": 1}}
	env := envMap(buildCheckEnv(st, State{}, "/tmp/run", "main"))
	if _, ok := env["SECRET_X"]; ok {
		t.Fatal("SECRET_X leaked into check env")
	}
}

func TestBuildCheckEnv_TokensAbsentWhenUnset(t *testing.T) {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		k := k
		if v, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { _ = os.Setenv(k, v) })
		} else {
			t.Cleanup(func() { _ = os.Unsetenv(k) })
		}
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("Unsetenv(%s): %v", k, err)
		}
	}
	st := &RunState{Current: "a", Visits: map[string]int{"a": 1}}
	env := envMap(buildCheckEnv(st, State{}, "/tmp/run", "main"))
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s present although unset in parent", k)
		}
	}
}
