package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/decodo/tyci/stream"
)

// mockRunner implements SubAgentRunner for testing
type mockRunner struct {
	RunTaskFunc func(ctx context.Context, task string, model string, temperature float64) (string, error)
}

func (m *mockRunner) RunTask(ctx context.Context, task string, model string, temperature float64) (string, error) {
	if m.RunTaskFunc != nil {
		return m.RunTaskFunc(ctx, task, model, temperature)
	}
	return "mock response", nil
}

func (m *mockRunner) RunTaskWithSystem(ctx context.Context, task string, model string, temperature float64, system string) (string, error) {
	if m.RunTaskFunc != nil {
		return m.RunTaskFunc(ctx, task, model, temperature)
	}
	return "mock response with custom system", nil
}

// failingRunner always returns an error
type failingRunner struct{}

func (f *failingRunner) RunTask(ctx context.Context, task string, model string, temperature float64) (string, error) {
	return "", fmt.Errorf("agent failed")
}

func (f *failingRunner) RunTaskWithSystem(ctx context.Context, task string, model string, temperature float64, system string) (string, error) {
	return "", fmt.Errorf("agent failed")
}

func TestCollector_Text(t *testing.T) {
	c := newCollector()
	c.Text("hello")
	c.Text(" world")
	res := c.Result()
	if res.Content != "hello world" {
		t.Errorf("expected 'hello world', got %q", res.Content)
	}
}

func TestCollector_Thinking(t *testing.T) {
	c := newCollector()
	c.Thinking("step 1")
	c.Thinking(" step 2")
	res := c.Result()
	if res.Thinking != "step 1 step 2" {
		t.Errorf("expected 'step 1 step 2', got %q", res.Thinking)
	}
}

func TestCollector_ToolCalls(t *testing.T) {
	c := newCollector()
	c.ToolCallStart("bash")
	c.ToolCallStart("read")
	res := c.Result()
	if res.ToolCalls != 2 {
		t.Errorf("expected 2 tool calls, got %d", res.ToolCalls)
	}
}

func TestCollector_Usage(t *testing.T) {
	c := newCollector()
	c.Summary(stream.Usage{Input: 100, Output: 50, Reasoning: 25}, stream.Stats{})
	res := c.Result()
	if res.Usage.Input != 100 {
		t.Errorf("expected Input 100, got %d", res.Usage.Input)
	}
	if res.Usage.Output != 50 {
		t.Errorf("expected Output 50, got %d", res.Usage.Output)
	}
	if res.Usage.Reasoning != 25 {
		t.Errorf("expected Reasoning 25, got %d", res.Usage.Reasoning)
	}
}

func TestCollector_Concurrency(t *testing.T) {
	c := newCollector()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Text("a")
			c.Thinking("b")
			c.ToolCallStart("test")
		}()
	}
	wg.Wait()
	res := c.Result()
	if res.ToolCalls != 10 {
		t.Errorf("expected 10 tool calls, got %d", res.ToolCalls)
	}
	if len(res.Content) != 10 {
		t.Errorf("expected 10 chars of text, got %d", len(res.Content))
	}
}

// mockOutput implements the parentOutput callback for testing streamingCollector.
// It records each call: toolIdx and line.
type mockOutput struct {
	mu    sync.Mutex
	calls []outputCall
}

type outputCall struct {
	toolIdx int
	line    string
}

func (m *mockOutput) call(toolIdx int, line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, outputCall{toolIdx, line})
}

func (m *mockOutput) lines() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var lines []string
	for _, c := range m.calls {
		lines = append(lines, c.line)
	}
	return lines
}

func TestStreamingCollector_ForwardsTextLines(t *testing.T) {
	mo := &mockOutput{}
	ctx := stream.WithOutput(context.Background(), mo.call)
	sc := newStreamingCollector(ctx, 2)
	if sc.parentOutput == nil {
		t.Fatal("parentOutput is nil — stream.OnOutput was not set")
	}

	// Text with newlines → should forward complete lines
	sc.Text("line1\nline2\n")
	sc.flushPartial()

	lines := mo.lines()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines forwarded, got %d: %q", len(lines), lines)
	}
	if lines[0] != "line1" {
		t.Errorf("expected 'line1', got %q", lines[0])
	}
	if lines[1] != "line2" {
		t.Errorf("expected 'line2', got %q", lines[1])
	}
}

