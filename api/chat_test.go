package api

import (
	"testing"
)

type mockHandler struct {
	chunks         []string
	thinking       []string
	toolCalls      []string
	toolArgs       []string
	toolResults    []string
	accumulated    []ToolCall
	toolStarted    bool
	thinkingActive bool
}

func (m *mockHandler) Chunk(text string) {
	m.chunks = append(m.chunks, text)
}

func (m *mockHandler) Thinking(text string) {
	m.thinking = append(m.thinking, text)
	m.thinkingActive = true
}

func (m *mockHandler) EndThinking() {
	m.thinkingActive = false
}

func (m *mockHandler) LogToolCallStart(name string) {
	m.toolCalls = append(m.toolCalls, name)
	m.toolStarted = true
}

func (m *mockHandler) ToolCallArg(text string) {
	m.toolArgs = append(m.toolArgs, text)
}

func (m *mockHandler) EndToolCall() {
	m.toolStarted = false
}

func (m *mockHandler) Summary(usage UsageInfo) {}

func (m *mockHandler) End() {}

func (m *mockHandler) Error(err error) {}

func (m *mockHandler) LogRequest(method, url string, body any) {}

func (m *mockHandler) LogResponse(data string) {}

func (m *mockHandler) AccumulateToolCall(idx int, name, argument string) {
	for len(m.accumulated) <= idx {
		m.accumulated = append(m.accumulated, ToolCall{Index: len(m.accumulated)})
	}
	if name != "" {
		m.accumulated[idx].Name = name
	}
	m.accumulated[idx].Argument += argument
}

func (m *mockHandler) GetToolCalls() []ToolCall {
	return m.accumulated
}

func TestAccumulateToolCall_SingleTool(t *testing.T) {
	h := &mockHandler{}

	// Simulate single tool call with arguments coming in chunks
	h.AccumulateToolCall(0, "read", "")
	h.AccumulateToolCall(0, "", "{\"path\": \"file.txt\"}")

	calls := h.GetToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "read" {
		t.Errorf("expected name 'read', got '%s'", calls[0].Name)
	}
	if calls[0].Argument != "{\"path\": \"file.txt\"}" {
		t.Errorf("expected argument '{\"path\": \"file.txt\"}', got '%s'", calls[0].Argument)
	}
}

func TestAccumulateToolCall_MultipleTools(t *testing.T) {
	h := &mockHandler{}

	// Simulate two tool calls
	h.AccumulateToolCall(0, "read", "")
	h.AccumulateToolCall(0, "", "{\"path\": \"file1.txt\"}")

	h.AccumulateToolCall(1, "read", "")
	h.AccumulateToolCall(1, "", "{\"path\": \"file2.txt\"}")

	calls := h.GetToolCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}

	if calls[0].Name != "read" || calls[0].Argument != "{\"path\": \"file1.txt\"}" {
		t.Errorf("tool 0: expected read with file1.txt, got %s with %s", calls[0].Name, calls[0].Argument)
	}

	if calls[1].Name != "read" || calls[1].Argument != "{\"path\": \"file2.txt\"}" {
		t.Errorf("tool 1: expected read with file2.txt, got %s with %s", calls[1].Name, calls[1].Argument)
	}
}

func TestAccumulateToolCall_ArgumentsAccumulation(t *testing.T) {
	h := &mockHandler{}

	// Simulate arguments coming in multiple chunks
	h.AccumulateToolCall(0, "bash", "")
	h.AccumulateToolCall(0, "", "{\"command\": \"ls")
	h.AccumulateToolCall(0, "", " -la\"")
	h.AccumulateToolCall(0, "", "}")

	calls := h.GetToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}

	expected := "{\"command\": \"ls -la\"}"
	if calls[0].Argument != expected {
		t.Errorf("expected argument '%s', got '%s'", expected, calls[0].Argument)
	}
}

func TestAccumulateToolCall_NonSequentialIndex(t *testing.T) {
	h := &mockHandler{}

	// Simulate tool call with index 1 coming before index 0
	// This can happen when API sends tool calls out of order
	h.AccumulateToolCall(1, "write", "")
	h.AccumulateToolCall(1, "", "{\"path\": \"file.txt\"}")

	h.AccumulateToolCall(0, "read", "")
	h.AccumulateToolCall(0, "", "{\"path\": \"input.txt\"}")

	calls := h.GetToolCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}

	// Index 0 should have read
	if calls[0].Name != "read" {
		t.Errorf("tool at index 0: expected name 'read', got '%s'", calls[0].Name)
	}

	// Index 1 should have write
	if calls[1].Name != "write" {
		t.Errorf("tool at index 1: expected name 'write', got '%s'", calls[1].Name)
	}
}

func TestAccumulateToolCall_EmptyArguments(t *testing.T) {
	h := &mockHandler{}

	// Simulate tool call with empty arguments
	h.AccumulateToolCall(0, "bash", "")

	calls := h.GetToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "bash" {
		t.Errorf("expected name 'bash', got '%s'", calls[0].Name)
	}
	if calls[0].Argument != "" {
		t.Errorf("expected empty argument, got '%s'", calls[0].Argument)
	}
}

func TestAccumulateToolCall_MultipleToolsWithPartialArgs(t *testing.T) {
	h := &mockHandler{}

	// Simulate interleaved chunks from multiple tools
	// Tool 0 starts
	h.AccumulateToolCall(0, "read", "")
	h.AccumulateToolCall(0, "", "{\"path\": \"file")

	// Tool 1 starts (interleaved)
	h.AccumulateToolCall(1, "read", "")
	h.AccumulateToolCall(1, "", "{\"path\": \"other")

	// Tool 0 continues
	h.AccumulateToolCall(0, "", ".txt\"}")

	// Tool 1 continues
	h.AccumulateToolCall(1, "", ".txt\"}")

	calls := h.GetToolCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}

	// Each tool should have its own complete argument
	if calls[0].Argument != "{\"path\": \"file.txt\"}" {
		t.Errorf("tool 0: expected '{\"path\": \"file.txt\"}', got '%s'", calls[0].Argument)
	}

	if calls[1].Argument != "{\"path\": \"other.txt\"}" {
		t.Errorf("tool 1: expected '{\"path\": \"other.txt\"}', got '%s'", calls[1].Argument)
	}
}
