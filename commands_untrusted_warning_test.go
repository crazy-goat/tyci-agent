package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRunUntrustedWarningNamesAllSkippedContent(t *testing.T) {
	cmd := exec.Command(binPath, "run", "--no-mcp", "--model", "missing-warning-provider/model", "--prompt", "hello")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+testDir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected the intentionally missing provider to stop the run")
	}
	got := string(out)
	for _, item := range []string{".tyci/hooks.json", ".tyci/tools/", "local cron dir", "mcp.json", "Global ~/.tyci/", "~/.tyci/trust.json"} {
		if !strings.Contains(got, item) {
			t.Errorf("warning missing %q: %s", item, got)
		}
	}
	if n := strings.Count(got, "this project is not trusted"); n != 1 {
		t.Errorf("want one warning, got %d: %s", n, got)
	}
	if strings.Contains(got, "not discoverable by name") {
		t.Errorf("run must not warn about workflow discovery: %s", got)
	}
}
