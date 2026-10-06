package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// seqGh prints $GH_SEQ_DIR/<n>.json for call n (1-based), or exits 8 for <n>.err.
// The last file repeats when calls run past it.
const seqGh = `
n=$(cat "$GH_SEQ_DIR/n" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$GH_SEQ_DIR/n"
while [ "$n" -gt 1 ] && [ ! -e "$GH_SEQ_DIR/$n.json" ] && [ ! -e "$GH_SEQ_DIR/$n.err" ]; do n=$((n-1)); done
[ -e "$GH_SEQ_DIR/$n.err" ] && exit 8
cat "$GH_SEQ_DIR/$n.json"
`

func ciJSON(bucket string) string {
	return `[{"name":"lint","state":"FAILURE","bucket":"fail"},{"name":"ci-ok","state":"X","bucket":"` + bucket + `"}]`
}

// runCI writes the sequence ("err" for a failing call) and runs ci_wait.sh.
func runCI(t *testing.T, seq []string, env map[string]string) (key string, exit int, calls int) {
	t.Helper()
	dir := t.TempDir()
	for i, s := range seq {
		name := filepath.Join(dir, string(rune('1'+i)))
		if s == "err" {
			name += ".err"
		} else {
			name += ".json"
		}
		if err := os.WriteFile(name, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	testutil.StubGH(t, seqGh)
	t.Setenv("GH_SEQ_DIR", dir)
	base := map[string]string{"TYCI_REPO": "o/r", "TYCI_PR": "5", "TYCI_CI_POLL_SEC": "0", "GH_SEQ_DIR": dir}
	for k, v := range env {
		base[k] = v
	}
	key, exit, _ = testutil.RunCheck(t, "ci_wait.sh", base)
	b, _ := os.ReadFile(filepath.Join(dir, "n"))
	return key, exit, callsOf(string(b))
}

func callsOf(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		n = n*10 + int(r-'0')
	}
	return n
}

func TestCIWait_PendingThenPass(t *testing.T) {
	key, _, calls := runCI(t, []string{ciJSON("pending"), ciJSON("pending"), ciJSON("pass")}, nil)
	if key != "green" || calls != 3 {
		t.Fatalf("key=%q calls=%d", key, calls)
	}
}

func TestCIWait_Fail(t *testing.T) {
	if key, _, _ := runCI(t, []string{ciJSON("fail")}, nil); key != "red" {
		t.Fatalf("key=%q", key)
	}
}

func TestCIWait_Cancelled(t *testing.T) {
	if key, _, _ := runCI(t, []string{ciJSON("cancel")}, nil); key != "red" {
		t.Fatalf("key=%q", key)
	}
}

func TestCIWait_OtherCheckFailsCiOkPasses(t *testing.T) {
	if key, _, _ := runCI(t, []string{ciJSON("pass")}, nil); key != "green" {
		t.Fatalf("key=%q", key)
	}
}

func TestCIWait_CheckNeverAppears(t *testing.T) {
	start := time.Now()
	key, exit, _ := runCI(t, []string{`[{"name":"lint","bucket":"pass"}]`}, map[string]string{"TYCI_CI_APPEAR_SEC": "1", "TYCI_CI_POLL_SEC": "1"})
	if key != "fail" || exit != 0 || time.Since(start) > 10*time.Second {
		t.Fatalf("key=%q exit=%d after %v", key, exit, time.Since(start))
	}
}

func TestCIWait_NoPRNumberFails(t *testing.T) {
	key, exit, _ := runCI(t, []string{ciJSON("pass")}, map[string]string{"TYCI_PR": ""})
	if key != "fail" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
}

func TestCIWait_TransientGhErrorRetries(t *testing.T) {
	if key, _, _ := runCI(t, []string{"err", ciJSON("pass")}, nil); key != "green" {
		t.Fatalf("key=%q", key)
	}
}

func TestCIWait_ThreeGhErrorsFails(t *testing.T) {
	key, _, calls := runCI(t, []string{"err", "err", "err", ciJSON("pass")}, nil)
	if key != "fail" || calls != 3 {
		t.Fatalf("key=%q calls=%d", key, calls)
	}
}

func TestCIWait_PrintsKeyAsLastLine(t *testing.T) {
	// RunCheck already takes the last stdout line as the key; also require one line only.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(ciJSON("pass")), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.StubGH(t, seqGh)
	_, _, stdout, _ := testutil.RunCheckOut(t, "ci_wait.sh", map[string]string{"TYCI_REPO": "o/r", "TYCI_PR": "5", "TYCI_CI_POLL_SEC": "0", "GH_SEQ_DIR": dir})
	if stdout != "green\n" {
		t.Fatalf("stdout=%q", stdout)
	}
}
