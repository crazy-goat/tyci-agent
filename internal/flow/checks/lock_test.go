package checks

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

func runLock(t *testing.T, home, runDir string) string {
	t.Helper()
	abs, _ := filepath.Abs("lock.sh")
	key, _, _ := testutil.RunCheck(t, abs, map[string]string{"HOME": home, "TYCI_REPO": "o/r", "TYCI_RUN_DIR": runDir, "TYCI_LOCK_POLL_SEC": "0.1"})
	return key
}

func holder(t *testing.T, home, runDir, state string, pid string) {
	t.Helper()
	d := filepath.Join(home, ".tyci", "locks", "o_r.merge")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, "owner"), []byte(pid+"\n"+runDir+"\n"), 0o644)
	os.WriteFile(filepath.Join(runDir, "state.json"), []byte(`{"status":"running","current":"`+state+`"}`), 0o644)
}

func TestLock_FreeTakes(t *testing.T) {
	if key := runLock(t, t.TempDir(), t.TempDir()); key != "ok" {
		t.Fatalf("key=%q", key)
	}
}

func TestLock_StaleHolderIsTaken(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	holder(t, home, other, "ask", itoa(os.Getpid()))
	if key := runLock(t, home, t.TempDir()); key != "ok" {
		t.Fatalf("key=%q", key)
	}
}

func TestLock_DeadHolderIsTaken(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	holder(t, home, other, "ci", "999999999")
	if key := runLock(t, home, t.TempDir()); key != "ok" {
		t.Fatalf("key=%q", key)
	}
}

func TestLock_WaitsForActiveHolder(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	holder(t, home, other, "ci", itoa(os.Getpid()))
	go func() {
		time.Sleep(600 * time.Millisecond)
		os.WriteFile(filepath.Join(other, "state.json"), []byte(`{"status":"running","current":"findings"}`), 0o644)
	}()
	start := time.Now()
	if key := runLock(t, home, t.TempDir()); key != "ok" {
		t.Fatalf("key=%q", key)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Fatal("did not wait for the holder")
	}
}

func TestLock_FailedHolderIsTaken(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	holder(t, home, other, "ci", itoa(os.Getpid()))
	os.WriteFile(filepath.Join(other, "state.json"), []byte(`{"status":"failed","current":"ci"}`), 0o644)
	if key := runLock(t, home, t.TempDir()); key != "ok" {
		t.Fatalf("key=%q", key)
	}
}
