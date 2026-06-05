package display

type UsageInfo struct {
	InputTokens           int
	OutputTokens          int
	CacheReadInputTokens  int
	CacheCreateInputTokens int
}

type ToolResult struct {
	Success bool
	Content string
	Error   string
}

type ToolCall struct {
	Name      string
	Arguments string
}

type Display interface {
	Chunk(text string)
	Thinking(text string)
	EndThinking()
	ToolCallStart(name string)
	ToolCallArg(text string)
	EndToolCall()
	ToolResult(name string, result *ToolResult)
	Summary(usage UsageInfo)
	Error(err error)
	End()
}
