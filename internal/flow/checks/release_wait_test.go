package checks

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// waitGh answers gh from env vars: RUNS_JSON (the run list, filtered by jq in the script) and
// ASSETS (the asset names, one per line, as --jq prints them). Other calls fail.
const waitGh = `
case "$*" in
  "run list "*) printf '%s' "$RUNS_JSON" ;;
  "release view "*) printf '%s\n' "$ASSETS" ;;
  *) exit 2 ;;
esac
`

func runWait(t *testing.T, env map[string]string) (key, stderr string) {
	t.Helper()
	testutil.StubGH(t, waitGh)
	base := map[string]string{"TYCI_REPO": "o/r", "TYCI_PARAM_MILESTONE": "v0.10.0", "TYCI_RELEASE_POLL": "0", "TYCI_RELEASE_TIMEOUT": "1800"}
	for k, v := range env {
		base[k] = v
	}
	script, _ := filepath.Abs("release_wait.sh")
	key, exit, _, stderr := testutil.RunCheckIn(t, "", script, base)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
	return key, stderr
}

const assetsOK = "tyci_linux_amd64\ntyci_darwin_arm64\nchecksums.txt"

func TestReleaseWaitFailed(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"run failed", map[string]string{
			"RUNS_JSON": `[{"headBranch":"v0.10.0","status":"completed","conclusion":"failure"}]`, "ASSETS": assetsOK}},
		{"no checksums", map[string]string{
			"RUNS_JSON": `[{"headBranch":"v0.10.0","status":"completed","conclusion":"success"}]`, "ASSETS": "tyci_linux_amd64"}},
		{"no binary", map[string]string{
			"RUNS_JSON": `[{"headBranch":"v0.10.0","status":"completed","conclusion":"success"}]`, "ASSETS": "checksums.txt"}},
		{"timeout while queued", map[string]string{
			"RUNS_JSON": `[{"headBranch":"v0.10.0","status":"queued","conclusion":null}]`, "TYCI_RELEASE_TIMEOUT": "0"}},
		{"run of another tag only", map[string]string{
			"RUNS_JSON": `[{"headBranch":"v0.9.0","status":"completed","conclusion":"success"}]`, "TYCI_RELEASE_TIMEOUT": "0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, stderr := runWait(t, tc.env)
			if key != "failed" {
				t.Fatalf("key=%q, want failed (stderr=%s)", key, stderr)
			}
			if !strings.Contains(stderr, "RESULT: fail") {
				t.Fatalf("stderr has no describe block: %q", stderr)
			}
		})
	}
}

func TestReleaseWaitOK(t *testing.T) {
	key, _ := runWait(t, map[string]string{
		"RUNS_JSON": `[{"headBranch":"v0.10.0","status":"completed","conclusion":"success"}]`, "ASSETS": assetsOK,
	})
	if key != "ok" {
		t.Fatalf("key=%q, want ok", key)
	}
}
