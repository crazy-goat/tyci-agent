package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// rebalanceApplyGh accepts the milestone creation and the issue edits of a rebalance run and rejects other calls.
const rebalanceApplyGh = `
case "$*" in
  "api -X POST repos/o/r/milestones -f title="*) ;;
  "issue edit "*" -R o/r --milestone "*) ;;
  *) exit 2 ;;
esac
`

// setupRebalanceApply writes proposal.json, the open milestones and the split answer of a run.
func setupRebalanceApply(t *testing.T, proposal, answer string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"proposal.json":   proposal,
		"milestones.json": `[{"title":"v0.10.0"}]`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	split := filepath.Join(dir, "artifacts", "001-split")
	if err := os.MkdirAll(split, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(split, "output.txt"), []byte(answer), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runRebalanceApply runs rebalance_apply.sh and returns the key and the gh calls it made.
func runRebalanceApply(t *testing.T, dir string) (key, log string) {
	t.Helper()
	logPath := testutil.StubGH(t, rebalanceApplyGh)
	key, exit, _ := testutil.RunCheck(t, "rebalance_apply.sh", map[string]string{"TYCI_REPO": "o/r", "TYCI_RUN_DIR": dir})
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	b, _ := os.ReadFile(logPath)
	return key, string(b)
}

const applyProposal = `{"assign":{"v0.10.0":[1],"v0.11.0":[2]},"create":["v0.11.0"]}`

func TestRebalanceApply_CreatesMilestoneAndAssigns(t *testing.T) {
	dir := setupRebalanceApply(t, applyProposal, `{"assign":{},"create":[]}`)
	key, log := runRebalanceApply(t, dir)
	if key != "done" {
		t.Fatalf("key=%q", key)
	}
	for _, want := range []string{
		"api -X POST repos/o/r/milestones -f title=v0.11.0",
		"issue edit 1 -R o/r --milestone v0.10.0",
		"issue edit 2 -R o/r --milestone v0.11.0",
	} {
		if strings.Count(log, want) != 1 {
			t.Fatalf("log=%q, want one call %q", log, want)
		}
	}
	if strings.Count(log, "milestones") != 1 {
		t.Fatalf("log=%q, want one milestone creation", log)
	}
}

func TestRebalanceApply_Idempotent(t *testing.T) {
	dir := setupRebalanceApply(t, applyProposal, `{"assign":{},"create":[]}`)
	runRebalanceApply(t, dir)
	key, log := runRebalanceApply(t, dir)
	if key != "done" || log != "" {
		t.Fatalf("second run: key=%q log=%q, want no gh call", key, log)
	}
}

func TestRebalanceApply_UsesValidatedCopy(t *testing.T) {
	// The answer of the split step names issue 2 for v0.10.0; the validated copy does not.
	dir := setupRebalanceApply(t, `{"assign":{"v0.10.0":[1]},"create":[]}`, `{"assign":{"v0.10.0":[1,2]},"create":[]}`)
	key, log := runRebalanceApply(t, dir)
	if key != "done" || strings.Contains(log, "issue edit 2") {
		t.Fatalf("key=%q log=%q", key, log)
	}
}

func TestRebalanceApply_ExistingMilestoneIsNotCreated(t *testing.T) {
	dir := setupRebalanceApply(t, `{"assign":{"v0.10.0":[1,2]},"create":["v0.10.0"]}`, "")
	_, log := runRebalanceApply(t, dir)
	if strings.Contains(log, "milestones") {
		t.Fatalf("log=%q, want no milestone creation for an open milestone", log)
	}
}

func TestRebalanceApply_NoProposalFails(t *testing.T) {
	dir := t.TempDir()
	testutil.StubGH(t, rebalanceApplyGh)
	key, exit, _ := testutil.RunCheck(t, "rebalance_apply.sh", map[string]string{"TYCI_REPO": "o/r", "TYCI_RUN_DIR": dir})
	if key != "fail" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
}
