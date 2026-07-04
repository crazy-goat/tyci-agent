package tools

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
)

// mockRunner implements SubAgentRunner for testing
type mockRunner struct {
	RunTaskFunc func(ctx context.Context, task string, model string, opts SubagentOptions) (string, error)
}

func (m *mockRunner) RunTask(ctx context.Context, task string, model string, opts SubagentOptions) (string, error) {
	if m.RunTaskFunc != nil {
		return m.RunTaskFunc(ctx, task, model, opts)
	}
	return "mock response", nil
}

func (m *mockRunner) RunTaskWithSystem(ctx context.Context, task string, model string, system string, opts SubagentOptions) (string, error) {
	if m.RunTaskFunc != nil {
		return m.RunTaskFunc(ctx, task, model, opts)
	}
	return "mock response with custom system", nil
}

// failingRunner always returns an error
type failingRunner struct{}

func (f *failingRunner) RunTask(ctx context.Context, task string, model string, opts SubagentOptions) (string, error) {
	return "", fmt.Errorf("agent failed")
}

func (f *failingRunner) RunTaskWithSystem(ctx context.Context, task string, model string, system string, opts SubagentOptions) (string, error) {
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

func TestParseTasks_MaxIterations_Single(t *testing.T) {
	v := 25
	input := map[string]any{"task": "do it", "max_iterations": v}
	tasks, err := parseTasks(input, "default/model")
	if err != nil {
		t.Fatalf("parseTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].MaxIterations == nil || *tasks[0].MaxIterations != v {
		t.Errorf("expected MaxIterations=%d, got %v", v, tasks[0].MaxIterations)
	}
}

func TestParseTasks_MaxIterations_PerItem(t *testing.T) {
	v1, v2 := 5, -1
	input := map[string]any{
		"tasks": []any{
			map[string]any{"task": "t1", "max_iterations": v1},
			map[string]any{"task": "t2", "max_iterations": v2},
			map[string]any{"task": "t3"}, // omitted → nil
		},
	}
	tasks, err := parseTasks(input, "default/model")
	if err != nil {
		t.Fatalf("parseTasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	if tasks[0].MaxIterations == nil || *tasks[0].MaxIterations != v1 {
		t.Errorf("tasks[0].MaxIterations: expected %d, got %v", v1, tasks[0].MaxIterations)
	}
	if tasks[1].MaxIterations == nil || *tasks[1].MaxIterations != v2 {
		t.Errorf("tasks[1].MaxIterations: expected %d, got %v", v2, tasks[1].MaxIterations)
	}
	if tasks[2].MaxIterations != nil {
		t.Errorf("tasks[2].MaxIterations: expected nil, got %v", *tasks[2].MaxIterations)
	}
}

func TestParseTasks_MaxIterations_InvalidType(t *testing.T) {
	input := map[string]any{"task": "do it", "max_iterations": "not a number"}
	_, err := parseTasks(input, "default/model")
	if err == nil {
		t.Fatal("expected error for non-integer max_iterations")
	}
	if !strings.Contains(err.Error(), "max_iterations") {
		t.Errorf("expected error mentioning 'max_iterations', got %v", err)
	}
}

// TestResolveMaxIter locks down the contract used by main.go: nil → default
// (unlimited), 0/negative → unlimited, positive → that value. Regression
// guard against the previous ad-hoc `*opts.MaxIterations != 0` check.
func TestResolveMaxIter(t *testing.T) {
	cases := []struct {
		name string
		opts SubagentOptions
		want int
	}{
		{"nil → unlimited", SubagentOptions{MaxIterations: nil}, DefaultSubagentMaxIterations},
		{"zero → unlimited", SubagentOptions{MaxIterations: ptr(0)}, DefaultSubagentMaxIterations},
		{"negative → unlimited", SubagentOptions{MaxIterations: ptr(-1)}, DefaultSubagentMaxIterations},
		{"deeply negative → unlimited", SubagentOptions{MaxIterations: ptr(-1000)}, DefaultSubagentMaxIterations},
		{"positive one", SubagentOptions{MaxIterations: ptr(1)}, 1},
		{"positive fifty", SubagentOptions{MaxIterations: ptr(50)}, 50},
		{"math.MaxInt", SubagentOptions{MaxIterations: ptr(math.MaxInt)}, math.MaxInt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveMaxIter(c.opts)
			if got != c.want {
				t.Errorf("ResolveMaxIter(%+v) = %d, want %d", c.opts, got, c.want)
			}
		})
	}
}

func ptr(i int) *int { return &i }

// TestParseTasks_MaxIterations_Float64 covers the real production path:
// encoding/json always delivers JSON numbers as float64, not int. The
// toInt helper must accept and faithfully truncate them.
func TestParseTasks_MaxIterations_Float64(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want int
	}{
		{"integer-valued", 25.0, 25},
		{"zero", 0.0, 0},
		{"negative whole", -1.0, -1},
		{"large safe value", 1e9, 1_000_000_000},
		{"truncation toward zero (positive fraction)", 9.9, 9},
		{"truncation toward zero (negative fraction)", -9.9, -9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			input := map[string]any{"task": "x", "max_iterations": c.in}
			tasks, err := parseTasks(input, "")
			if err != nil {
				t.Fatalf("parseTasks: %v", err)
			}
			if tasks[0].MaxIterations == nil || *tasks[0].MaxIterations != c.want {
				t.Errorf("got %v, want %d", tasks[0].MaxIterations, c.want)
			}
		})
	}
}

