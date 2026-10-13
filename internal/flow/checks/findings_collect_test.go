package checks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// collectGh serves the open issues from $GH_ISSUES and the permission of the logins alice (write)
// and mallory (read).
const collectGh = `
case "$*" in
  *"issues?state=open"*) cat "$GH_ISSUES" ;;
  *"/collaborators/alice/permission"*) echo write ;;
  *"/collaborators/mallory/permission"*) echo read ;;
  *) exit 2 ;;
esac
`

// runCollect runs findings_collect.sh on issues (the gh issue list) and returns the key and the candidate numbers.
func runCollect(t *testing.T, issues string, extra map[string]string) (key string, numbers []int) {
	t.Helper()
	dir := t.TempDir()
	issuesPath := filepath.Join(dir, "issues.json")
	if err := os.WriteFile(issuesPath, []byte(issues), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.StubGH(t, collectGh)
	env := map[string]string{"TYCI_REPO": "o/r", "TYCI_RUN_DIR": dir, "GH_ISSUES": issuesPath}
	for k, v := range extra {
		env[k] = v
	}
	key, exit, _ := testutil.RunCheck(t, "findings_collect.sh", env)
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	b, err := os.ReadFile(filepath.Join(dir, "candidates.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cands []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(b, &cands); err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		numbers = append(numbers, c.Number)
	}
	return key, numbers
}

const collectIssues = `[
 {"number":1,"title":"a","body":"x","user":{"login":"alice"},"labels":[]},
 {"number":2,"title":"b","body":"x","user":{"login":"mallory"},"labels":[]},
 {"number":3,"title":"pr","body":"x","user":{"login":"alice"},"labels":[],"pull_request":{}},
 {"number":4,"title":"skip","body":"x","user":{"login":"alice"},"labels":[{"name":"wontfix"}]}
]`

func TestFindingsCollect_FiltersAuthorsPullRequestsAndLabels(t *testing.T) {
	key, nums := runCollect(t, collectIssues, map[string]string{"TYCI_EXCLUDE_LABEL": "wontfix"})
	if key != "found" {
		t.Fatalf("key=%q", key)
	}
	if len(nums) != 1 || nums[0] != 1 {
		t.Fatalf("candidates=%v, want [1]", nums)
	}
}

func TestFindingsCollect_NoCandidates(t *testing.T) {
	key, nums := runCollect(t, `[{"number":2,"title":"b","body":"","user":{"login":"mallory"},"labels":[]}]`, nil)
	if key != "none" || len(nums) != 0 {
		t.Fatalf("key=%q candidates=%v", key, nums)
	}
}

func TestFindingsCollect_NoRunDirFails(t *testing.T) {
	testutil.StubGH(t, collectGh)
	key, exit, _ := testutil.RunCheck(t, "findings_collect.sh", map[string]string{"TYCI_REPO": "o/r"})
	if key != "fail" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
}
