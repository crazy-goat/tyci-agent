package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// tyciForge is the fake `tyci forge --repo REPO OP ARGS` of the GitLab tests. It answers
// OP from the env var OUT_<op> (dashes become underscores) and fails when FAIL_<op> is
// set. can-write USER answers PERM_<user> (dots become underscores; default none).
// pr-comment and issue-comment copy the body file to $0.body. pr-create creates the MR:
// after it, pr-list answers OUT_pr_list_after.
const tyciForge = `
[ "$1 $2 $3" = "forge --repo g/p" ] || { echo "bad call: $*" >&2; exit 2; }
shift 3
op=${1//-/_}; shift
f=FAIL_$op
[ -z "${!f:-}" ] || { echo "tyci forge: GitLab API: 502" >&2; exit 1; }
case "$op" in
  can_write) v=PERM_${1//./_}; echo "${!v:-none}"; exit 0 ;;
  pr_comment | issue_comment) cat "$2" > "$0.body" ;;
  pr_create) touch "$0.created" ;;
  pr_list) [ -e "$0.created" ] && op=pr_list_after ;;
esac
v=OUT_$op
if [ -n "${!v:-}" ]; then printf '%s\n' "${!v}"; fi
`

// gitlabRun holds the logs of one GitLab check run.
type gitlabRun struct {
	key, stdout, stderr, tyci, gh, bin string
	exit                               int
}

// runGitLab runs script in dir ("" = the checks dir) with the fake tyci forge and a gh that
// always fails: the GitLab path must not call gh.
func runGitLab(t *testing.T, dir, script string, env map[string]string) gitlabRun {
	t.Helper()
	ghLog := testutil.StubGH(t, "exit 97")
	tenv, tyciLog := testutil.StubTyci(t, tyciForge)
	tenv["TYCI_REPO"] = "g/p"
	for k, v := range env {
		tenv[k] = v
	}
	if dir != "" {
		script, _ = filepath.Abs(script)
	}
	var r gitlabRun
	r.key, r.exit, r.stdout, r.stderr = testutil.RunCheckIn(t, dir, script, tenv)
	r.bin = tenv["TYCI_BIN"]
	b, _ := os.ReadFile(tyciLog)
	r.tyci = string(b)
	b, _ = os.ReadFile(ghLog)
	r.gh = string(b)
	if r.gh != "" {
		t.Errorf("%s called gh on GitLab:\n%s", script, r.gh)
	}
	return r
}

func gitlabIssue(author string, labels ...string) string {
	ls := make([]string, len(labels))
	for i, l := range labels {
		ls[i] = `{"name":"` + l + `"}`
	}
	return `{"state":"OPEN","labels":[` + strings.Join(ls, ",") + `],"author":{"login":"` + author + `"},"assignees":[]}`
}

func TestGitLab_IssueDone(t *testing.T) {
	for name, tc := range map[string]struct {
		env      map[string]string
		key      string
		exit     int
		assigned bool
	}{
		"dotted author goes": {map[string]string{"OUT_issue_view": gitlabIssue("a.b_c", "accepted"), "PERM_a_b_c": "write", "OUT_whoami": "tyci"}, "go", 0, true},
		"risky":              {map[string]string{"OUT_issue_view": gitlabIssue("a.b_c", "accepted", "risky"), "PERM_a_b_c": "write", "OUT_whoami": "tyci"}, "go-risky", 0, true},
		"no write skips":     {map[string]string{"OUT_issue_view": gitlabIssue("a.b_c", "accepted")}, "skip", 0, false},
		"can-write fails":    {map[string]string{"OUT_issue_view": gitlabIssue("a.b_c", "accepted"), "FAIL_can_write": "1"}, "", 1, false},
		"whoami fails":       {map[string]string{"OUT_issue_view": gitlabIssue("a.b_c", "accepted"), "PERM_a_b_c": "write", "FAIL_whoami": "1"}, "", 1, false},
		"assign fails":       {map[string]string{"OUT_issue_view": gitlabIssue("a.b_c", "accepted"), "PERM_a_b_c": "write", "OUT_whoami": "tyci", "FAIL_issue_assign_self": "1"}, "", 1, true},
	} {
		t.Run(name, func(t *testing.T) {
			tc.env["TYCI_ISSUE"] = "7"
			r := runGitLab(t, "", issueScript, tc.env)
			if r.key != tc.key || r.exit != tc.exit {
				t.Fatalf("key=%q exit=%d stderr=%s", r.key, r.exit, r.stderr)
			}
			if !strings.Contains(r.tyci, "forge --repo g/p issue-view 7\n") {
				t.Errorf("log:\n%s", r.tyci)
			}
			if got := strings.Contains(r.tyci, "issue-assign-self 7\n"); got != tc.assigned {
				t.Errorf("assign-self called=%v, log:\n%s", got, r.tyci)
			}
		})
	}
}

