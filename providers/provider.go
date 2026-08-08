package providers

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/connector"
)

func BuildSystemPrompt() string {
	return buildSystemPrompt(true, "")
}

// BuildSubagentSystemPrompt is the system prompt for a headless child agent
// spawned via the subagent tool. It drops the subagent tool from the listing
// (children cannot spawn further children) and states the subagent contract:
// work autonomously, never ask questions, and end the turn with a single
// self-contained final message that IS the result the parent receives.
func BuildSubagentSystemPrompt() string {
	return buildSystemPrompt(false, `
You are a SUBAGENT spawned by a parent agent to complete ONE task and report back.
- You cannot ask questions — there is no user to reply. Make reasonable assumptions and proceed.
- Do the whole task, then END YOUR TURN with a single self-contained final message that IS your result (the findings/answer/summary the parent needs). The parent sees only your final text, not your tool calls or thinking.
- Do not stop early and do not loop. If you get blocked, state in your final message what you did and exactly what is blocking you.`)
}

func buildSystemPrompt(includeSubagent bool, roleNote string) string {
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

	// The subagent tool is only advertised to the top-level agent; children run
	// with a schema that omits it, so listing it here would tempt them to call
	// a tool that does not exist.
	subagentLine := ""
	if includeSubagent {
		subagentLine = "\n- subagent(task, tasks?, model?): delegate independent work to child agents."
	}

	prompt := fmt.Sprintf(`You coding agent. Non-interactive. No ask question. Just do.%s

Context:
- Date: %s
- Working directory: %s
- OS: %s
- DO NOT leave working directory. Stay here or Piotr will find you and rip your legs off from your ass.
- Can use temp directory: %s

Tools available:
- find(pattern, cwd?, exclude?, limit?, includeDirs?, absolute?): find files by glob pattern or search their contents. Use method="glob" for file path patterns (e.g. **/*.go) or method="grep" for text/regex/word search inside files. Returns relative paths by default.
- todo(action, id?, content?, status?, parentId?): manage per-run todo list. actions: add/update/doing/blocked/done/remove/list/clear.
- read(path, offset?, limit?, lineNumbers?): read file contents. Returns full file; use offset/limit for ranges, lineNumbers=true to prefix each line with its number.
- write(path, content, range?, oldString?, newString?, occurrence?, dryRun?): write file or replace text. Use content+range for writing; use oldString+newString for replacements.
- bash(description, command, timeout?): run shell command when no tool fits.%s

IMPORTANT: Always start by creating a plan using the todo tool before using other tools. The system enforces this — non-todo tool calls will fail until at least one todo item exists. Use todo(action="add", content="...") or todo(action="add_batch", items=[...]) first.

Be terse. No fluff. Short sentence. Get job done.
`, roleNote, date, wd, osName, tempDir, subagentLine)

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
		prompt += "\nUse skills(name) to load a skill's full content.\n"
	}

	return prompt
}

// The canonical message types live in package connector, because providers
// imports connector (and never the other way round). These are type aliases,
// not new types, so every existing consumer — agent, session, the CLI —
// keeps compiling unchanged and the two spellings stay interchangeable.

// ContentBlock represents a single content block within a RichMessage.
type ContentBlock = connector.ContentBlock

// RichMessage is the canonical message type used throughout the agent loop.
type RichMessage = connector.Message

// Request is what a connector.ModelClient sends.
type Request = connector.Request

// Provider is the CATALOG: one named entry answering questions about which
// models it serves and whether it has a credential, plus the factory that
// turns such an entry into something able to send a request.
//
// It deliberately has no Stream. Sending bytes is the job of
// connector.ModelClient, which is the only abstraction package agent ever
// sees, and Client is the only door to it. Before this split the two
// abstractions were glued together, which is how FreeModels() could sit dead
// on the interface for as long as it did — nobody could tell whether it was a
// catalog question or a transport concern.
//
// Minting the client is a METHOD, not a package-level Client(p, model)
// function, because the provider is the only thing that knows how to reach
// its own models (URI, auth, connector kind). A free function would have to
// take the interface and then go looking for the transport behind it, i.e.
// type-assert — and a failed assertion there degrades silently. As a method
// it is checked by the compiler.
type Provider interface {
	Name() string
	IsConfigured() bool
	Models() []string

	// Client returns a ModelClient bound to model on this provider. The name
	// is deliberately NOT validated: `--model provider/anything` has always
	// passed an unlisted name straight through, and the "model not found in
	// provider" error surfaces at request time.
	Client(model string) connector.ModelClient
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
