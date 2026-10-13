package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// tagGh answers `pr view N ... --json mergeCommit` with MERGE_SHA. Other calls fail.
const tagGh = `
case "$*" in
  "pr view "*) printf '%s\n' "$MERGE_SHA" ;;
  *) exit 2 ;;
esac
`

// tagEnv makes a clone with origin, the merge commit (the head of main) and a run dir.
// approvals is written to approvals.txt when not empty.
func tagEnv(t *testing.T, approvals string) (dir, runDir, sha string, env map[string]string) {
	t.Helper()
	dir = releaseClone(t, changelogReady)
	sha = git(t, dir, "rev-parse", "HEAD")
	runDir = t.TempDir()
	if approvals != "" {
		if err := os.WriteFile(filepath.Join(runDir, "approvals.txt"), []byte(approvals), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env = map[string]string{
		"TYCI_PR": "171", "TYCI_REPO": "o/r", "TYCI_DEFAULT_BRANCH": "main", "TYCI_RUN_DIR": runDir,
		"TYCI_PARAM_MILESTONE": "v0.10.0", "MERGE_SHA": sha,
		// The test env has no HOME git config, so the annotated tag needs an identity.
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t",
	}
	return dir, runDir, sha, env
}

func runTag(t *testing.T, dir string, env map[string]string) (key, stderr string) {
	t.Helper()
	testutil.StubGH(t, tagGh)
	script, _ := filepath.Abs("release_tag.sh")
	key, exit, _, stderr := testutil.RunCheckIn(t, dir, script, env)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
	return key, stderr
}

const approvedRelease = "2026-10-13T10:00:00Z\task_release\tyes\n"

func TestReleaseTagRequiresApproval(t *testing.T) {
	cases := map[string]string{
		"no approval file": "",
		"answer no":        "2026-10-13T10:00:00Z\task_release\tno\n",
		"other state yes":  "2026-10-13T10:00:00Z\task\tyes\n",
	}
	for name, approvals := range cases {
		t.Run(name, func(t *testing.T) {
			dir, _, _, env := tagEnv(t, approvals)
			key, _ := runTag(t, dir, env)
			if key != "fail" {
				t.Fatalf("key=%q, want fail", key)
			}
			if remote := git(t, dir, "ls-remote", "--tags", "origin"); remote != "" {
				t.Fatalf("a tag was pushed without approval: %s", remote)
			}
		})
	}
}

func TestReleaseTagCreatesAnnotatedTag(t *testing.T) {
	dir, _, sha, env := tagEnv(t, approvedRelease)
	key, _ := runTag(t, dir, env)
	if key != "tagged" {
		t.Fatalf("key=%q, want tagged", key)
	}
	if got := git(t, dir, "ls-remote", "--tags", "origin", "refs/tags/v0.10.0"); !strings.Contains(got, "refs/tags/v0.10.0") {
		t.Fatalf("tag not on origin: %q", got)
	}
	if got := git(t, dir, "cat-file", "-t", "refs/tags/v0.10.0"); got != "tag" {
		t.Fatalf("tag object type %q, want annotated tag", got)
	}
	if got := git(t, dir, "rev-parse", "v0.10.0^{commit}"); got != sha {
		t.Fatalf("tag points to %s, want merge commit %s", got, sha)
	}
}

func TestReleaseTagExists(t *testing.T) {
	t.Run("on origin", func(t *testing.T) {
		dir, _, _, env := tagEnv(t, approvedRelease)
		git(t, dir, "tag", "-a", "v0.10.0", "-m", "old", "HEAD")
		git(t, dir, "push", "-q", "origin", "v0.10.0")
		before := git(t, dir, "ls-remote", "--tags", "origin")
		key, _ := runTag(t, dir, env)
		if key != "fail" {
			t.Fatalf("key=%q, want fail", key)
		}
		if after := git(t, dir, "ls-remote", "--tags", "origin"); after != before {
			t.Fatalf("remote tags changed: %q -> %q", before, after)
		}
	})
	t.Run("only local", func(t *testing.T) {
		dir, _, _, env := tagEnv(t, approvedRelease)
		git(t, dir, "tag", "-a", "v0.10.0", "-m", "old")
		key, _ := runTag(t, dir, env)
		if key != "fail" {
			t.Fatalf("key=%q, want fail", key)
		}
		if remote := git(t, dir, "ls-remote", "--tags", "origin"); remote != "" {
			t.Fatalf("a local tag was pushed: %s", remote)
		}
	})
}
