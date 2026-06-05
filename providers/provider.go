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

	"github.com/decodo/tyci-agent/api"
	"github.com/decodo/tyci-agent/stream"
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
- read(path, offset?, limit?): read file contents (optional: line number to start from 1-indexed, max lines). Truncated to 2000 lines/50KB – use offset to continue.
- write(path, content, append?): write content to file (optional: append mode)
- edit(path, oldString, newString, replaceAll?): replace text in file (optional: replace all occurrences)
- bash(description, command): run shell command (use when no other tool fits). Provide a short description of what the command does.
- subagent(task, tasks?, model?, timeout?): delegate complex or independent tasks to a child agent with its own context. Use when a task is self-contained, can run in parallel, or needs separate reasoning. Returns result text.

Be terse. No fluff. Short sentence. Get job done.
`, date, wd, osName, tempDir)

	// Append AGENTS.md from CWD if present
	if agentsMd, err := os.ReadFile(filepath.Join(wd, "AGENTS.md")); err == nil {
		content := strings.TrimSpace(string(agentsMd))
		if content != "" {
			prompt += "\n---\nAdditional instructions from AGENTS.md:\n" + content
		}
	}

	return prompt
}

// ContentBlock represents a single content block within a RichMessage.
type ContentBlock struct {
	Type       string          `json:"type"` // "text", "thinking", "toolCall", "toolResult"
	Text       string          `json:"text,omitempty"`
	Thinking   string          `json:"thinking,omitempty"`

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