func TestGitLab_OpenPR(t *testing.T) {
	e, remote := newOpenPREnv(t)
	env := map[string]string{"TYCI_BRANCH": "issue-7", "TYCI_ISSUE": "7", "TYCI_DEFAULT_BRANCH": "main", "TYCI_RUN_DIR": e.runDir, "OUT_pr_list": "[]"}
	if r := runGitLab(t, e.work, "open_pr.sh", env); r.key != "none" || !strings.Contains(r.tyci, "pr-list issue-7\n") {
		t.Fatalf("key=%q stderr=%s log=%s", r.key, r.stderr, r.tyci)
	}
	env["OUT_pr_list"] = `[{"number":171,"headRefName":"issue-7","isCrossRepository":false,"closingIssuesReferences":[]}]`
	if r := runGitLab(t, e.work, "open_pr.sh", env); r.key != "found" {
		t.Fatalf("key=%q stderr=%s", r.key, r.stderr)
	}
	if got := git(t, e.work, "rev-parse", "HEAD"); got != remote {
		t.Fatalf("HEAD = %s, want the MR head %s", got, remote)
	}
	if b, _ := os.ReadFile(filepath.Join(e.runDir, "pr")); strings.TrimSpace(string(b)) != "171" {
		t.Fatalf("pr file = %q", b)
	}
}

func TestGitLab_Push(t *testing.T) {
	e := newPushEnv(t)
	r := runGitLab(t, e.work, "push.sh", map[string]string{
		"TYCI_BRANCH": "issue-7", "TYCI_DEFAULT_BRANCH": "main", "TYCI_RUN_DIR": e.runDir, "TYCI_ISSUE": "7",
		"OUT_pr_list": "[]", "OUT_pr_list_after": `[{"number":171}]`,
	})
	if r.exit != 0 || r.key == "" {
		t.Fatalf("key=%q exit=%d stderr=%s", r.key, r.exit, r.stderr)
	}
	if !strings.Contains(r.tyci, "pr-create main issue-7 feat: seven Closes #7\n") {
		t.Errorf("log:\n%s", r.tyci)
	}
	if b, _ := os.ReadFile(filepath.Join(e.runDir, "pr")); strings.TrimSpace(string(b)) != "171" {
		t.Fatalf("pr file = %q", b)
	}
}

func TestGitLab_PostReview(t *testing.T) {
	r := runGitLab(t, "", "post_review.sh", map[string]string{"TYCI_PR": "9", "TYCI_REVIEW_DIR": writeReview(t, "003-review", "ACCEPT\nlooks good\n")})
	if r.key != "ok" || !strings.Contains(r.tyci, "pr-comment 9 ") {
		t.Fatalf("key=%q stderr=%s log=%s", r.key, r.stderr, r.tyci)
	}
	if b, _ := os.ReadFile(r.bin + ".body"); !strings.HasPrefix(string(b), "<!-- tyci-agent -->\n") || !strings.Contains(string(b), "looks good") {
		t.Errorf("body = %q", b)
	}
	if r = runGitLab(t, "", "post_review.sh", map[string]string{"TYCI_PR": "9", "TYCI_REVIEW_DIR": writeReview(t, "003-review", "ACCEPT\n"), "FAIL_pr_comment": "1"}); r.key != "fail" {
		t.Fatalf("key=%q", r.key)
	}
}

