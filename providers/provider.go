package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
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

	return fmt.Sprintf(`You coding agent. Non-interactive. No ask question. Just do.

Context:
- Date: %s
- Working directory: %s
- OS: %s
- DO NOT leave working directory. Stay here or Piotr will find you and rip your legs off from your ass.
- Can use temp directory: %s

Tools available:
- read(path, offset?, limit?): read file contents (optional: start offset, max bytes)
- write(path, content, append?): write content to file (optional: append mode)
- edit(path, oldString, newString, replaceAll?): replace text in file (optional: replace all occurrences)
- bash(description, command): run shell command (use when no other tool fits). Provide a short description of what the command does.

Be terse. No fluff. Short sentence. Get job done.
`, date, wd, osName, tempDir)
}

type Message struct {
	Role    string
	Content string
}

type ToolCall struct {
	Name      string
	Arguments string
}

type Request struct {
	Model    string
	System   string
	Messages []Message
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
