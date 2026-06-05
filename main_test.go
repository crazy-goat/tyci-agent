package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tyci-agent-test")
	if err != nil {
		os.Stderr.WriteString("mkdir temp: " + err.Error())
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	binPath = filepath.Join(dir, "tyci-agent")
	out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput()
	if err != nil {
		os.Stderr.WriteString("build failed: " + string(out))
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestEmptyPromptExitsZero(t *testing.T) {
	cmd := exec.Command(binPath, "--prompt", "")
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("exit code %d, stderr: %s", exitErr.ExitCode(), exitErr.Stderr)
		}
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNoPromptFlagExitsZero(t *testing.T) {
	cmd := exec.Command(binPath)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("exit code %d, stderr: %s", exitErr.ExitCode(), exitErr.Stderr)
		}
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHelpExitsZero(t *testing.T) {
	cmd := exec.Command(binPath, "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("exit code %d:\n%s", exitErr.ExitCode(), string(out))
		}
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) == 0 {
		t.Error("--help produced no output")
	}
}
