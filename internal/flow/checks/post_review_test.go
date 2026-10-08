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

// writeReview writes report.md into the artifact dir art and returns that dir,
// the value of TYCI_REVIEW_DIR.
func writeReview(t *testing.T, art, text string) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "artifacts", art)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "report.md"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

// runPostReview runs the check with TYCI_REVIEW_DIR set to reviewDir ("" for none).
func runPostReview(t *testing.T, reviewDir string, env map[string]string) (key string, exit int, body string) {
	t.Helper()
	key, exit, body, _ = runPostReviewErr(t, reviewDir, env)
	return key, exit, body
}

// runPostReviewErr is runPostReview that also returns stderr.
func runPostReviewErr(t *testing.T, reviewDir string, env map[string]string) (key string, exit int, body, stderr string) {
	t.Helper()
	logPath := testutil.StubGH(t, reviewGh)
	base := map[string]string{"TYCI_REPO": "o/r", "TYCI_PR": "9", "TYCI_REVIEW_DIR": reviewDir}
	for k, v := range env {
		base[k] = v
	}
	key, exit, stderr = testutil.RunCheck(t, "post_review.sh", base)
	b, _ := os.ReadFile(logPath + ".body")
	return key, exit, string(b), stderr
}

func TestPostReview_Body(t *testing.T) {
	key, exit, body := runPostReview(t, writeReview(t, "003-review", "ACCEPT\nlooks good\n"), nil)
	if key != "ok" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if !strings.HasPrefix(body, "<!-- tyci-agent -->\n") || !strings.Contains(body, "ACCEPT\nlooks good") {
		t.Errorf("body = %q", body)
	}
}

// #356: the review step may have any name; the report comes from TYCI_REVIEW_DIR.
func TestPostReview_ReviewStateWithOtherName(t *testing.T) {
	key, _, body := runPostReview(t, writeReview(t, "005-judge", "ACCEPT\nnewest\n"), nil)
	if key != "ok" || !strings.Contains(body, "ACCEPT\nnewest") {
		t.Fatalf("key=%q body=%q", key, body)
	}
}

func TestPostReview_FailStillExitsZero(t *testing.T) {
	for name, tc := range map[string]struct {
		review string
		env    map[string]string
	}{
		"gh fails":     {"ACCEPT\n", map[string]string{"GH_FAIL": "1"}},
		"empty review": {"", nil},
		"no PR":        {"ACCEPT\n", map[string]string{"TYCI_PR": ""}},
	} {
		key, exit, _, stderr := runPostReviewErr(t, writeReview(t, "003-review", tc.review), tc.env)
		if key != "fail" || exit != 0 {
			t.Errorf("%s: key=%q exit=%d", name, key, exit)
		}
		wantBlock(t, stderr, "fail", "goes on to ci")
	}
}

// #368: a run that continued an open PR has no review step; nothing is posted.
func TestPostReview_NoReviewSkips(t *testing.T) {
	key, exit, body := runPostReview(t, "", nil)
	if key != "skip" || exit != 0 || body != "" {
		t.Errorf("key=%q exit=%d body=%q", key, exit, body)
	}
}
