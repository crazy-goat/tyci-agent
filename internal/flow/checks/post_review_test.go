package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// reviewGh saves the --body-file content of `pr review` into $GH_LOG.body.
const reviewGh = `
case "$*" in
  "pr review 9 -R o/r --comment --body-file "*)
    [ -n "${GH_FAIL:-}" ] && exit 1
    cat "${*: -1}" > "$GH_LOG.body" ;;
  *) exit 2 ;;
esac
`

func writeArtifact(t *testing.T, runDir, art, name, text string) {
	t.Helper()
	d := filepath.Join(runDir, "artifacts", art)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runPostReview(t *testing.T, review string, env map[string]string) (key string, exit int, body string) {
	t.Helper()
	dir := t.TempDir()
	if review != "" {
		writeArtifact(t, dir, "003-review", "report.md", review)
	}
	return runPostReviewIn(t, dir, env)
}

func runPostReviewIn(t *testing.T, dir string, env map[string]string) (key string, exit int, body string) {
	t.Helper()
	logPath := testutil.StubGH(t, reviewGh)
	base := map[string]string{"TYCI_REPO": "o/r", "TYCI_PR": "9", "TYCI_RUN_DIR": dir}
	for k, v := range env {
		base[k] = v
	}
	key, exit, _ = testutil.RunCheck(t, "post_review.sh", base)
	b, _ := os.ReadFile(logPath + ".body")
	return key, exit, string(b)
}

func TestPostReview_Body(t *testing.T) {
	key, exit, body := runPostReview(t, "ACCEPT\nlooks good\n", nil)
	if key != "ok" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if !strings.HasPrefix(body, "<!-- tyci-agent -->\n") || !strings.Contains(body, "ACCEPT\nlooks good") {
		t.Errorf("body = %q", body)
	}
}

func TestPostReview_FailStillExitsZero(t *testing.T) {
	for name, tc := range map[string]struct {
		review string
		env    map[string]string
	}{
		"gh fails":  {"ACCEPT\n", map[string]string{"GH_FAIL": "1"}},
		"no review": {"", nil},
		"no PR":     {"ACCEPT\n", map[string]string{"TYCI_PR": ""}},
	} {
		key, exit, _ := runPostReview(t, tc.review, tc.env)
		if key != "fail" || exit != 0 {
			t.Errorf("%s: key=%q exit=%d", name, key, exit)
		}
	}
}

// #340: the newest review report wins (numeric order, not text order).
func TestPostReview_PostsNewestReviewReport(t *testing.T) {
	dir := t.TempDir()
	writeArtifact(t, dir, "003-review", "report.md", "CHANGES\nold\n")
	writeArtifact(t, dir, "999-review", "report.md", "CHANGES\nolder\n")
	writeArtifact(t, dir, "1000-review", "report.md", "ACCEPT\nnewest\n")
	writeArtifact(t, dir, "1001-code", "report.md", "worker report\n")
	key, _, body := runPostReviewIn(t, dir, nil)
	if key != "ok" || !strings.Contains(body, "ACCEPT\nnewest") {
		t.Fatalf("key=%q body=%q", key, body)
	}
}
