package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// releaseGh answers gh from env vars: MS_JSON (the milestone list, filtered by jq in the script)
// and CI_CONCLUSION (the jq result of the ci-ok check run). Other calls fail.
const releaseGh = `
case "$*" in
  "api --paginate repos/o/r/milestones"*) printf '%s' "$MS_JSON" ;;
  "api repos/o/r/commits/main/check-runs"*) printf '%s\n' "${CI_CONCLUSION:-}" ;;
  *) exit 2 ;;
esac
`

const changelogReady = "# Changelog\n\n## [Unreleased]\n\n### Added\n- Flow: something new (#1).\n\n## [0.9.0] - 2026-10-13\n\n- Old entry.\n"

// releaseClone makes a bare origin with a clone (the cwd of the check) that holds CHANGELOG.md.
func releaseClone(t *testing.T, changelog string) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	git(t, root, "init", "-q", "--bare", "-b", "main", origin)
	git(t, root, "clone", "-q", origin, work)
	git(t, work, "checkout", "-q", "-b", "main")
	git(t, work, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, work, "push", "-q", "origin", "main")
	if err := os.WriteFile(filepath.Join(work, "CHANGELOG.md"), []byte(changelog), 0o600); err != nil {
		t.Fatal(err)
	}
	return work
}

func TestReleasePreflight(t *testing.T) {
	const milestoneOpen0 = `[{"title":"v0.10.0","open_issues":0,"closed_issues":3}]`
	cases := []struct {
		name      string
		ms        string
		ci        string
		changelog string
		tag       bool
		want      string
	}{
		{"ready", milestoneOpen0, "success", changelogReady, false, "ready"},
		{"open issues", `[{"title":"v0.10.0","open_issues":2,"closed_issues":1}]`, "success", changelogReady, false, "fail"},
		{"milestone missing", `[{"title":"v0.9.0","open_issues":0,"closed_issues":1}]`, "success", changelogReady, false, "fail"},
		{"ci not green", milestoneOpen0, "failure", changelogReady, false, "fail"},
		{"ci missing", milestoneOpen0, "", changelogReady, false, "fail"},
		{"tag exists", milestoneOpen0, "success", changelogReady, true, "fail"},
		{"empty unreleased", milestoneOpen0, "success", "# Changelog\n\n## [Unreleased]\n\n## [0.9.0] - 2026-10-13\n\n- Old entry.\n", false, "fail"},
	}
	script, _ := filepath.Abs("release_preflight.sh")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := releaseClone(t, tc.changelog)
			if tc.tag {
				git(t, dir, "tag", "-a", "v0.10.0", "-m", "Release v0.10.0")
				git(t, dir, "push", "-q", "origin", "v0.10.0")
			}
			testutil.StubGH(t, releaseGh)
			env := map[string]string{
				"TYCI_REPO": "o/r", "TYCI_DEFAULT_BRANCH": "main", "TYCI_PARAM_MILESTONE": "v0.10.0",
				"TYCI_RUN_DIR": t.TempDir(), "MS_JSON": tc.ms, "CI_CONCLUSION": tc.ci,
			}
			key, exit, stdout, stderr := testutil.RunCheckIn(t, dir, script, env)
			if exit != 0 || key != tc.want {
				t.Fatalf("key=%q exit=%d stdout=%q stderr=%q, want %q", key, exit, stdout, stderr, tc.want)
			}
			if tc.want == "fail" && !strings.Contains(stderr, "RESULT: fail") {
				t.Fatalf("stderr has no describe block: %q", stderr)
			}
		})
	}
}
