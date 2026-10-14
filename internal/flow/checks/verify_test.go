package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// verifyGo is a fake go for verify.sh. 'go test' prints a failing test with a token and fails
// when TEST_FAIL is set. It never reaches the network.
const verifyGo = `
if [ "$1" = test ] && [ -n "${TEST_FAIL:-}" ]; then
  echo "--- FAIL: TestX"
  echo "TOKEN=ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  exit 1
fi
echo "ok $1"
exit 0
`

// runVerify runs verify.sh in a temp worktree with the fake go and returns the key and the artifact dir.
func runVerify(t *testing.T, env map[string]string) (key, dir string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/usr/bin/env bash\n"+verifyGo), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir = t.TempDir()
	script, _ := filepath.Abs("verify.sh")
	base := map[string]string{"TYCI_ARTIFACT_DIR": dir}
	for k, v := range env {
		base[k] = v
	}
	key, exit, _, stderr := testutil.RunCheckIn(t, t.TempDir(), script, base)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr)
	}
	return key, dir
}

func TestVerifyRed(t *testing.T) {
	key, dir := runVerify(t, map[string]string{"TEST_FAIL": "1"})
	if key != "red" {
		t.Fatalf("key=%q, want red", key)
	}
	b, err := os.ReadFile(filepath.Join(dir, "verify.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "--- FAIL: TestX") {
		t.Fatalf("verify.log lacks the failure: %s", b)
	}
	if strings.Contains(string(b), "ghp_aaaa") {
		t.Fatalf("verify.log has the token: %s", b)
	}
}

func TestVerifyGreen(t *testing.T) {
	key, dir := runVerify(t, nil)
	if key != "green" {
		t.Fatalf("key=%q, want green", key)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "verify.log")); !strings.Contains(string(b), "ok test") {
		t.Fatalf("verify.log = %q", b)
	}
}
