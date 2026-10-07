package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// commentsGh answers from env vars: PULLS and ISSUES (JSON, several documents
// model several pages), ME (own login), PERM_<user> (missing = 404).
const commentsGh = `
case "$*" in
  "api --paginate repos/o/r/pulls/5/comments") printf '%s\n' "$PULLS" ;;
  "api --paginate repos/o/r/issues/5/comments") printf '%s\n' "$ISSUES" ;;
  "api user -q .login") echo "$ME" ;;
  "api repos/o/r/collaborators/"*)
    u=${2#repos/o/r/collaborators/}; u=${u%/permission}; v=PERM_$u
    [ -z "${PERM_FAIL:-}" ] || { echo "gh: Server Error (HTTP 502)" >&2; exit 1; }
    [ -n "${!v:-}" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
    echo "${!v}" ;;
  *) exit 2 ;;
esac
`

func cmt(id int, user, body string) string {
	return `{"id":` + itoa(id) + `,"user":{"login":"` + user + `"},"html_url":"https://x/` + itoa(id) + `","body":"` + body + `"}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

type fetched struct {
	key, comments, lastID, log, stderr string
}

func runFetch(t *testing.T, env map[string]string) fetched {
	t.Helper()
	logPath := testutil.StubGH(t, commentsGh)
	dir, art := t.TempDir(), t.TempDir()
	base := map[string]string{"TYCI_REPO": "o/r", "TYCI_PR": "5", "TYCI_RUN_DIR": dir, "TYCI_ARTIFACT_DIR": art, "PULLS": "[]", "ISSUES": "[]", "ME": "me"}
	for k, v := range env {
		base[k] = v
	}
	key, exit, stderr := testutil.RunCheck(t, "fetch_comments.sh", base)
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	read := func(d, n string) string { b, _ := os.ReadFile(filepath.Join(d, n)); return string(b) }
	log, _ := os.ReadFile(logPath)
	return fetched{key, read(art, "comments.md"), strings.TrimSpace(read(dir, "last_comment_id")), string(log), stderr}
}

func TestFetchComments_FiltersNonTeam(t *testing.T) {
	f := runFetch(t, map[string]string{
		"PULLS":    "[" + cmt(1, "bob", "from reader") + "," + cmt(2, "alice", "from team") + "]",
		"PERM_bob": "read", "PERM_alice": "write",
	})
	if f.key != "new" || strings.Contains(f.comments, "from reader") || !strings.Contains(f.comments, "## Comment 1 by alice (https://x/2)\nfrom team") {
		t.Errorf("%+v", f)
	}
}

func TestFetchComments_Collaborator404Dropped(t *testing.T) {
	f := runFetch(t, map[string]string{"ISSUES": "[" + cmt(1, "stranger", "hi") + "]"})
	if f.key != "none" || f.comments != "" {
		t.Errorf("%+v", f)
	}
}

func TestFetchComments_NewerThanLastID(t *testing.T) {
	f := runFetch(t, map[string]string{
		"TYCI_LAST_REVIEW_COMMENT_ID": "10",
		"PULLS":                       "[" + cmt(9, "alice", "old") + "," + cmt(11, "alice", "newer") + "]",
		"PERM_alice":                  "admin",
	})
	if f.key != "new" || strings.Contains(f.comments, "old") || !strings.Contains(f.comments, "newer") {
		t.Errorf("%+v", f)
	}
}

func TestFetchComments_DropsOwnMarkedCommentsOnly(t *testing.T) {
	f := runFetch(t, map[string]string{
		"ME":         "alice",
		"PULLS":      "[" + cmt(1, "alice", "<!-- tyci-agent -->\\nreview") + "," + cmt(2, "alice", "real comment") + "]",
		"PERM_alice": "write",
	})
	if f.key != "new" || strings.Contains(f.comments, "review") || !strings.Contains(f.comments, "real comment") {
		t.Errorf("%+v", f)
	}
}

func TestFetchComments_Paginates(t *testing.T) {
	f := runFetch(t, map[string]string{
		"PULLS":      "[" + cmt(1, "alice", "page one") + "]\n[" + cmt(2, "alice", "page two") + "]",
		"PERM_alice": "write",
	})
	if !strings.Contains(f.comments, "page one") || !strings.Contains(f.comments, "page two") {
		t.Errorf("%+v", f)
	}
}

func TestFetchComments_PermissionCachedPerAuthor(t *testing.T) {
	f := runFetch(t, map[string]string{
		"PULLS":      "[" + cmt(1, "alice", "a") + "," + cmt(2, "alice", "b") + "]",
		"ISSUES":     "[" + cmt(3, "alice", "c") + "]",
		"PERM_alice": "write",
	})
	if n := strings.Count(f.log, "collaborators/alice/permission"); n != 1 {
		t.Errorf("permission calls = %d, want 1", n)
	}
}

func TestFetchComments_NoneWhenEmpty(t *testing.T) {
	f := runFetch(t, nil)
	if f.key != "none" || f.comments != "" {
		t.Errorf("%+v", f)
	}
}

func TestFetchComments_PrintsLastID(t *testing.T) {
	// The highest id counts even when its comment is dropped.
	f := runFetch(t, map[string]string{
		"ISSUES":     "[" + cmt(4, "alice", "keep") + "," + cmt(7, "bob", "drop") + "]",
		"PERM_alice": "write", "PERM_bob": "read",
	})
	if f.lastID != "7" {
		t.Errorf("last id = %q", f.lastID)
	}
}

func TestFetchComments_ReviewIDBelowIssueID(t *testing.T) {
	// Separate id sequences: a review comment with a low id must survive a high issue comment id.
	f := runFetch(t, map[string]string{
		"TYCI_LAST_COMMENT_ID": "3000000000",
		"PULLS":                "[" + cmt(2000000000, "alice", "diff comment") + "]",
		"ISSUES":               "[" + cmt(3000000005, "alice", "issue comment") + "]",
		"PERM_alice":           "write",
	})
	if f.key != "new" || !strings.Contains(f.comments, "diff comment") || !strings.Contains(f.comments, "issue comment") {
		t.Errorf("%+v", f)
	}
}

func TestFetchComments_PermissionErrorFailsAndKeepsMarks(t *testing.T) {
	f := runFetch(t, map[string]string{
		"PULLS":     "[" + cmt(5, "alice", "x") + "]",
		"PERM_FAIL": "1",
	})
	if f.key != "fail" || f.lastID != "" {
		t.Errorf("%+v", f)
	}
	wantBlock(t, f.stderr, "fail", "permission lookup failed for alice")
}