func TestGitLab_CIWait(t *testing.T) {
	clean := `{"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}`
	for name, tc := range map[string]struct {
		view, checks, key string
		log               bool
	}{
		"green":    {clean, `[{"name":"ci-ok","state":"success","bucket":"pass","link":"u"}]`, "green", false},
		"red":      {clean, `[{"name":"ci-ok","state":"failed","bucket":"fail","link":"u"}]`, "red", true},
		"conflict": {`{"state":"OPEN","mergeable":"CONFLICTING","mergeStateStatus":"DIRTY"}`, `[]`, "conflict", false},
	} {
		t.Run(name, func(t *testing.T) {
			art := t.TempDir()
			r := runGitLab(t, "", "ci_wait.sh", map[string]string{"TYCI_PR": "5", "TYCI_CI_POLL_SEC": "0", "TYCI_ARTIFACT_DIR": art,
				"OUT_pr_view": tc.view, "OUT_pr_checks": tc.checks, "OUT_ci_log": "=== job test ===\nFAIL"})
			if r.key != tc.key {
				t.Fatalf("key=%q stderr=%s", r.key, r.stderr)
			}
			b, err := os.ReadFile(filepath.Join(art, "ci-failed.log"))
			if tc.log != (err == nil) || (tc.log && !strings.Contains(string(b), "FAIL")) || tc.log != strings.Contains(r.tyci, "ci-log 5\n") {
				t.Errorf("log file %q (%v), calls:\n%s", b, err, r.tyci)
			}
		})
	}
}

func TestGitLab_Merge(t *testing.T) {
	view := func(state, head string) string {
		return `{"state":"` + state + `","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","headRefOid":"` + head + `"}`
	}
	for name, tc := range map[string]struct {
		state, bucket, key, fail string
		merges                   bool
	}{
		"merged":         {"OPEN", "pass", "merged", "", true},
		"ci not green":   {"OPEN", "pending", "fail", "", false},
		"already merged": {"MERGED", "pass", "merged", "", false},
		"merge refused":  {"OPEN", "pass", "fail", "1", true},
	} {
		t.Run(name, func(t *testing.T) {
			e := newPushEnv(t)
			git(t, e.work, "push", "-q", "origin", "issue-7")
			head := git(t, e.work, "rev-parse", "HEAD")
			r := runGitLab(t, e.work, "merge.sh", map[string]string{"TYCI_PR": "171", "TYCI_DEFAULT_BRANCH": "main",
				"OUT_pr_view": view(tc.state, head), "OUT_pr_checks": `[{"name":"ci-ok","bucket":"` + tc.bucket + `"}]`, "FAIL_pr_merge": tc.fail})
			if r.key != tc.key || r.exit != 0 {
				t.Fatalf("key=%q exit=%d stderr=%s", r.key, r.exit, r.stderr)
			}
			if got := strings.Contains(r.tyci, "pr-merge 171 "+head+"\n"); got != tc.merges {
				t.Errorf("pr-merge called=%v, log:\n%s", got, r.tyci)
			}
			// The failure text names the GitLab command, not gh.
			if tc.fail != "" && (!strings.Contains(r.stderr, "tyci forge pr-merge 171") || strings.Contains(r.stderr, "gh pr")) {
				t.Errorf("stderr:\n%s", r.stderr)
			}
		})
	}
}

