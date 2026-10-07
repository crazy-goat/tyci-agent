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

func runPostReview(t *testing.T, review string, env map[string]string) (key string, exit int, body string) {
	t.Helper()
	logPath := testutil.StubGH(t, reviewGh)
	dir := t.TempDir()
	if review != "" {
		if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte(review), 0o644); err != nil {
			t.Fatal(err)
		}
	}
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
