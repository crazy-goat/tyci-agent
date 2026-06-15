package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/stream"
)

func BuildSystemPrompt() string {
	wd, _ := os.Getwd()
	if wd == "" {
		wd = "."
	}

	date := time.Now().Format("2006-01-02")
	osName := runtime.GOOS

	tempDir := "/tmp"
	if osName == "windows" {
		tempDir = "%TEMP%"
	}

	prompt := fmt.Sprintf(`You coding agent. Non-interactive. No ask question. Just do.

Context:
- Date: %s
- Working directory: %s
- OS: %s
- DO NOT leave working directory. Stay here or Piotr will find you and rip your legs off from your ass.
- Can use temp directory: %s

Tools available:
- glob(pattern, cwd?, exclude?, limit?, includeDirs?, absolute?): find files by glob. Returns relative paths by default.
- grep(pattern, cwd?, include?, exclude?, mode?, caseSensitive?, context?, limit?, output?, maxLineLength?): search contents. mode: text/regex/word. output: lines/files/count.
- todo(action, id?, content?, status?, priority?, parentId?): manage per-run todo list. actions: add/update/doing/blocked/done/remove/list/clear.
- read(path, offset?, limit?, lineNumbers?): read file contents. Use lineNumbers=true for exact line edits.
- write(path, content, range?): write file. range: line number, 'from...to', 'before:N', 'after:N', 'all', or -1/'append'. Defaults to whole file.
- edit(path, oldString, newString, occurrence?, dryRun?): replace exact text. Default requires one match; occurrence can be number or 'all'.
- bash(description, command, timeout?): run shell command when no tool fits.
- subagent(task, tasks?, model?, temperature?): delegate independent work to child agents.

Be terse. No fluff. Short sentence. Get job done.
`, date, wd, osName, tempDir)

	// Append AGENTS.md from CWD if present
	if agentsMd, err := os.ReadFile(filepath.Join(wd, "AGENTS.md")); err == nil {
		content := strings.TrimSpace(string(agentsMd))
		if content != "" {
			prompt += "\n---\nAdditional instructions from AGENTS.md:\n" + content
		}
	}

	// List available skills (names only, not content)
	skillsDir := filepath.Join(os.Getenv("HOME"), ".tyci", "skills")
	if skillNames, err := listSkillNames(skillsDir); err == nil && len(skillNames) > 0 {
		prompt += "\n---\nAvailable skills: " + strings.Join(skillNames, ", ")
		prompt += "\nUse load_skill(name) to load a skill's full content.\n"
	}

	return prompt
}

// ContentBlock represents a single content block within a RichMessage.
type ContentBlock struct {
	Type     string `json:"type"` // "text", "thinking", "toolCall", "toolResult"
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`

	// Tool call fields
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`

	// Tool result fields
	IsError    bool   `json:"isError,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
}

// RichMessage is the canonical message type used throughout the agent loop.
// It carries structured content blocks instead of a flat text string,
// allowing providers to build their own wire format.
type RichMessage struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// Request is passed to Provider.Stream.
type Request struct {
	Model    string
	System   string
	Messages []RichMessage
	Tools    json.RawMessage
	Debug    bool
}

type Provider interface {
	Name() string
	IsConfigured() bool
	Models() []string
	FreeModels() []string
	Stream(ctx context.Context, req Request) (<-chan stream.Event, error)
}

var DefaultRetryConfig = api.RetryConfig{MaxRetries: 5, BaseBackoff: 4, MaxBackoff: 128}

// listSkillNames returns the names of all skills in the given directory.
func listSkillNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); err == nil {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}