func TestGitLab_FetchComments(t *testing.T) {
	dir, art := t.TempDir(), t.TempDir()
	r := runGitLab(t, "", "fetch_comments.sh", map[string]string{"TYCI_PR": "5", "TYCI_RUN_DIR": dir, "TYCI_ARTIFACT_DIR": art,
		"OUT_pr_comments": "[" + cmt(1, "a.b", "from team") + "," + cmt(2, "bob", "from reader") + "," + cmt(3, "tyci", "<!-- tyci-agent -->\\nreview") + "]",
		"OUT_whoami":      "tyci", "PERM_a_b": "write", "PERM_tyci": "write",
	})
	b, _ := os.ReadFile(filepath.Join(art, "comments.md"))
	last, _ := os.ReadFile(filepath.Join(dir, "last_comment_id"))
	c := string(b)
	if r.key != "new" || !strings.Contains(c, "by a.b") || strings.Contains(c, "from reader") || strings.Contains(c, "review") {
		t.Fatalf("key=%q comments=%q stderr=%s", r.key, c, r.stderr)
	}
	if strings.TrimSpace(string(last)) != "3" {
		t.Errorf("last id = %q", last)
	}
	if !strings.Contains(r.tyci, "pr-comments 5\n") || !strings.Contains(r.tyci, "can-write bob\n") {
		t.Errorf("log:\n%s", r.tyci)
	}
}

// The steps that tyci supports only on GitHub return their failure key on GitLab, and do
// not call gh.
func TestGitLab_GitHubOnlyScripts(t *testing.T) {
	for script, key := range map[string]string{
		"ci_failure.sh": "fail", "findings_apply.sh": "fail", "findings_collect.sh": "fail",
		"rebalance_collect.sh": "fail", "rebalance_apply.sh": "fail", "milestone_close.sh": "fail",
		"release_tag.sh": "fail", "release_preflight.sh": "fail", "release_wait.sh": "failed",
	} {
		t.Run(script, func(t *testing.T) {
			r := runGitLab(t, "", script, nil)
			if r.key != key || r.exit != 0 || !strings.Contains(r.stderr, "works only for GitHub repos") {
				t.Fatalf("key=%q exit=%d stdout=%s stderr=%s", r.key, r.exit, r.stdout, r.stderr)
			}
			if r.tyci != "" {
				t.Errorf("tyci called:\n%s", r.tyci)
			}
		})
	}
}

// A script copied without lib_forge.sh (an older workflow copy) must not fall back to gh
// on a GitLab repo: it exits non-zero without a key. On GitHub it works as before.
func TestGitLab_MissingLibFails(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"describe.sh", "post_review.sh", "release_wait.sh"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	review := writeReview(t, "003-review", "ACCEPT\n")
	r := runGitLab(t, "", filepath.Join(dir, "post_review.sh"), map[string]string{"TYCI_PR": "9", "TYCI_REVIEW_DIR": review})
	if r.exit == 0 || !strings.Contains(r.stderr, "lib_forge.sh is missing") || r.tyci != "" {
		t.Fatalf("key=%q exit=%d stderr=%s", r.key, r.exit, r.stderr)
	}
	r = runGitLab(t, "", filepath.Join(dir, "release_wait.sh"), nil)
	if r.exit == 0 || !strings.Contains(r.stderr, "lib_forge.sh is missing") {
		t.Fatalf("key=%q exit=%d stderr=%s", r.key, r.exit, r.stderr)
	}

	logPath := testutil.StubGH(t, reviewGh)
	key, exit, _ := testutil.RunCheck(t, filepath.Join(dir, "post_review.sh"), map[string]string{"TYCI_REPO": "o/r", "TYCI_PR": "9", "TYCI_REVIEW_DIR": review})
	if b, _ := os.ReadFile(logPath); key != "ok" || exit != 0 || !strings.Contains(string(b), "pr review 9") {
		t.Fatalf("GitHub without lib_forge.sh: key=%q exit=%d log=%s", key, exit, b)
	}
}