// TestToInt_RejectsBadFloats ensures a runaway model value can't silently
// let a child agent run to math.MaxInt64 iterations.
func TestToInt_RejectsBadFloats(t *testing.T) {
	cases := []struct {
		name string
		in   float64
	}{
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
		{"NaN", math.NaN()},
		{"overflow positive", 1e30},
		{"overflow negative", -1e30},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := toInt(c.in)
			if err == nil {
				t.Errorf("toInt(%v): expected error, got nil", c.in)
			}
		})
	}
}

// TestRunSingleTask_PropagatesMaxIter ensures the parsed MaxIterations
// actually flows through to the runner interface — not lost in the build of
// SubagentOptions inside runSingleTask.
func TestRunSingleTask_PropagatesMaxIter(t *testing.T) {
	var captured *int
	runner := &mockRunner{
		RunTaskFunc: func(_ context.Context, _ string, _ string, opts SubagentOptions) (string, error) {
			captured = opts.MaxIterations
			return "ok", nil
		},
	}
	v := 42
	task := subagentTask{Task: "do", MaxIterations: &v}
	res := runSingleTask(context.Background(), runner, task, 0)
	if !res.Success || res.Content != "ok" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if captured == nil || *captured != v {
		t.Errorf("runner received MaxIterations=%v, want %d", captured, v)
	}
}

// TestRunSingleTask_NilMaxIter passes nil through (parent omitted the
// field); the runner should see nil and resolve via the default.
func TestRunSingleTask_NilMaxIter(t *testing.T) {
	var captured *int
	runner := &mockRunner{
		RunTaskFunc: func(_ context.Context, _ string, _ string, opts SubagentOptions) (string, error) {
			captured = opts.MaxIterations
			return "ok", nil
		},
	}
	task := subagentTask{Task: "do"} // MaxIterations nil
	runSingleTask(context.Background(), runner, task, 0)
	if captured != nil {
		t.Errorf("runner received MaxIterations=%v, want nil", captured)
	}
}

// TestRunSingleTask_TruncatedOnCapHit is the headline fix from the code
// review: when the runner returns ErrSubagentTruncated (even with text)
// the subagentResult must be Success=true with Truncated=true, so the
// parent can distinguish a partial answer from a clean completion.
func TestRunSingleTask_TruncatedOnCapHit(t *testing.T) {
	runner := &mockRunner{
		RunTaskFunc: func(_ context.Context, _, _ string, _ SubagentOptions) (string, error) {
			return "partial answer\n\n[note: ...]", fmt.Errorf("hit cap: %w", ErrSubagentTruncated)
		},
	}
	res := runSingleTask(context.Background(), runner, subagentTask{Task: "x"}, 0)
	if !res.Success {
		t.Errorf("expected Success=true on partial truncation, got false (Error=%q)", res.Error)
	}
	if !res.Truncated {
		t.Errorf("expected Truncated=true, got false")
	}
	if res.Error != "" {
		t.Errorf("expected empty Error on partial truncation, got %q", res.Error)
	}
	if res.Content != "partial answer\n\n[note: ...]" {
		t.Errorf("unexpected Content: %q", res.Content)
	}
}

