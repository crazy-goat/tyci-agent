package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/session"
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

func TestCronTickRunsWithNoInteractiveSessionAndSkipsWhatIsNotDue(t *testing.T) {
	temp := t.TempDir()
	data := map[string]any{"jobs": []map[string]any{{
		"name": "future", "prompt": "not yet", "dir": temp, "schedule": "every 1h",
		"last_run": time.Now().Format(time.RFC3339),
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

	// tick is a plain one-shot invocation of the binary — nothing else of
	// tyci's is running, no console/TUI session, which is the whole point:
	// it must work exactly like this so an OS scheduler can call it.
	cmd := exec.Command(binPath, "cron", "tick")
	cmd.Dir = temp
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cron tick: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "no jobs due") {
		t.Fatalf("cron tick output = %q, want the not-yet-due job skipped", out)
	}
	if _, statErr := os.Stat(filepath.Join(global, "cron-logs", "future.log")); !os.IsNotExist(statErr) {
		t.Fatalf("a job that is not due must not have run: %v", statErr)
	}
}

func TestCronTickDispatchesADueJob(t *testing.T) {
	temp := t.TempDir()
	data := map[string]any{"jobs": []map[string]any{{
		"name": "due-now", "prompt": "look busy", "dir": temp, "schedule": "every 1h",
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

	cmd := exec.Command(binPath, "cron", "tick")
	cmd.Dir = temp
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cron tick: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ran 1 due job(s)") {
		t.Fatalf("cron tick output = %q, want the due job reported as dispatched", out)
	}
	// Regardless of whether the dispatched `tyci run` succeeded (it has no
	// provider configured here), a log must exist recording the attempt —
	// proof the job was actually started, not merely recognized as due.
	if _, statErr := os.Stat(filepath.Join(global, "cron-logs", "due-now.log")); statErr != nil {
		t.Fatalf("expected a log for the dispatched job: %v", statErr)
	}
}

func TestCronListFromSubdirectoryUsesProjectRootCronFile(t *testing.T) {
	project := t.TempDir()
	subdir := filepath.Join(project, "nested", "work")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-q", project)
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	local := filepath.Join(project, ".tyci")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	jobs, err := json.Marshal(map[string]any{"jobs": []map[string]any{{
		"name": "root-job", "prompt": "run from project root", "dir": project, "schedule": "every 1h",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "cron.json"), jobs, 0o644); err != nil {
		t.Fatal(err)
	}
	projectKey, err := session.ProjectKey(project)
	if err != nil {
		t.Fatal(err)
	}
	trustData, err := json.Marshal(map[string]any{"projects": map[string]any{
		projectKey: map[string]any{"trusted": true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, ".tyci", "trust.json"), trustData, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binPath, "cron", "list")
	cmd.Dir = subdir
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cron list from subdirectory: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "root-job") {
		t.Fatalf("cron list from subdirectory did not load project-root cron.json: %q", out)
	}
}
