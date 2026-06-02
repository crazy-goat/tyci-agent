package providers

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"
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
- bash(command): run shell command (use when no other tool fits)

Be terse. No fluff. Short sentence. Get job done.
`, date, wd, osName, tempDir)
}

type UsageInfo struct {
	InputTokens  int
	OutputTokens int
	Cost         float64
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ToolCall struct {
	Name      string
	Arguments string
}

type SendResult struct {
	Text      string
	ToolCalls []ToolCall
}

type OutputHandler interface {
	Chunk(text string)
	Thinking(text string)
	EndThinking()
	LogToolCallStart(name string)
	ToolCallArg(text string)
	EndToolCall()
	Summary(usage UsageInfo)
	End()
	Error(err error)
}

type Provider interface {
	Name() string
	IsConfigured() bool
	Models() []string
	FreeModels() []string
	Send(ctx context.Context, model, prompt, system string, debug bool) (*SendResult, error)
	SendWithMessages(ctx context.Context, model, prompt, system string, messages []Message, debug bool) (*SendResult, error)
	SendWithHandler(model string, messages []Message, handler OutputHandler, debug, hideThinking, hideTools bool) (*SendResult, error)
}
