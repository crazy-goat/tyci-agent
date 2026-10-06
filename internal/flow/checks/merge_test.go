package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// mergeGh answers gh from env vars: CI_BUCKET, MERGE_STATE, REMOTE_HEAD, MERGE_FAIL.
const mergeGh = `
case "$*" in
  "pr checks"*) echo "[{\"name\":\"lint\",\"bucket\":\"fail\"},{\"name\":\"ci-ok\",\"bucket\":\"${CI_BUCKET:-pass}\"}]" | jq -r '[.[] | select(.name == "ci-ok")][0].bucket' ;;
  "pr view"*mergeStateStatus*) echo "${MERGE_STATE:-CLEAN}" ;;
  "pr view"*headRefOid*) echo "$REMOTE_HEAD" ;;
  "pr merge"*) [ -n "${MERGE_FAIL:-}" ] && { echo "merge refused" >&2; exit 1; }; exit 0 ;;
  *) exit 2 ;;
esac
`

// runMerge commits file on the issue branch (when not empty) and runs merge.sh.
func runMerge(t *testing.T, e pushEnv, file string, env map[string]string) (key string, stderr, log string, head string) {
	t.Helper()
	if file != "" {
		p := filepath.Join(e.work, file)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, e.work, "add", "-A")
		git(t, e.work, "commit", "-q", "-m", "change "+file)
	}
	head = git(t, e.work, "rev-parse", "HEAD")
	logPath := testutil.StubGH(t, mergeGh)
	base := map[string]string{"TYCI_PR": "171", "TYCI_REPO": "o/r", "TYCI_DEFAULT_BRANCH": "main", "REMOTE_HEAD": head}
	for k, v := range env {
		base[k] = v
	}
	script, _ := filepath.Abs("merge.sh")
	key, exit, _, stderr := testutil.RunCheckIn(t, e.work, script, base)
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, stderr)
	}
	b, _ := os.ReadFile(logPath)
	return key, stderr, string(b), head
}

func TestMerge_Merged(t *testing.T) {
	key, _, log, head := runMerge(t, newPushEnv(t), "a.txt", nil)
	if key != "merged" {
		t.Fatalf("key=%q", key)
	}
	want := "pr merge 171 -R o/r --squash --delete-branch --match-head-commit " + head
	if !strings.Contains(log, want) {
		t.Fatalf("log lacks %q:\n%s", want, log)
	}
}

func TestMerge_BehindPrintsBehindWithoutMerging(t *testing.T) {
	key, _, log, _ := runMerge(t, newPushEnv(t), "a.txt", map[string]string{"MERGE_STATE": "BEHIND"})
	if key != "behind" || strings.Contains(log, "pr merge") {
		t.Fatalf("key=%q log=%s", key, log)
	}
}

func TestMerge_CiNotGreenFails(t *testing.T) {
	key, _, log, _ := runMerge(t, newPushEnv(t), "a.txt", map[string]string{"CI_BUCKET": "pending"})
	if key != "fail" || strings.Contains(log, "pr merge") {
		t.Fatalf("key=%q log=%s", key, log)
	}
}

func TestMerge_ProtectedPath(t *testing.T) {
	for _, f := range []string{".github/workflows/x.yml", ".tyci/workflows/a.json", ".tyci/checks/c.sh", "internal/flow/checks/push.sh"} {
		t.Run(f, func(t *testing.T) {
			key, _, log, _ := runMerge(t, newPushEnv(t), f, nil)
			if key != "protected" || log != "" {
				t.Fatalf("key=%q log=%q", key, log)
			}
		})
	}
}

func TestMerge_NormalPathIsNotProtected(t *testing.T) {
	key, _, _, _ := runMerge(t, newPushEnv(t), "README.md", nil)
	if key != "merged" {
		t.Fatalf("key=%q", key)
	}
}

func TestMerge_FetchFailureFails(t *testing.T) {
	e := newPushEnv(t)
	git(t, e.work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	key, _, log, _ := runMerge(t, e, "", nil)
	if key != "fail" || log != "" {
		t.Fatalf("key=%q log=%q", key, log)
	}
}

func TestMerge_HeadMismatchFails(t *testing.T) {
	key, _, log, _ := runMerge(t, newPushEnv(t), "a.txt", map[string]string{"REMOTE_HEAD": "deadbeef"})
	if key != "fail" || strings.Contains(log, "pr merge") {
		t.Fatalf("key=%q log=%s", key, log)
	}
}

func TestMerge_MergeCommandFailsPrintsFail(t *testing.T) {
	key, stderr, _, _ := runMerge(t, newPushEnv(t), "a.txt", map[string]string{"MERGE_FAIL": "1"})
	if key != "fail" || !strings.Contains(stderr, "merge refused") {
		t.Fatalf("key=%q stderr=%s", key, stderr)
	}
}

func TestMerge_NeverUsesForbiddenFlags(t *testing.T) {
	b, err := os.ReadFile("merge.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"--admin", "--auto", "--merge", "--rebase"} {
		if strings.Contains(string(b), f) {
			t.Errorf("merge.sh contains %s", f)
		}
	}
}
