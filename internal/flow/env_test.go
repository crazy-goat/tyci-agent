package flow

import (
	"os"
	"path/filepath"
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

// #356: TYCI_REVIEW_DIR is the artifact dir of the newest review step, whatever
// its state is called. A task of the review role (findings) is not a review.
// #403: the step role decides, so a state renamed after the run started still counts.
func TestReviewDir(t *testing.T) {
	r := &Runner{RunDir: "/tmp/run"}
	art := func(name string) string { return filepath.Join("/tmp/run", "artifacts", name) }
	steps := []Step{
		{State: "judge", Role: "review", Artifact: "002-judge"},
		{State: "code", Role: "worker", Artifact: "003-code"},
		{State: "judge", Role: "review", Artifact: "004-judge"},
		{State: "findings", Role: "review", Task: "findings_to_issues", Artifact: "005-findings"},
	}
	for name, tc := range map[string]struct {
		history []Step
		want    string
	}{
		"newest review wins":    {steps, art("004-judge")},
		"earlier review":        {steps[:2], art("002-judge")},
		"findings is no review": {steps[3:], ""},
		"no review":             {steps[1:2], ""},
		"review without dir":    {[]Step{{State: "judge", Role: "review"}}, ""},
		"no history":            {nil, ""},
		"renamed review state":  {[]Step{{State: "review", Role: "review", Artifact: "001-review"}}, art("001-review")},
	} {
		if got := r.reviewDir(&RunState{History: tc.history}); got != tc.want {
			t.Errorf("%s: reviewDir = %q, want %q", name, got, tc.want)
		}
	}
}
