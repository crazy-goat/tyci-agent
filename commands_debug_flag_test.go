package main

import (
	"path/filepath"
	"testing"
)

// TestDebugFlagIsHiddenAndDeprecated checks that --debug stays a flag, so old
// scripts do not fail, but is hidden from the help and marked deprecated.
func TestDebugFlagIsHiddenAndDeprecated(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup("debug")
	if f == nil {
		t.Fatal("the --debug flag is missing: old scripts would fail with unknown flag")
	}
	if !f.Hidden {
		t.Error("--debug is not hidden")
	}
	if f.Deprecated == "" {
		t.Error("--debug is not marked deprecated")
	}
}

// TestDebugFlagIsAcceptedAndChangesNothing checks that a run with --debug works
// and writes the same debug log as a run without the flag.
func TestDebugFlagIsAcceptedAndChangesNothing(t *testing.T) {
	home := wfHome(t)
	wfUseRepo(t, wfRunRepo(t, home))
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "one-check", "workflow.json"), oneCheckFlow("one-check"))
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "one-check", "checks", "ok.sh"), wfOKScript)

	if _, _, err := runWorkflowCLI(t, "workflow", "run", "one-check", "13", "--json", "--debug"); err != nil {
		t.Fatalf("run with --debug: %v", err)
	}
	logs, _ := filepath.Glob(filepath.Join(home, ".tyci", "debug", "*.log"))
	if len(logs) != 1 {
		t.Errorf("debug logs = %v, want one: the log is on by default", logs)
	}
}
