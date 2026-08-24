package agentdefs

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParse_AllFields(t *testing.T) {
	content := `---
description: A test agent
model: anthropic/claude-sonnet-5
tools:  read, find ,bash
max_iterations: 5
temperature: 0.7
fallback:
  - anthropic/claude-haiku
  - anthropic/claude-opus
---

# Test Agent

This is the body.`

	def, err := Parse("myagent.md", []byte(content))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if def.Name != "myagent" {
		t.Errorf("Name = %q, want %q", def.Name, "myagent")
	}
	if def.Description != "A test agent" {
		t.Errorf("Description = %q", def.Description)
	}
	if def.Model != "anthropic/claude-sonnet-5" {
		t.Errorf("Model = %q", def.Model)
	}
	wantTools := []string{"read", "find", "bash"}
	if !reflect.DeepEqual(def.Tools, wantTools) {
		t.Errorf("Tools = %v, want %v", def.Tools, wantTools)
	}
	if def.MaxIterations != 5 {
		t.Errorf("MaxIterations = %d, want 5", def.MaxIterations)
	}
	if def.Temperature == nil || *def.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", def.Temperature)
	}
	wantFallback := []string{"anthropic/claude-haiku", "anthropic/claude-opus"}
	if !reflect.DeepEqual(def.Fallback, wantFallback) {
		t.Errorf("Fallback = %v, want %v", def.Fallback, wantFallback)
	}
	if def.SystemPrompt != "# Test Agent\n\nThis is the body." {
		t.Errorf("SystemPrompt = %q", def.SystemPrompt)
	}
}

func TestParse_SystemPromptMode_DefaultsToAppend(t *testing.T) {
	content := `---
model: foo
---

body`
	def, err := Parse("a.md", []byte(content))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if def.SystemPromptMode != SystemPromptModeAppend {
		t.Errorf("SystemPromptMode = %q, want %q", def.SystemPromptMode, SystemPromptModeAppend)
	}
}

func TestParse_SystemPromptMode_ExplicitReplace(t *testing.T) {
	content := `---
model: foo
system_prompt_mode: replace
---

body`
	def, err := Parse("a.md", []byte(content))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if def.SystemPromptMode != SystemPromptModeReplace {
		t.Errorf("SystemPromptMode = %q, want %q", def.SystemPromptMode, SystemPromptModeReplace)
	}
}

func TestParse_SystemPromptMode_ExplicitAppend(t *testing.T) {
	content := `---
model: foo
system_prompt_mode: append
---

body`
	def, err := Parse("a.md", []byte(content))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if def.SystemPromptMode != SystemPromptModeAppend {
		t.Errorf("SystemPromptMode = %q, want %q", def.SystemPromptMode, SystemPromptModeAppend)
	}
}

func TestParse_SystemPromptMode_InvalidValueErrors(t *testing.T) {
	content := `---
model: foo
system_prompt_mode: yolo
---

body`
	_, err := Parse("a.md", []byte(content))
	if err == nil {
		t.Fatal("expected an error for an invalid system_prompt_mode value")
	}
}

func TestParse_SystemOverridesBody(t *testing.T) {
	content := `---
system: override prompt
---

body content that should be ignored`

	def, err := Parse("a.md", []byte(content))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if def.SystemPrompt != "override prompt" {
		t.Errorf("SystemPrompt = %q, want %q", def.SystemPrompt, "override prompt")
	}
}

func TestParse_Errors(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"no frontmatter", "just a body, no frontmatter markers"},
		{"unclosed frontmatter", "---\nmodel: foo\n\nbody without closing marker"},
		{"broken yaml", "---\nmodel: [unclosed\n---\nbody"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("a.md", []byte(tt.content))
			if err == nil {
				t.Errorf("expected error for %q", tt.name)
			}
		})
	}
}

func TestParse_TemperatureZeroIsDistinctFromUnset(t *testing.T) {
	content := `---
model: foo
temperature: 0
---

body`
	def, err := Parse("a.md", []byte(content))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if def.Temperature == nil {
		t.Fatal("Temperature = nil, want pointer to 0")
	}
	if *def.Temperature != 0 {
		t.Errorf("Temperature = %v, want 0", *def.Temperature)
	}
}

func TestParse_TemperatureOmittedIsNil(t *testing.T) {
	content := `---
model: foo
---

body`
	def, err := Parse("a.md", []byte(content))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if def.Temperature != nil {
		t.Errorf("Temperature = %v, want nil", *def.Temperature)
	}
}

func TestParse_TemperatureRange(t *testing.T) {
	tests := []struct {
		name        string
		temperature string
		wantErr     bool
	}{
		{"lower bound valid", "0", false},
		{"upper bound valid", "2", false},
		{"mid-range valid", "1.3", false},
		{"below lower bound", "-0.1", true},
		{"above upper bound", "2.1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "---\nmodel: foo\ntemperature: " + tt.temperature + "\n---\n\nbody"
			def, err := Parse("a.md", []byte(content))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for temperature %s, got none", tt.temperature)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for temperature %s: %v", tt.temperature, err)
			}
			if def.Temperature == nil {
				t.Fatal("Temperature = nil, want set")
			}
		})
	}
}

