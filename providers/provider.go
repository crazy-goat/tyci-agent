package providers

import "context"

type UsageInfo struct {
	InputTokens  int
	OutputTokens int
	Cost         float64
}

type StreamHandler interface {
	Chunk(text string)
	Summary(usage UsageInfo)
	End()
	Error(err error)
}

type Provider interface {
	Name() string
	IsConfigured() bool
	Models() []string
	Send(ctx context.Context, model, prompt, system string, handler StreamHandler) error
}