func TestStreamingCollector_ForwardsThinkingLines(t *testing.T) {
	mo := &mockOutput{}
	ctx := stream.WithOutput(context.Background(), mo.call)
	sc := newStreamingCollector(ctx, 3)
	sc.Thinking("thought\n")

	lines := mo.lines()
	if len(lines) != 1 || lines[0] != "thought" {
		t.Errorf("expected ['thought'], got %q", lines)
	}
}

func TestStreamingCollector_FlushesPartialLine(t *testing.T) {
	mo := &mockOutput{}
	ctx := stream.WithOutput(context.Background(), mo.call)
	sc := newStreamingCollector(ctx, 0)
	// Send text without trailing newline
	sc.Text("partial line without newline")

	// Nothing forwarded yet (buffered)
	if len(mo.lines()) != 0 {
		t.Errorf("expected no lines before flush, got %q", mo.lines())
	}

	// After flush, partial should appear
	sc.flushPartial()
	lines := mo.lines()
	if len(lines) != 1 || lines[0] != "partial line without newline" {
		t.Errorf("expected ['partial line without newline'], got %q", lines)
	}
}

func TestStreamingCollector_UsesCorrectToolIdx(t *testing.T) {
	mo := &mockOutput{}
	ctx := stream.WithOutput(context.Background(), mo.call)
	sc := newStreamingCollector(ctx, 7)
	sc.Text("test\n")
	sc.flushPartial()

	if len(mo.calls) != 1 || mo.calls[0].toolIdx != 7 {
		t.Errorf("expected toolIdx=7, got %d; calls=%+v", mo.calls[0].toolIdx, mo.calls)
	}
}

func TestStreamingCollector_StreamProgressForwardsCorrectly(t *testing.T) {
	mo := &mockOutput{}
	ctx := stream.WithOutput(context.Background(), mo.call)
	sc := newStreamingCollector(ctx, 5)
	// Simulate what subagent's runOnce does when a bash tool runs
	// StreamProgress is called with inner toolIdx but it ignores it and uses sc.toolIdx
	sc.StreamProgress(0, "bash output line 1")
	sc.StreamProgress(1, "bash output line 2")

	if len(mo.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(mo.calls))
	}
	if mo.calls[0].toolIdx != 5 || mo.calls[0].line != "bash output line 1" {
		t.Errorf("call[0]: expected toolIdx=5, line='bash output line 1', got %+v", mo.calls[0])
	}
	if mo.calls[1].toolIdx != 5 || mo.calls[1].line != "bash output line 2" {
		t.Errorf("call[1]: expected toolIdx=5, line='bash output line 2', got %+v", mo.calls[1])
	}
}

func TestStreamingCollector_ParentOutputNilSkipsForwarding(t *testing.T) {
	// When stream.Output(ctx) is nil, streamingCollector should not panic or forward
	ctx := context.Background()
	sc := newStreamingCollector(ctx, 0)
	// These should not panic
	sc.Text("hello\n")
	sc.Thinking("think\n")
	sc.StreamProgress(0, "output")
	sc.flushPartial()
}

func TestStreamingCollector_TextAndThinkingMixed(t *testing.T) {
	mo := &mockOutput{}
	ctx := stream.WithOutput(context.Background(), mo.call)
	sc := newStreamingCollector(ctx, 1)
	sc.Text("I'll look up the file.\n")
	sc.Thinking("Let me check README...\n")
	sc.Thinking("Done thinking.\n")
	sc.flushPartial()

	lines := mo.lines()
	expected := []string{"I'll look up the file.", "Let me check README...", "Done thinking."}
	if len(lines) != len(expected) {
		t.Fatalf("expected %d lines, got %d: %q", len(expected), len(lines), lines)
	}
	for i, exp := range expected {
		if lines[i] != exp {
			t.Errorf("line[%d]: expected %q, got %q", i, exp, lines[i])
		}
	}
}

// TestToolIdxCtxKey verifies that ToolIdxCtxKey can be stored and retrieved.
func TestToolIdxCtxKey(t *testing.T) {
	ctx := context.WithValue(context.Background(), stream.ToolIdxCtxKey{}, 42)
	if val, ok := ctx.Value(stream.ToolIdxCtxKey{}).(int); !ok {
		t.Fatal("ToolIdxCtxKey not found in context")
	} else if val != 42 {
		t.Errorf("expected 42, got %d", val)
	}
}

