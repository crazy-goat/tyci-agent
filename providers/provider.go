package providers

import "context"

type UsageInfo struct {
	InputTokens  int
	OutputTokens int
	Cost         float64
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Provider interface {
	Name() string
	IsConfigured() bool
	Models() []string
	FreeModels() []string
	Send(ctx context.Context, model, prompt, system string, debug bool) (*SendResult, error)
	SendWithMessages(ctx context.Context, model, prompt, system string, messages []Message, debug bool) (*SendResult, error)
}

type ToolCall struct {
	Name      string
	Arguments string
}

type SendResult struct {
	Text      string
	ToolCalls []ToolCall
}
