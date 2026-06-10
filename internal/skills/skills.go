package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Skill represents a loaded skill with metadata and content.
type Skill struct {
	Name        string // skill directory name (e.g., "git-workflow")
	Description string // first line or frontmatter description
	Content     string // full markdown content
	Path        string // path to SKILL.md file
}

// SkillsDir returns the default skills directory (~/.tyci/skills).
func SkillsDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/tmp"
	}
	return filepath.Join(home, ".tyci", "skills")
}

// LoadSkills reads all skills from the given directory.
// Each skill is a subdirectory containing a SKILL.md file.
func LoadSkills(dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading skills directory: %w", err)
	}

	var skills []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		skill, err := LoadSkill(dir, entry.Name())
		if err != nil {
			continue // skip skills that can't be loaded
		}
		skill.Path = skillPath
		skills = append(skills, *skill)
	}
	return skills, nil
}

// LoadSkill loads a single skill by name from the given directory.
func LoadSkill(dir, name string) (*Skill, error) {
	skillPath := filepath.Join(dir, name, "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, fmt.Errorf("reading skill %q: %w", name, err)
	}

	content := string(data)
	description := extractDescription(content)

	return &Skill{
		Name:        name,
		Description: description,
		Content:     content,
		Path:        skillPath,
	}, nil
}

// ListSkills returns the names of all available skills in the given directory.
func ListSkills(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading skills directory: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Check if SKILL.md exists
		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); err == nil {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// extractDescription extracts a description from the skill content.
// It looks for:
// 1. A YAML frontmatter "description:" field
// 2. The first non-empty, non-heading line
func extractDescription(content string) string {
	lines := strings.Split(content, "\n")

	// Check for YAML frontmatter
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			line := strings.TrimSpace(lines[i])
			if line == "---" {
				break
			}
			if strings.HasPrefix(line, "description:") {
				desc := strings.TrimPrefix(line, "description:")
				return strings.TrimSpace(desc)
			}
		}
	}

	// Fall back to first non-empty, non-heading line
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(trimmed) > 200 {
			return trimmed[:200] + "..."
		}
		return trimmed
	}

	return ""
}
