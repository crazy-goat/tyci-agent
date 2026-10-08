package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeMarkdownAgent writes a minimal markdown agent definition file named
// "<name>.md" into dir, creating dir if needed.
func writeMarkdownAgent(t *testing.T, dir, name, frontmatter, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "---\n" + frontmatter + "\n---\n" + body
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestListMarkdownAgents_ProjectLocalVisible(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "reviewer", "model: anthropic/claude-opus", "You review code.")

	names, err := ListMarkdownAgents()
	if err != nil {
		t.Fatalf("ListMarkdownAgents: %v", err)
	}
	if len(names) != 1 || names[0] != "reviewer" {
		t.Errorf("ListMarkdownAgents() = %v, want [reviewer]", names)
	}
}

func TestGetMarkdownAgent_ProjectLocalVisible(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "reviewer", "model: anthropic/claude-opus", "You review code.")

	got, err := GetMarkdownAgent("reviewer")
	if err != nil {
		t.Fatalf("GetMarkdownAgent: %v", err)
	}
	if got.Name != "reviewer" {
		t.Errorf("Name = %q, want %q", got.Name, "reviewer")
	}
	if got.Frontmatter.Model != "anthropic/claude-opus" {
		t.Errorf("Model = %q, want %q", got.Frontmatter.Model, "anthropic/claude-opus")
	}
	if got.SystemPrompt != "You review code." {
		t.Errorf("SystemPrompt = %q, want %q", got.SystemPrompt, "You review code.")
	}
}

func TestGetMarkdownAgent_ProjectOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(home, GlobalConfigDir, "agents"), "coder", "model: openai/global-model", "Global coder.")
	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "coder", "model: openai/project-model", "Project coder.")

	got, err := GetMarkdownAgent("coder")
	if err != nil {
		t.Fatalf("GetMarkdownAgent: %v", err)
	}
	if got.Frontmatter.Model != "openai/project-model" {
		t.Errorf("Model = %q, want project-local model %q (project should override global)", got.Frontmatter.Model, "openai/project-model")
	}

	names, err := ListMarkdownAgents()
	if err != nil {
		t.Fatalf("ListMarkdownAgents: %v", err)
	}
	if len(names) != 1 || names[0] != "coder" {
		t.Errorf("ListMarkdownAgents() = %v, want a single deduped [coder]", names)
	}
}

func TestProjectLocalDiscoveryFromRepositorySubdirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
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

	subdir := filepath.Join(repo, "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMarkdownAgent(t, filepath.Join(repo, ".tyci", "agents"), "root-agent", "model: provider/root", "Root agent.")
	t.Chdir(subdir)

	got, err := GetMarkdownAgent("root-agent")
	if err != nil {
		t.Fatalf("GetMarkdownAgent from subdirectory: %v", err)
	}
	if got.Frontmatter.Model != "provider/root" {
		t.Errorf("markdown model = %q, want provider/root", got.Frontmatter.Model)
	}

}

func TestMarkdownAgentFrontmatter_TemperatureSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "tempagent", "model: anthropic/claude-opus\ntemperature: 0.7", "Deterministic-ish.")

	got, err := GetMarkdownAgent("tempagent")
	if err != nil {
		t.Fatalf("GetMarkdownAgent: %v", err)
	}
	if got.Frontmatter.Temperature != 0.7 {
		t.Errorf("Frontmatter.Temperature = %v, want 0.7", got.Frontmatter.Temperature)
	}
}

func TestMarkdownAgentFrontmatter_TemperatureUnsetDefaultsToZero(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "notempagent", "model: anthropic/claude-opus", "No temperature set.")

	got, err := GetMarkdownAgent("notempagent")
	if err != nil {
		t.Fatalf("GetMarkdownAgent: %v", err)
	}
	if got.Frontmatter.Temperature != 0 {
		t.Errorf("Frontmatter.Temperature = %v, want 0 (unset unwraps to zero value)", got.Frontmatter.Temperature)
	}
}

func TestMarkdownAgentFrontmatter_ToolsJoinedFromCommaList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	writeMarkdownAgent(t, filepath.Join(wd, ".tyci", "agents"), "toolagent", "model: anthropic/claude-opus\ntools: read, bash", "Does stuff.")

	got, err := GetMarkdownAgent("toolagent")
	if err != nil {
		t.Fatalf("GetMarkdownAgent: %v", err)
	}
	if got.Frontmatter.Tools != "read, bash" {
		t.Errorf("Frontmatter.Tools = %q, want %q", got.Frontmatter.Tools, "read, bash")
	}
}
