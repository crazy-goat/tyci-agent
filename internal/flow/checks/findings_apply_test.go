package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// applyGh accepts `issue edit N -R o/r --milestone v1` and rejects other calls.
const applyGh = `
case "$*" in
  "issue edit "*" -R o/r --milestone v1") ;;
  *) exit 2 ;;
esac
`

// setupApply writes candidates.json (candidate issue numbers 1 and 2) and the pick answer of a run.
// approvals is written to approvals.txt when not empty.
func setupApply(t *testing.T, answer, approvals string) string {
	t.Helper()
	dir := t.TempDir()
	cands := `[{"number":1,"title":"a","body":""},{"number":2,"title":"b","body":""}]`
	if err := os.WriteFile(filepath.Join(dir, "candidates.json"), []byte(cands), 0o600); err != nil {
		t.Fatal(err)
	}
	pick := filepath.Join(dir, "artifacts", "001-pick")
	if err := os.MkdirAll(pick, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pick, "output.txt"), []byte(answer), 0o600); err != nil {
		t.Fatal(err)
	}
	if approvals != "" {
		if err := os.WriteFile(filepath.Join(dir, "approvals.txt"), []byte(approvals), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runApply(t *testing.T, dir string) (key string, log string) {
	t.Helper()
	logPath := testutil.StubGH(t, applyGh)
	env := map[string]string{"TYCI_REPO": "o/r", "TYCI_RUN_DIR": dir, "TYCI_PARAM_MILESTONE": "v1"}
	key, exit, _ := testutil.RunCheck(t, "findings_apply.sh", env)
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	b, _ := os.ReadFile(logPath)
	return key, string(b)
}

func TestFindingsApply_CriticalMoves(t *testing.T) {
	dir := setupApply(t, `{"critical":[1],"doubt":[]}`, "")
	key, log := runApply(t, dir)
	if key != "done" || strings.Count(log, "issue edit 1 -R o/r --milestone v1") != 1 {
		t.Fatalf("key=%q log=%q", key, log)
	}
}

func TestFindingsApply_ForeignNumberMovesNothing(t *testing.T) {
	dir := setupApply(t, `{"critical":[1],"doubt":[99]}`, "")
	key, log := runApply(t, dir)
	if key != "fail" || strings.Contains(log, "issue edit") {
		t.Fatalf("key=%q log=%q", key, log)
	}
}

func TestFindingsApply_Idempotent(t *testing.T) {
	dir := setupApply(t, `{"critical":[1],"doubt":[]}`, "")
	runApply(t, dir)
	key, log := runApply(t, dir)
	if key != "done" || strings.Count(log, "issue edit") != 0 {
		t.Fatalf("second run: key=%q log=%q", key, log)
	}
}

func TestFindingsApply_DoubtWaitsForApproval(t *testing.T) {
	dir := setupApply(t, `{"critical":[],"doubt":[2]}`, "")
	key, log := runApply(t, dir)
	if key != "doubt" || strings.Contains(log, "issue edit") {
		t.Fatalf("key=%q log=%q", key, log)
	}
}

func TestFindingsApply_DoubtMovesAfterYes(t *testing.T) {
	dir := setupApply(t, `{"critical":[],"doubt":[2]}`, "2025-01-01T00:00:00Z\task\tyes\n")
	key, log := runApply(t, dir)
	if key != "done" || strings.Count(log, "issue edit 2 -R o/r --milestone v1") != 1 {
		t.Fatalf("key=%q log=%q", key, log)
	}
}

func TestFindingsApply_BadAnswerFails(t *testing.T) {
	dir := setupApply(t, `not json`, "")
	key, log := runApply(t, dir)
	if key != "fail" || strings.Contains(log, "issue edit") {
		t.Fatalf("key=%q log=%q", key, log)
	}
}