func TestParseTasks_SingleTask(t *testing.T) {
	input := map[string]any{"task": "do something"}
	tasks, err := parseTasks(input, "default/model")
	if err != nil {
		t.Fatalf("parseTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Task != "do something" {
		t.Errorf("expected 'do something', got %q", tasks[0].Task)
	}
	if tasks[0].Model != "" {
		t.Errorf("expected empty model override, got %q", tasks[0].Model)
	}
}

func TestParseTasks_SingleTaskWithModel(t *testing.T) {
	input := map[string]any{"task": "do something", "model": "other/model"}
	tasks, err := parseTasks(input, "default/model")
	if err != nil {
		t.Fatalf("parseTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Task != "do something" {
		t.Errorf("expected 'do something', got %q", tasks[0].Task)
	}
	if tasks[0].Model != "other/model" {
		t.Errorf("expected 'other/model', got %q", tasks[0].Model)
	}
}

func TestParseTasks_MultipleTasks(t *testing.T) {
	input := map[string]any{
		"tasks": []any{
			map[string]any{"task": "task 1"},
			map[string]any{"task": "task 2", "model": "other/model"},
		},
	}
	tasks, err := parseTasks(input, "default/model")
	if err != nil {
		t.Fatalf("parseTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Task != "task 1" {
		t.Errorf("expected 'task 1', got %q", tasks[0].Task)
	}
	if tasks[1].Task != "task 2" {
		t.Errorf("expected 'task 2', got %q", tasks[1].Task)
	}
	if tasks[1].Model != "other/model" {
		t.Errorf("expected 'other/model', got %q", tasks[1].Model)
	}
}

func TestParseTasks_EmptyInput(t *testing.T) {
	_, err := parseTasks(map[string]any{}, "default/model")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
	if !strings.Contains(err.Error(), "task") {
		t.Errorf("expected error mentioning 'task', got %v", err)
	}
}

func TestParseTasks_TasksArrayEmpty(t *testing.T) {
	_, err := parseTasks(map[string]any{"tasks": []any{}}, "default/model")
	if err == nil {
		t.Fatal("expected error for empty tasks array")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected error mentioning 'empty', got %v", err)
	}
}

// TestSetSubAgentRunner_RegistersTool is the regression test for the wiring bug:
// the "subagent" tool is advertised in the schema, so it must also be present in
// the executable registry. Before the fix, SetSubAgentRunner was never called and
// RunTool returned "unknown tool: subagent".
func TestSetSubAgentRunner_RegistersTool(t *testing.T) {
	if _, ok := toolRegistry["subagent"]; ok {
		t.Fatal("precondition: subagent should not be registered before SetSubAgentRunner")
	}
	t.Cleanup(func() {
		delete(toolRegistry, "subagent")
		subagentToolInstance = nil
	})

	SetSubAgentRunner(&mockRunner{})

	tool, ok := toolRegistry["subagent"]
	if !ok {
		t.Fatal("subagent tool not registered after SetSubAgentRunner")
	}
	if tool.Name() != "subagent" {
		t.Errorf("expected registered tool name 'subagent', got %q", tool.Name())
	}

	// RunTool must now dispatch to it rather than returning "unknown tool".
	res := RunTool(context.Background(), "subagent", map[string]any{})
	if res.Success {
		t.Fatal("expected failure for empty task, but tool should still be reached")
	}
	if strings.Contains(res.Error, "unknown tool") {
		t.Errorf("subagent still not reachable via RunTool: %q", res.Error)
	}
}

func TestSubagentTool_MissingRunner(t *testing.T) {
	tool := &SubagentTool{}
	// Context without runner
	result := tool.Run(context.Background(), map[string]any{"task": "test"})
	if result.Success {
		t.Fatal("expected failure when no runner")
	}
	if !strings.Contains(result.Error, "subagent runner not configured") {
		t.Errorf("expected 'subagent runner not configured' error, got %q", result.Error)
	}
}

func TestSubagentTool_MissingModel(t *testing.T) {
	tool := &SubagentTool{Runner: &mockRunner{}}
	// Context with runner but no model
	result := tool.Run(context.Background(), map[string]any{"task": "test"})
	if result.Success {
		t.Fatal("expected failure when no model")
	}
	if !strings.Contains(result.Error, "no model") {
		t.Errorf("expected 'no model' error, got %q", result.Error)
	}
}

func TestStreamingCollector_PreservesThreadSafety(t *testing.T) {
	mo := &mockOutput{}
	ctx := stream.WithOutput(context.Background(), mo.call)
	sc := newStreamingCollector(ctx, 0)
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sc.Text("a\n")
			sc.Thinking("b\n")
		}()
	}
	wg.Wait()
	sc.flushPartial()

	// Should have 40 lines (20 text + 20 thinking)
	lines := mo.lines()
	if len(lines) != 40 {
		t.Errorf("expected 40 lines, got %d", len(lines))
	}
}
