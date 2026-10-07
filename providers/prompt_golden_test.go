//go:build !noanthropic && !nogemini

package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenPrompt renders a prompt in an empty HOME and working directory and
// replaces the volatile context values, so the text is stable.
func goldenPrompt(t *testing.T, build func() string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)
	real, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	_, date, osName, tempDir := envContext()
	out := build()
	out = strings.ReplaceAll(out, real, "<WD>")
	out = strings.ReplaceAll(out, wd, "<WD>")
	out = strings.ReplaceAll(out, date, "<DATE>")
	return strings.ReplaceAll(out, "OS "+osName+" · temp dir "+tempDir, "OS <OS> · temp dir <TMP>")
}

func checkPromptGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join(testdataDir, name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v\nrun: go test ./providers/ -run Prompt -update", path, err)
	}
	if string(want) != got {
		t.Errorf("prompt differs from %s\nrun: go test ./providers/ -run Prompt -update (only if the change is intended)", path)
	}
}

func TestChatPromptUnchangedForConsole(t *testing.T) {
	checkPromptGolden(t, "prompt_chat.golden.txt", goldenPrompt(t, BuildSystemPrompt))
}

func TestSubagentPromptUnchanged(t *testing.T) {
	checkPromptGolden(t, "prompt_subagent.golden.txt", goldenPrompt(t, BuildSubagentSystemPrompt))
}

func TestSubagentRolePromptUnchanged(t *testing.T) {
	checkPromptGolden(t, "prompt_subagent_role.golden.txt", goldenPrompt(t, func() string {
		return BuildSubagentSystemPromptWithRole("You review Go code.", true)
	}))
}

func TestScoutPromptUnchanged(t *testing.T) {
	checkPromptGolden(t, "prompt_scout.golden.txt", goldenPrompt(t, func() string {
		return BuildScoutSystemPrompt(true)
	}))
}

// testdataDir is absolute because goldenPrompt changes the working directory.
var testdataDir, _ = filepath.Abs("testdata")
