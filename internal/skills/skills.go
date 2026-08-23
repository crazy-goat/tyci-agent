package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill represents a loaded skill with metadata and content.
type Skill struct {
	Name        string // skill directory name (e.g., "git-workflow")
	Description string // first line or frontmatter description
	Content     string // full markdown content
	Path        string // path to SKILL.md file
}

// SkillsDir returns the global skills directory (~/.tyci/skills).
func SkillsDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/tmp"
	}
	return filepath.Join(home, ".tyci", "skills")
}

// ProjectDir returns the project-local skills directory: <wd>/.tyci/skills.
// If wd is empty, the current working directory is used. Not trust-gated —
// a skill is plain markdown injected as prompt text, the same posture as
// agents/ and memory/, not something a project directory makes tyci
// execute (see internal/trust's doc comment on what item 23 does gate).
func ProjectDir(wd string) string {
	if wd == "" {
		wd, _ = os.Getwd()
	}
	return filepath.Join(wd, ".tyci", "skills")
}

// Dirs returns the ordered list of directories to load skills from: global
// first, then project-local — later directories win when merged by
// ListSkillsMerged/GetSkillMerged, mirroring internal/agentdefs.Dirs.
func Dirs(wd string) []string {
	return []string{SkillsDir(), ProjectDir(wd)}
}

// ListSkillsMerged returns every skill visible from wd: global then
// project-local, merged with project-local taking precedence on a name
// collision (mirroring how agents/ already works). The result is sorted by
// name.
func ListSkillsMerged(wd string) ([]Skill, error) {
	byName := make(map[string]Skill)
	for _, dir := range Dirs(wd) {
		found, err := LoadSkills(dir)
		if err != nil {
			continue
		}
		for _, s := range found {
			byName[s.Name] = s
		}
	}
	result := make([]Skill, 0, len(byName))
	for _, s := range byName {
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// GetSkillMerged returns the named skill, looking in both the global and
// project-local directories with project-local taking precedence.
func GetSkillMerged(wd, name string) (*Skill, error) {
	for _, dir := range []string{ProjectDir(wd), SkillsDir()} {
		if skill, err := LoadSkill(dir, name); err == nil {
			return skill, nil
		}
	}
	return nil, fmt.Errorf("skill %q not found", name)
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
