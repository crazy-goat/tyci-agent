package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkill(t *testing.T) {
	dir := t.TempDir()

	// Create test skill
	skillDir := filepath.Join(dir, "my-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `---
description: A test skill
---

# My Skill

This is my skill content.`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Load the skill
	skill, err := LoadSkill(dir, "my-skill")
	if err != nil {
		t.Fatalf("LoadSkill failed: %v", err)
	}

	if skill.Name != "my-skill" {
		t.Errorf("expected name 'my-skill', got %q", skill.Name)
	}
	if skill.Description != "A test skill" {
		t.Errorf("expected description 'A test skill', got %q", skill.Description)
	}
	if skill.Content != content {
		t.Errorf("content mismatch")
	}
}

func TestLoadSkills(t *testing.T) {
	dir := t.TempDir()

	// Create multiple skills
	for _, name := range []string{"skill-a", "skill-b", "skill-c"} {
		skillDir := filepath.Join(dir, name)
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			t.Fatal(err)
		}
		content := "# " + name + "\n\nSome content for " + name
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills failed: %v", err)
	}

	if len(skills) != 3 {
		t.Errorf("expected 3 skills, got %d", len(skills))
	}
}

func TestListSkills(t *testing.T) {
	dir := t.TempDir()

	// Create skills
	for _, name := range []string{"alpha", "beta"} {
		skillDir := filepath.Join(dir, name)
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Skill"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create a directory without SKILL.md (should be ignored)
	if err := os.MkdirAll(filepath.Join(dir, "no-skill"), 0755); err != nil {
		t.Fatal(err)
	}

	names, err := ListSkills(dir)
	if err != nil {
		t.Fatalf("ListSkills failed: %v", err)
	}

	if len(names) != 2 {
		t.Errorf("expected 2 skill names, got %d: %v", len(names), names)
	}
}

func TestLoadSkill_NotFound(t *testing.T) {
	dir := t.TempDir()

	_, err := LoadSkill(dir, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent skill")
	}
}

func TestLoadSkills_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatalf("LoadSkills failed: %v", err)
	}

	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestExtractDescription(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name: "frontmatter description",
			content: `---
description: This is a description
---

# Title`,
			expected: "This is a description",
		},
		{
			name:     "first non-heading line",
			content:  "# Title\n\nThis is the description.",
			expected: "This is the description.",
		},
		{
			name:     "empty content",
			content:  "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractDescription(tt.content)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// writeSkill creates <dir>/<name>/SKILL.md with the given description.
func writeSkill(t *testing.T, dir, name, description string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\ndescription: " + description + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// =============================================================================
// project-local merge (TODO.md item 22)
// =============================================================================

func TestListSkillsMerged_UnionsGlobalAndProjectLocal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSkill(t, SkillsDir(), "global-only", "lives in ~/.tyci/skills")

	wd := t.TempDir()
	writeSkill(t, ProjectDir(wd), "local-only", "lives in .tyci/skills")

	got, err := ListSkillsMerged(wd)
	if err != nil {
		t.Fatalf("ListSkillsMerged: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d skills, want 2: %+v", len(got), got)
	}
	names := map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	if !names["global-only"] || !names["local-only"] {
		t.Errorf("expected both global-only and local-only, got %v", names)
	}
}

// TestListSkillsMerged_LocalWinsOnNameCollision mirrors how agents/ already
// works: a project-local skill with the same directory name as a global one
// replaces it rather than both showing up.
func TestListSkillsMerged_LocalWinsOnNameCollision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSkill(t, SkillsDir(), "shared-name", "the global version")

	wd := t.TempDir()
	writeSkill(t, ProjectDir(wd), "shared-name", "the local version")

	got, err := ListSkillsMerged(wd)
	if err != nil {
		t.Fatalf("ListSkillsMerged: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d skills, want exactly 1 (local wins on collision): %+v", len(got), got)
	}
	if got[0].Description != "the local version" {
		t.Errorf("Description = %q, want the local version to win", got[0].Description)
	}
}

func TestGetSkillMerged_LocalWinsOnNameCollision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSkill(t, SkillsDir(), "shared-name", "the global version")

	wd := t.TempDir()
	writeSkill(t, ProjectDir(wd), "shared-name", "the local version")

	skill, err := GetSkillMerged(wd, "shared-name")
	if err != nil {
		t.Fatalf("GetSkillMerged: %v", err)
	}
	if skill.Description != "the local version" {
		t.Errorf("Description = %q, want the local version to win", skill.Description)
	}
}

func TestGetSkillMerged_FallsBackToGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSkill(t, SkillsDir(), "global-only", "the global version")

	wd := t.TempDir() // no project-local skills dir at all
	skill, err := GetSkillMerged(wd, "global-only")
	if err != nil {
		t.Fatalf("GetSkillMerged: %v", err)
	}
	if skill.Description != "the global version" {
		t.Errorf("Description = %q, want the global version", skill.Description)
	}
}
