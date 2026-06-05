package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSystemPrompt_noAgentsMd(t *testing.T) {
	// No AGENTS.md file present — prompt should not contain the separator
	prompt := BuildSystemPrompt()
	if strings.Contains(prompt, "Additional instructions from AGENTS.md") {
		t.Errorf("expected no AGENTS.md content when file is missing, but found it")
	}
}

func TestBuildSystemPrompt_withAgentsMd(t *testing.T) {
	// Create temp dir with AGENTS.md
	dir := t.TempDir()
	content := "Use tabs for indentation.\nPrefer table-driven tests."
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Save original wd, change to temp dir
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	prompt := BuildSystemPrompt()
	if !strings.Contains(prompt, "Additional instructions from AGENTS.md") {
		t.Errorf("expected AGENTS.md content to be appended, but it's missing")
	}
	if !strings.Contains(prompt, "Use tabs for indentation.") {
		t.Errorf("expected AGENTS.md content to include file text")
	}
}

func TestBuildSystemPrompt_emptyAgentsMd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("   \n\n  "), 0644); err != nil {
		t.Fatal(err)
	}

	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	prompt := BuildSystemPrompt()
	if strings.Contains(prompt, "Additional instructions from AGENTS.md") {
		t.Errorf("expected no AGENTS.md content when file is empty/whitespace")
	}
}
