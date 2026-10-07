package runlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/redact"
	"github.com/crazy-goat/tyci-agent/session"
)

const tok = "tok-exact-secret-value-123"

func TestWriter_NeverWritesSecret(t *testing.T) {
	redact.Add(tok)
	dir := t.TempDir()
	w, err := Open(dir, "worker", 1, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	ghp := "ghp_" + strings.Repeat("z", 36)
	err = w.Write("assistant", []session.ContentBlock{
		{Type: "text", Text: "use " + tok},
		{Type: "tool_use", Name: "bash", Arguments: []byte(`{"cmd":"echo ` + ghp + `"}`)},
		{Type: "tool_result", Text: "OPENAI_API_KEY=" + tok},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	b, err := os.ReadFile(Path(dir, "worker", 1))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), tok) || strings.Contains(string(b), ghp) {
		t.Fatalf("secret on disk: %s", b)
	}
	if !strings.Contains(string(b), "[REDACTED]") {
		t.Fatalf("no mask: %s", b)
	}
}

func TestWriter_FileModes(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, "review", 2, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	fi, _ := os.Stat(Path(dir, "review", 2))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Join(dir, "agents"))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", di.Mode().Perm())
	}
}

func TestWriter_NeverAppendsToOldFile(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, "worker", 1, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	old, _ := os.ReadFile(Path(dir, "worker", 1))
	if _, err := Open(dir, "worker", 1, dir, "m", "p"); err == nil {
		t.Fatal("reopening seq 1 must fail")
	}
	w2, err := Open(dir, "worker", 2, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	_ = w2.Close()
	again, _ := os.ReadFile(Path(dir, "worker", 1))
	if string(old) != string(again) {
		t.Error("old file changed")
	}
}

func mkRun(t *testing.T, root, name, status string, age time.Duration) string {
	t.Helper()
	d := filepath.Join(root, "repo", name)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "state.json")
	if err := os.WriteFile(p, []byte(`{"status":"`+status+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return d
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestPrune(t *testing.T) {
	root := t.TempDir()
	day := 24 * time.Hour
	doneOld := mkRun(t, root, "a", "done", 31*day)
	failedOld := mkRun(t, root, "b", "failed", 40*day)
	running := mkRun(t, root, "c", "running", 90*day)
	paused := mkRun(t, root, "d", "paused", 90*day)
	doneNew := mkRun(t, root, "e", "done", 2*day)

	n, err := Prune(root, 30, time.Now())
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if exists(doneOld) || exists(failedOld) {
		t.Error("old finished runs must go")
	}
	if !exists(running) || !exists(paused) || !exists(doneNew) {
		t.Error("running, paused and new runs must stay")
	}
}

func TestPrune_ZeroKeepsAll(t *testing.T) {
	root := t.TempDir()
	d := mkRun(t, root, "a", "done", 500*24*time.Hour)
	if n, err := Prune(root, 0, time.Now()); err != nil || n != 0 || !exists(d) {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestPrune_NegativeIsConfigError(t *testing.T) {
	_, err := Prune(t.TempDir(), -1, time.Now())
	if err == nil || !strings.Contains(err.Error(), "logs.retention_days") {
		t.Fatalf("err=%v", err)
	}
}
