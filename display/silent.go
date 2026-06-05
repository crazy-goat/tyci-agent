package display

import "strings"

type Silent struct {
	text        strings.Builder
	toolCalls   []ToolCall
	currentTool *ToolCall
}

func NewSilent() *Silent {
	return &Silent{}
}

func (s *Silent) Chunk(text string) {
	s.text.WriteString(text)
}

func (s *Silent) Thinking(text string) {}

func (s *Silent) EndThinking() {}

func (s *Silent) ToolCallStart(name string) {
	s.currentTool = &ToolCall{Name: name}
}

func (s *Silent) ToolCallArg(text string) {
	if s.currentTool == nil {
		return
	}
	s.currentTool.Arguments += text
}

func (s *Silent) EndToolCall() {
	if s.currentTool == nil {
		return
	}
	s.toolCalls = append(s.toolCalls, *s.currentTool)
	s.currentTool = nil
}

func (s *Silent) ToolResult(name string, result *ToolResult) {}

func (s *Silent) Summary(usage UsageInfo) {}

func (s *Silent) Error(err error) {}

func (s *Silent) End() {}

func (s *Silent) Text() string {
	return s.text.String()
}

func (s *Silent) ToolCalls() []ToolCall {
	out := make([]ToolCall, len(s.toolCalls))
	copy(out, s.toolCalls)
	return out
}
