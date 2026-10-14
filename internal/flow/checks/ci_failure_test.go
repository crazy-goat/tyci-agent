package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// ciFailureGh answers gh from env vars: RUNS_JSON (the run list) and RUN_LOG (the failed log).
// Other calls fail.
const ciFailureGh = `
case "$*" in
  "run list "*) printf '%s' "$RUNS_JSON" ;;
  "run view "*) printf '%s\n' "$RUN_LOG" ;;
  *) exit 2 ;;
esac
`

// runCiFailure runs ci_failure.sh with the fake gh and returns the key and the artifact dir.
func runCiFailure(t *testing.T, runs, log string) (key, dir string) {
	t.Helper()
	testutil.StubGH(t, ciFailureGh)
	dir = t.TempDir()
	script, _ := filepath.Abs("ci_failure.sh")
	env := map[string]string{
		"TYCI_REPO": "o/r", "TYCI_DEFAULT_BRANCH": "main", "TYCI_ARTIFACT_DIR": dir,
		"RUNS_JSON": runs, "RUN_LOG": log,
	}
	key, exit, _, stderr := testutil.RunCheckIn(t, "", script, env)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
	return key, dir
}

const redRuns = `[{"databaseId":555,"conclusion":"failure","status":"completed"},{"databaseId":554,"conclusion":"success","status":"completed"}]`

func TestCiFailureRedaction(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	log := "setup ok\nTOKEN=" + token + "\nFAIL: TestX\nAuthorization: Bearer " + strings.Repeat("b", 20) + "\n"
	key, dir := runCiFailure(t, redRuns, log)
	if key != "red" {
		t.Fatalf("key=%q, want red", key)
	}
	b, err := os.ReadFile(filepath.Join(dir, "failure.log"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, token) || strings.Contains(got, strings.Repeat("b", 20)) {
		t.Fatalf("secret in failure.log: %q", got)
	}
	if !strings.Contains(got, "FAIL: TestX") {
		t.Fatalf("failure.log lost the failure line: %q", got)
	}
}

func TestCiFailureKeepsLast300Lines(t *testing.T) {
	var lines []string
	for i := 1; i <= 400; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	lines[399] = "the last line"
	key, dir := runCiFailure(t, redRuns, strings.Join(lines, "\n"))
	if key != "red" {
		t.Fatalf("key=%q", key)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "failure.log"))
	if n := strings.Count(string(b), "\n"); n != 300 || !strings.HasSuffix(string(b), "the last line\n") {
		t.Fatalf("failure.log has %d lines, want the last 300", n)
	}
}

func TestCiFailureGreen(t *testing.T) {
	runs := `[{"databaseId":560,"conclusion":"","status":"in_progress"},{"databaseId":559,"conclusion":"success","status":"completed"}]`
	key, dir := runCiFailure(t, runs, "should not be read")
	if key != "green" {
		t.Fatalf("key=%q, want green", key)
	}
	if _, err := os.Stat(filepath.Join(dir, "failure.log")); err == nil {
		t.Fatal("failure.log written for a green default branch")
	}
}