func TestParse_EmptyToolsIsNil(t *testing.T) {
	content := `---
model: foo
---

body`
	def, err := Parse("a.md", []byte(content))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if def.Tools != nil {
		t.Errorf("Tools = %v, want nil", def.Tools)
	}
}

func writeAgent(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

const validAgentContent = `---
description: valid agent
model: anthropic/claude-sonnet-5
---

Valid body.`

func TestLoadDir_MissingDir(t *testing.T) {
	defs, err := LoadDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if defs != nil {
		t.Errorf("expected nil defs, got %v", defs)
	}
}

func TestLoadDir_IgnoresSubdirsAndNonMd(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "good.md", validAgentContent)
	writeAgent(t, dir, "notes.txt", validAgentContent)
	if err := os.MkdirAll(filepath.Join(dir, "subdir.md"), 0755); err != nil {
		t.Fatal(err)
	}

	defs, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir failed: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "good" {
		t.Fatalf("expected exactly [good], got %v", defs)
	}
	if defs[0].Path != filepath.Join(dir, "good.md") {
		t.Errorf("Path = %q", defs[0].Path)
	}
}

func TestLoadDir_SkipsBrokenFileSilently(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "good.md", validAgentContent)
	writeAgent(t, dir, "broken.md", "no frontmatter here at all")

	defs, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir failed: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "good" {
		t.Fatalf("expected only [good] to load, got %v", defs)
	}
}

func TestLoad_LaterDirWinsAndSorted(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeAgent(t, globalDir, "zeta.md", `---
model: global-model
---
global zeta`)
	writeAgent(t, globalDir, "alpha.md", `---
model: global-model
---
global alpha`)
	writeAgent(t, projectDir, "alpha.md", `---
model: project-model
---
project alpha`)

	defs := Load([]string{globalDir, projectDir})

	if len(defs) != 2 {
		t.Fatalf("expected 2 defs, got %d: %v", len(defs), defs)
	}
	if defs[0].Name != "alpha" || defs[1].Name != "zeta" {
		t.Errorf("expected sorted [alpha, zeta], got [%s, %s]", defs[0].Name, defs[1].Name)
	}
	if defs[0].Model != "project-model" {
		t.Errorf("expected project dir to win for alpha, got Model=%q", defs[0].Model)
	}
	if defs[1].Model != "global-model" {
		t.Errorf("expected global model for zeta, got Model=%q", defs[1].Model)
	}
}

func TestGetAndList_Hermetic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	globalAgentsDir := filepath.Join(home, ".tyci", "agents")
	if err := os.MkdirAll(globalAgentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeAgent(t, globalAgentsDir, "helper.md", `---
description: global helper
model: global-model
---
global helper body`)

	wd := t.TempDir()
	projectAgentsDir := filepath.Join(wd, ".tyci", "agents")
	if err := os.MkdirAll(projectAgentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeAgent(t, projectAgentsDir, "helper.md", `---
description: project helper
model: project-model
---
project helper body`)
	writeAgent(t, projectAgentsDir, "other.md", `---
model: other-model
---
other body`)

	list := List(wd)
	if len(list) != 2 {
		t.Fatalf("expected 2 defs, got %d: %v", len(list), list)
	}
	if list[0].Name != "helper" || list[1].Name != "other" {
		t.Errorf("expected sorted [helper, other], got [%s, %s]", list[0].Name, list[1].Name)
	}

	def, ok := Get(wd, "helper")
	if !ok {
		t.Fatal("Get(helper) not found")
	}
	if def.Model != "project-model" {
		t.Errorf("expected project override, got Model=%q", def.Model)
	}

	if _, ok := Get(wd, "nonexistent"); ok {
		t.Error("Get(nonexistent) unexpectedly found")
	}
}

func TestGlobalDir_UsesHomeEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := filepath.Join(home, ".tyci", "agents")
	if got := GlobalDir(); got != want {
		t.Errorf("GlobalDir() = %q, want %q", got, want)
	}
}

func TestProjectDir(t *testing.T) {
	want := filepath.Join("/some/wd", ".tyci", "agents")
	if got := ProjectDir("/some/wd"); got != want {
		t.Errorf("ProjectDir() = %q, want %q", got, want)
	}
}

func TestProjectDirFromRepositorySubdirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file")
	runGit("commit", "-qm", "one")

	subdir := filepath.Join(repo, "nested", "dir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentsDir := filepath.Join(repo, ".tyci", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAgent(t, agentsDir, "from-root.md", validAgentContent)

	got := ProjectDir(subdir)
	root, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".tyci", "agents")
	if got != want {
		t.Fatalf("ProjectDir(subdir) = %q, want %q", got, want)
	}
	defs := List(subdir)
	if len(defs) != 1 || defs[0].Name != "from-root" {
		t.Fatalf("List(subdir) = %v, want [from-root]", defs)
	}
}