// TestRunSingleTask_TruncatedOnEmptyText documents the wrapper-side
// behavior: when the runner returns ErrSubagentTruncated, runSingleTask
// treats it as "truncated" regardless of whether text came back. main.go
// is responsible for the upstream policy (empty + wrapped → hard error
// before reaching here, see agentRunner.run); the tools-side wrapper just
// surfaces the sentinel faithfully.
func TestRunSingleTask_TruncatedOnEmptyText(t *testing.T) {
	runner := &mockRunner{
		RunTaskFunc: func(_ context.Context, _, _ string, _ SubagentOptions) (string, error) {
			return "", fmt.Errorf("hit cap: %w", ErrSubagentTruncated)
		},
	}
	res := runSingleTask(context.Background(), runner, subagentTask{Task: "x"}, 0)
	if !res.Success {
		t.Errorf("expected Success=true when runner wraps ErrSubagentTruncated, got false (Error=%q)", res.Error)
	}
	if !res.Truncated {
		t.Errorf("expected Truncated=true, got false")
	}
	if res.Error != "" {
		t.Errorf("expected empty Error on truncated sentinel, got %q", res.Error)
	}
}

// TestTruncatedMarker_Stable locks the marker string so format drift
// between tools/subagent.go (the producer) and the main-package adapter
// (the consumer) is caught by tests.
func TestTruncatedMarker_Stable(t *testing.T) {
	const want = "[truncated=true]"
	if TruncatedMarker != want {
		t.Errorf("TruncatedMarker drifted from %q to %q", want, TruncatedMarker)
	}
}

// TestRunSingleTask_TruncatedFlagReachesToolResult is the end-to-end Go-level
// contract for the propagation chain: SetSubAgentRunner(...)
// → tools.RunTool("subagent", ...) → ToolResult with Truncated=true on the
// single-task code path. Parallel-array case mirrors this via the embedded
// subagentResult.Truncated (json tag). The parent-LLM-facing string is the
// adapter's job and is verified separately by the integration smoke test.
func TestRunSingleTask_TruncatedFlagReachesToolResult(t *testing.T) {
	t.Cleanup(func() {
		delete(toolRegistry, "subagent")
		subagentToolInstance = nil
	})
	SetSubAgentRunner(&mockRunner{
		RunTaskFunc: func(_ context.Context, _, _ string, _ SubagentOptions) (string, error) {
			return "partial answer\n\n[note: ...]", fmt.Errorf("hit cap: %w", ErrSubagentTruncated)
		},
	})

	ctx := providers.WithModel(context.Background(), "test/model")
	res := RunTool(ctx, "subagent", map[string]any{"task": "x"})
	if !res.Success {
		t.Fatalf("expected success on partial truncation, got error: %q", res.Error)
	}
	if !res.Truncated {
		t.Errorf("expected ToolResult.Truncated=true, got false (parent LLM would lose the signal)")
	}
	if res.Content != "partial answer\n\n[note: ...]" {
		t.Errorf("unexpected Content: %q", res.Content)
	}
}

// TestRunSingleTask_CleanSuccessNotTruncated is the negative half: a clean
// (non-cap-hit) execution must NOT set Truncated, so the marker isn't
// spuriously appended to a complete answer.
func TestRunSingleTask_CleanSuccessNotTruncated(t *testing.T) {
	t.Cleanup(func() {
		delete(toolRegistry, "subagent")
		subagentToolInstance = nil
	})
	SetSubAgentRunner(&mockRunner{
		RunTaskFunc: func(_ context.Context, _, _ string, _ SubagentOptions) (string, error) {
			return "complete answer", nil
		},
	})

	ctx := providers.WithModel(context.Background(), "test/model")
	res := RunTool(ctx, "subagent", map[string]any{"task": "x"})
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if res.Truncated {
		t.Errorf("expected Truncated=false on clean completion, got true")
	}
	if res.Content != "complete answer" {
		t.Errorf("unexpected Content: %q", res.Content)
	}
}
