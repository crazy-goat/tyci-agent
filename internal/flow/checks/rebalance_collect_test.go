package checks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// rebalanceGh serves the open issues from $GH_ISSUES, the open milestones from $GH_MILESTONES and the
// permission of the logins alice (write) and mallory (read).
const rebalanceGh = `
case "$*" in
  *"issues?state=open"*) cat "$GH_ISSUES" ;;
  *"milestones?state=open"*) cat "$GH_MILESTONES" ;;
  *"/collaborators/alice/permission"*) echo write ;;
  *"/collaborators/mallory/permission"*) echo read ;;
  *) exit 2 ;;
esac
`

// runRebalanceCollect runs rebalance_collect.sh and returns the key, the candidate numbers and the
// milestone titles it wrote. Empty milestones means an empty list.
func runRebalanceCollect(t *testing.T, issues, milestones string) (key string, numbers []int, titles []string) {
	t.Helper()
	dir := t.TempDir()
	issuesPath := filepath.Join(dir, "issues.json")
	milesPath := filepath.Join(dir, "milestones.gh.json")
	if err := os.WriteFile(issuesPath, []byte(issues), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(milesPath, []byte(milestones), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.StubGH(t, rebalanceGh)
	env := map[string]string{"TYCI_REPO": "o/r", "TYCI_RUN_DIR": dir, "GH_ISSUES": issuesPath, "GH_MILESTONES": milesPath}
	key, exit, _ := testutil.RunCheck(t, "rebalance_collect.sh", env)
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
	m, err := os.ReadFile(filepath.Join(dir, "milestones.json"))
	if err != nil {
		t.Fatal(err)
	}
	var miles []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(m, &miles); err != nil {
		t.Fatal(err)
	}
	for _, ms := range miles {
		titles = append(titles, ms.Title)
	}
	return key, numbers, titles
}

func TestRebalanceCollect_FindsCandidatesAndMilestones(t *testing.T) {
	key, nums, titles := runRebalanceCollect(t, collectIssues, `[{"title":"v0.10.0","number":1},{"title":"v0.11.0","number":2}]`)
	if key != "found" {
		t.Fatalf("key=%q", key)
	}
	if len(nums) != 2 || nums[0] != 1 || nums[1] != 4 {
		t.Fatalf("candidates=%v, want [1 4] (no PR, no read-only author)", nums)
	}
	if len(titles) != 2 || titles[0] != "v0.10.0" || titles[1] != "v0.11.0" {
		t.Fatalf("milestones=%v", titles)
	}
}

func TestRebalanceCollect_NoCandidatesIsNone(t *testing.T) {
	key, nums, _ := runRebalanceCollect(t, `[{"number":2,"title":"b","body":"","user":{"login":"mallory"},"labels":[]}]`, `[]`)
	if key != "none" || len(nums) != 0 {
		t.Fatalf("key=%q candidates=%v", key, nums)
	}
}

func TestRebalanceCollect_MilestoneListFailureFails(t *testing.T) {
	dir := t.TempDir()
	issuesPath := filepath.Join(dir, "issues.json")
	if err := os.WriteFile(issuesPath, []byte(collectIssues), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.StubGH(t, rebalanceGh)
	// A missing milestones file makes the stub fail, as a gh outage does.
	env := map[string]string{"TYCI_REPO": "o/r", "TYCI_RUN_DIR": dir, "GH_ISSUES": issuesPath, "GH_MILESTONES": filepath.Join(dir, "missing.json")}
	key, exit, _ := testutil.RunCheck(t, "rebalance_collect.sh", env)
	if key != "fail" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
}

func TestRebalanceCollect_NoRunDirFails(t *testing.T) {
	testutil.StubGH(t, rebalanceGh)
	key, exit, _ := testutil.RunCheck(t, "rebalance_collect.sh", map[string]string{"TYCI_REPO": "o/r"})
	if key != "fail" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
}
