package providers

import (
	"context"
	_ "embed"
)

//go:embed SYSTEM_PROMPT.md
var SystemPrompt string

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
	SendWithHandler(model string, messages []Message, handler OutputHandler, debug bool) (*SendResult, error)
}
