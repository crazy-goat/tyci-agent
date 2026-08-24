package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCronListCLI(t *testing.T) {
	temp := t.TempDir()
	data := map[string]any{"jobs": []map[string]any{{
		"name": "nightly", "prompt": "run tests", "dir": temp, "schedule": "every 1h",
	}}}
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(testDir, ".tyci")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, "cron.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binPath, "cron", "list")
	cmd.Dir = temp
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cron list: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "nightly") || !strings.Contains(string(out), "every 1h") {
		t.Fatalf("cron list output = %q", out)
	}
}

func TestCronRunRequiresExistingName(t *testing.T) {
	cmd := exec.Command(binPath, "cron", "run", "missing")
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected missing cron job to fail")
	}
	if !strings.Contains(string(out), `cron job "missing" not found`) {
		t.Fatalf("output = %q", out)
	}
}
