package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/stream"
)

// fakeModelClient returns the minimal connector.ModelClient these tests need:
// an identity to put in the context, and a Stream that refuses to run. The
// subagent path under test reads the default model off the context and never
// sends a request, so a client that streamed would only hide a mistake.
func fakeModelClient(model string) *connectortest.Fake {
	return &connectortest.Fake{
		ProviderName: "test-provider",
		ModelName:    model,
		StreamErr:    errors.New("fakeModelClient does not stream"),
	}
}

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
	for _, want := range []string{
		"tasks array is empty",
		`single task`,
		`{"task":"..."}`,
		`non-empty tasks array`,
		`{"tasks":[{"task":"..."}]}`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got %v", want, err)
		}
	}
}

// TestSetSubAgentRunner_RegistersTool is the regression test for the wiring bug:
// the "subagent" tool is advertised in the schema, so it must also be present in
// the executable registry. Before the fix, SetSubAgentRunner was never called and
// RunTool returned "unknown tool: subagent".
func TestSetSubAgentRunner_RegistersTool(t *testing.T) {
	if _, ok := lookupTool("subagent"); ok {
		t.Fatal("precondition: subagent should not be registered before SetSubAgentRunner")
	}
	t.Cleanup(func() {
		unregisterTool("subagent")
		subagentToolInstance = nil
	})

	SetSubAgentRunner(&mockRunner{})

	tool, ok := lookupTool("subagent")
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

// =============================================================================
// Agent-definition wiring: agentdefs.Get(...) → SubagentOptions/model/system
// =============================================================================
//
// These tests exercise runSingleTask's precedence rules for a named agent's
// frontmatter (model, max_iterations, tools, system prompt) against a
// per-task override and the parent's inherited model. They are hermetic:
// each test gets its own HOME and cwd so agentdefs never sees the real
// ~/.tyci/agents.

// recordingRunner captures everything runSingleTask handed to the
// SubAgentRunner interface, plus which method was called — the mockRunner
// above collapses RunTask/RunTaskWithSystem into one callback and drops the
// system prompt, which is exactly what these tests need to observe.
type recordingRunner struct {
	called       bool
	withSystem   bool
	gotModel     string
	gotSystem    string
	gotOpts      SubagentOptions
	returnErr    error
	returnResult string
}

func (r *recordingRunner) RunTask(_ context.Context, _ string, model string, opts SubagentOptions) (string, error) {
	r.called = true
	r.withSystem = false
	r.gotModel = model
	r.gotOpts = opts
	if r.returnResult == "" && r.returnErr == nil {
		return "ok", nil
	}
	return r.returnResult, r.returnErr
}

func (r *recordingRunner) RunTaskWithSystem(_ context.Context, _ string, model string, system string, opts SubagentOptions) (string, error) {
	r.called = true
	r.withSystem = true
	r.gotModel = model
	r.gotSystem = system
	r.gotOpts = opts
	if r.returnResult == "" && r.returnErr == nil {
		return "ok", nil
	}
	return r.returnResult, r.returnErr
}

// setupHermeticAgentDirs points agentdefs at a throwaway HOME (global agents
// dir) and cwd (project-local agents dir), so tests never touch the real
// ~/.tyci/agents. Returns the project-local .tyci/agents directory, already
// created, where test agent .md files should be written.
func setupHermeticAgentDirs(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	t.Chdir(wd)

	projectAgentsDir := filepath.Join(wd, ".tyci", "agents")
	if err := os.MkdirAll(projectAgentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	return projectAgentsDir
}

func writeTestAgent(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// ctxWithParentModel builds a context carrying a parent ModelClient, the
// same way agent.executeTools would when invoking the subagent tool.
func ctxWithParentModel(model string) context.Context {
	return connector.WithModelClient(context.Background(), fakeModelClient(model))
}

// --- Model precedence: task.Model > def.Model > parent model ---------------

func TestRunSingleTask_ModelPrecedence_TaskWins(t *testing.T) {
	dir := setupHermeticAgentDirs(t)
	writeTestAgent(t, dir, "myagent", "---\nmodel: def-model\n---\nbody")

	r := &recordingRunner{}
	task := subagentTask{Task: "x", Agent: "myagent", Model: "task-model"}
	res := runSingleTask(ctxWithParentModel("parent-model"), r, task, 0, true)

	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if r.gotModel != "task-model" {
		t.Errorf("expected task.Model to win, got %q", r.gotModel)
	}
}

func TestRunSingleTask_ModelPrecedence_DefWins(t *testing.T) {
	dir := setupHermeticAgentDirs(t)
	writeTestAgent(t, dir, "myagent", "---\nmodel: def-model\n---\nbody")

	r := &recordingRunner{}
	task := subagentTask{Task: "x", Agent: "myagent"} // no task.Model
	res := runSingleTask(ctxWithParentModel("parent-model"), r, task, 0, true)

	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if r.gotModel != "def-model" {
		t.Errorf("expected def.Model to win over parent model, got %q", r.gotModel)
	}
}

func TestRunSingleTask_ModelPrecedence_ParentWins(t *testing.T) {
	// No agent at all — nothing to look up, so def.Model is never in play.
	setupHermeticAgentDirs(t)

	r := &recordingRunner{}
	task := subagentTask{Task: "x"}
	res := runSingleTask(ctxWithParentModel("parent-model"), r, task, 0, true)

	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if r.gotModel != "parent-model" {
		t.Errorf("expected parent model as last resort, got %q", r.gotModel)
	}
}

// --- max_iterations compatibility: accepted and ignored ---------------------

func TestRunSingleTask_MaxIterationsAreIgnored(t *testing.T) {
	dir := setupHermeticAgentDirs(t)
	writeTestAgent(t, dir, "myagent", "---\nmax_iterations: 7\n---\nbody")

	r := &recordingRunner{}
	v := 3
	task := subagentTask{Task: "x", Agent: "myagent", MaxIterations: &v}
	runSingleTask(context.Background(), r, task, 0, true)

	if r.gotOpts.MaxIterations != nil {
		t.Errorf("expected MaxIterations to be ignored for subagents, got %v", *r.gotOpts.MaxIterations)
	}
}

// --- System prompt: def.SystemPrompt routes to RunTaskWithSystem -----------

func TestRunSingleTask_SystemPromptFromDef_UsesRunTaskWithSystem(t *testing.T) {
	dir := setupHermeticAgentDirs(t)
	writeTestAgent(t, dir, "myagent", "---\ndescription: has a body\n---\nYou are a careful reviewer.")

	r := &recordingRunner{}
	task := subagentTask{Task: "x", Agent: "myagent"}
	res := runSingleTask(context.Background(), r, task, 0, true)

	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}
	if !r.withSystem {
		t.Fatal("expected RunTaskWithSystem to be called, RunTask was called instead")
	}
	if r.gotSystem != "You are a careful reviewer." {
		t.Errorf("expected the agent's markdown body as system prompt, got %q", r.gotSystem)
	}
}

func TestRunSingleTask_NoAgent_UsesPlainRunTask(t *testing.T) {
	setupHermeticAgentDirs(t)

	r := &recordingRunner{}
	task := subagentTask{Task: "x"} // no Agent
	runSingleTask(context.Background(), r, task, 0, true)

	if r.withSystem {
		t.Error("expected RunTask (no system prompt) for a plain subagent, RunTaskWithSystem was called")
	}
}

// --- Unknown agent: hard failure, not a silent fallback ---------------------

// TestRunSingleTask_UnknownAgent_HardFails locks in the deliberate behavior
// change from the old code: a typo'd agent name used to silently degrade to
// a plain subagent using the parent's model/prompt, which was
// indistinguishable from a correctly-resolved agent and masked typos. It now
// fails outright and never touches the runner.
func TestRunSingleTask_UnknownAgent_HardFails(t *testing.T) {
	setupHermeticAgentDirs(t) // no agent files written — "ghost" cannot exist

	r := &recordingRunner{}
	task := subagentTask{Task: "x", Agent: "ghost"}
	res := runSingleTask(context.Background(), r, task, 0, true)

	if res.Success {
		t.Fatal("expected failure for unknown agent, got success")
	}
	if r.called {
		t.Error("runner must not be invoked when the named agent cannot be resolved")
	}
	wantSubstr := `agent "ghost" not found (looked in ~/.tyci/agents and ./.tyci/agents)`
	if res.Error != wantSubstr {
		t.Errorf("unexpected error message: got %q, want %q", res.Error, wantSubstr)
	}
}

// --- def.Tools flows into SubagentOptions.Tools -----------------------------

func TestRunSingleTask_DefToolsReachesOptions(t *testing.T) {
	dir := setupHermeticAgentDirs(t)
	writeTestAgent(t, dir, "myagent", "---\ntools: read, bash\n---\nbody")

	r := &recordingRunner{}
	task := subagentTask{Task: "x", Agent: "myagent"}
	runSingleTask(context.Background(), r, task, 0, true)

	want := []string{"read", "bash"}
	if len(r.gotOpts.Tools) != len(want) {
		t.Fatalf("expected Tools=%v, got %v", want, r.gotOpts.Tools)
	}
	for i, name := range want {
		if r.gotOpts.Tools[i] != name {
			t.Errorf("Tools[%d]: expected %q, got %q", i, name, r.gotOpts.Tools[i])
		}
	}
}

// --- def.SystemPromptMode flows into SubagentOptions.SystemPromptMode ------

// TestRunSingleTask_SystemPromptMode_AppendReachesOptions covers the default
// mode: agentdefs.Parse normalizes an omitted `system_prompt_mode` to
// "append" (see agentdefs_test.go), and runSingleTask must carry that value
// through to SubagentOptions unchanged so main.go's RunTaskWithSystem can act
// on it.
func TestRunSingleTask_SystemPromptMode_AppendReachesOptions(t *testing.T) {
	dir := setupHermeticAgentDirs(t)
	writeTestAgent(t, dir, "myagent", "---\ndescription: no mode set\n---\nbody")

	r := &recordingRunner{}
	task := subagentTask{Task: "x", Agent: "myagent"}
	runSingleTask(context.Background(), r, task, 0, true)

	if r.gotOpts.SystemPromptMode != "append" {
		t.Errorf("expected SystemPromptMode=%q, got %q", "append", r.gotOpts.SystemPromptMode)
	}
}

// TestRunSingleTask_SystemPromptMode_ReplaceReachesOptions covers the
// explicit opt-in: `system_prompt_mode: replace` in the frontmatter must
// reach SubagentOptions.SystemPromptMode as "replace".
func TestRunSingleTask_SystemPromptMode_ReplaceReachesOptions(t *testing.T) {
	dir := setupHermeticAgentDirs(t)
	writeTestAgent(t, dir, "myagent", "---\nsystem_prompt_mode: replace\n---\nbody")

	r := &recordingRunner{}
	task := subagentTask{Task: "x", Agent: "myagent"}
	runSingleTask(context.Background(), r, task, 0, true)

	if r.gotOpts.SystemPromptMode != "replace" {
		t.Errorf("expected SystemPromptMode=%q, got %q", "replace", r.gotOpts.SystemPromptMode)
	}
}

func TestRunSingleTask_NoAgent_ToolsIsNil(t *testing.T) {
	setupHermeticAgentDirs(t)

	r := &recordingRunner{}
	task := subagentTask{Task: "x"}
	runSingleTask(context.Background(), r, task, 0, true)

	if r.gotOpts.Tools != nil {
		t.Errorf("expected nil Tools for a plain subagent, got %v", r.gotOpts.Tools)
	}
}

// --- def.Temperature flows into SubagentOptions.Temperature ----------------

func TestRunSingleTask_DefTemperatureReachesOptions(t *testing.T) {
	dir := setupHermeticAgentDirs(t)
	writeTestAgent(t, dir, "myagent", "---\ntemperature: 0.4\n---\nbody")

	r := &recordingRunner{}
	task := subagentTask{Task: "x", Agent: "myagent"}
	runSingleTask(context.Background(), r, task, 0, true)

	if r.gotOpts.Temperature == nil || *r.gotOpts.Temperature != 0.4 {
		t.Errorf("expected Temperature=0.4, got %v", r.gotOpts.Temperature)
	}
}

// temperature: 0 is a legal, meaningful value ("deterministic") and must
// still reach SubagentOptions as a non-nil pointer to 0 — not nil, which
// would mean "unset".
func TestRunSingleTask_DefTemperatureZeroReachesOptionsAsNonNil(t *testing.T) {
	dir := setupHermeticAgentDirs(t)
	writeTestAgent(t, dir, "myagent", "---\ntemperature: 0\n---\nbody")

	r := &recordingRunner{}
	task := subagentTask{Task: "x", Agent: "myagent"}
	runSingleTask(context.Background(), r, task, 0, true)

	if r.gotOpts.Temperature == nil {
		t.Fatal("expected a non-nil pointer to 0.0, got nil")
	}
	if *r.gotOpts.Temperature != 0.0 {
		t.Errorf("expected Temperature=0.0, got %v", *r.gotOpts.Temperature)
	}
}

func TestRunSingleTask_NoAgent_TemperatureIsNil(t *testing.T) {
	setupHermeticAgentDirs(t)

	r := &recordingRunner{}
	task := subagentTask{Task: "x"}
	runSingleTask(context.Background(), r, task, 0, true)

	if r.gotOpts.Temperature != nil {
		t.Errorf("expected nil Temperature for a plain subagent, got %v", *r.gotOpts.Temperature)
	}
}

func TestRunSingleTask_NoTemperatureInFrontmatter_IsNil(t *testing.T) {
	dir := setupHermeticAgentDirs(t)
	writeTestAgent(t, dir, "myagent", "---\ndescription: no temperature here\n---\nbody")

	r := &recordingRunner{}
	task := subagentTask{Task: "x", Agent: "myagent"}
	runSingleTask(context.Background(), r, task, 0, true)

	if r.gotOpts.Temperature != nil {
		t.Errorf("expected nil Temperature when frontmatter omits it, got %v", *r.gotOpts.Temperature)
	}
}

// --- def.Fallback flows into SubagentOptions.Fallbacks ----------------------

func TestRunSingleTask_DefFallbackReachesOptions(t *testing.T) {
	dir := setupHermeticAgentDirs(t)
	writeTestAgent(t, dir, "myagent", "---\nfallback:\n  - acme/big\n  - acme/small\n---\nbody")

	r := &recordingRunner{}
	task := subagentTask{Task: "x", Agent: "myagent"}
	runSingleTask(context.Background(), r, task, 0, true)

	want := []string{"acme/big", "acme/small"}
	if len(r.gotOpts.Fallbacks) != len(want) {
		t.Fatalf("expected Fallbacks=%v, got %v", want, r.gotOpts.Fallbacks)
	}
	for i, spec := range want {
		if r.gotOpts.Fallbacks[i] != spec {
			t.Errorf("Fallbacks[%d]: expected %q, got %q", i, spec, r.gotOpts.Fallbacks[i])
		}
	}
}

func TestRunSingleTask_NoAgent_FallbacksIsNil(t *testing.T) {
	setupHermeticAgentDirs(t)

	r := &recordingRunner{}
	task := subagentTask{Task: "x"}
	runSingleTask(context.Background(), r, task, 0, true)

	if r.gotOpts.Fallbacks != nil {
		t.Errorf("expected nil Fallbacks for a plain subagent, got %v", r.gotOpts.Fallbacks)
	}
}

func TestRunSingleTask_NoFallbackInFrontmatter_IsNil(t *testing.T) {
	dir := setupHermeticAgentDirs(t)
	writeTestAgent(t, dir, "myagent", "---\ndescription: no fallback here\n---\nbody")

	r := &recordingRunner{}
	task := subagentTask{Task: "x", Agent: "myagent"}
	runSingleTask(context.Background(), r, task, 0, true)

	if r.gotOpts.Fallbacks != nil {
		t.Errorf("expected nil Fallbacks when frontmatter omits it, got %v", r.gotOpts.Fallbacks)
	}
}

// =============================================================================
// GetSubagentToolsSchemaJSONFor — the tool-whitelist filter
// =============================================================================

// schemaToolNames extracts the "function.name" of every entry in a marshaled
// tool schema, for order-insensitive comparisons.
func schemaToolNames(t *testing.T, data []byte) map[string]bool {
	t.Helper()
	var arr []map[string]any
	if err := json.Unmarshal(data, &arr); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	names := make(map[string]bool, len(arr))
	for _, entry := range arr {
		fn, ok := entry["function"].(map[string]any)
		if !ok {
			continue
		}
		name, ok := fn["name"].(string)
		if ok {
			names[name] = true
		}
	}
	return names
}

// TestSubagentSchemaDescription_RelaysRatherThanAnswers guards the last of
// item 29's five reworded strings: the "subagent" tool's own schema
// description (tools/tool.go, near what was line 250) used to mention
// "answer(job_id, text) unblocks a question" as if answering it were
// unconditionally correct. It must now name the renamed answer_job tool
// and carry the same relay-unless-you-know nuance as the other four sites.
func TestSubagentSchemaDescription_RelaysRatherThanAnswers(t *testing.T) {
	var desc string
	for _, entry := range GetToolsSchema() {
		fn, ok := entry["function"].(map[string]any)
		if !ok {
			continue
		}
		if name, _ := fn["name"].(string); name == "subagent" {
			desc, _ = fn["description"].(string)
		}
	}
	if desc == "" {
		t.Fatal("could not find the \"subagent\" tool's schema description")
	}
	if !strings.Contains(desc, "answer_job(job_id, text)") {
		t.Errorf("expected the subagent description to name the renamed answer_job tool, got %q", desc)
	}
	if strings.Contains(desc, "answer(job_id, text) unblocks") {
		t.Errorf("expected the old unqualified \"answer(...) unblocks\" wording to be gone, got %q", desc)
	}
	if !strings.Contains(desc, "never an invented stand-in") {
		t.Errorf("expected the subagent description to say never to invent a stand-in answer, got %q", desc)
	}
}

func TestGetSubagentToolsSchemaJSONFor_EmptyReturnsFullSchema(t *testing.T) {
	got := GetSubagentToolsSchemaJSONFor(nil)
	want := GetSubagentToolsSchemaJSON()
	if string(got) != string(want) {
		t.Errorf("expected empty allowed list to return the cached full schema unchanged")
	}
	names := schemaToolNames(t, got)
	if names["subagent"] {
		t.Error("full subagent schema must never include \"subagent\" itself")
	}
	if names["agents"] {
		t.Error("subagent schema must never include \"agents\" — a child can't spawn subagents, so it has no use for the agent-name discovery tool")
	}
	if !names["bash"] || !names["read"] || !names["lock"] || !names["wait"] || !names["ask_parent"] {
		t.Errorf("expected an unrestricted subagent schema to include bash/read/lock/wait/ask_parent, got %v", names)
	}
	if names["answer_job"] {
		t.Error("subagent schema must never include \"answer_job\" — a plain child cannot spawn further children, so it can never have a descendant blocked on ask_parent to unblock")
	}
}

// TestGetToolsSchema_IncludesAgents locks in that the top-level (non-child)
// schema, unlike the subagent one, does offer "agents" — only the child
// schema drops it.
func TestGetToolsSchema_IncludesAgents(t *testing.T) {
	names := schemaToolNames(t, GetToolsSchemaJSON())
	if !names["agents"] {
		t.Error("expected the top-level tool schema to include \"agents\"")
	}
}

// TestGetTopLevelToolsSchema_ExcludesAskParent guards item 29's role-gating:
// the top-level, non-job main-agent conversation has no job id, so
// AskTool.Run always fails ask_parent immediately ("ask_parent only works
// inside a job"). GetTopLevelToolsSchema is what commands.go actually hands
// the top-level agent.Config as its Schema — it must not offer a tool that
// structurally cannot work there, unlike the full GetToolsSchema/
// GetAllToolsSchema (which subagent children and /btw still need it from).
func TestGetTopLevelToolsSchema_ExcludesAskParent(t *testing.T) {
	names := schemaToolNames(t, GetTopLevelToolsSchemaJSON())
	if names["ask_parent"] {
		t.Error("expected the top-level tool schema to exclude ask_parent — the top-level conversation has no job id")
	}
	// Everything else GetAllToolsSchema offers should still be there —
	// this is a narrow exclusion, not a different, smaller tool set.
	if !names["agents"] || !names["subagent"] || !names["answer_job"] {
		t.Errorf("expected the top-level schema to still include agents/subagent/answer_job, got %v", names)
	}
}

// TestGetToolsSchema_StillIncludesAskParent guards the other half: the full
// schema (what GetSubagentToolsSchema and GetAllToolsSchema build from)
// must still list ask_parent — a subagent child and a /btw
// side-conversation both need it, since both do get a job id.
func TestGetToolsSchema_StillIncludesAskParent(t *testing.T) {
	names := schemaToolNames(t, GetToolsSchemaJSON())
	if !names["ask_parent"] {
		t.Error("expected the full tool schema to still include ask_parent")
	}
}

func TestGetSubagentToolsSchemaJSONFor_FiltersToAllowed(t *testing.T) {
	got := GetSubagentToolsSchemaJSONFor([]string{"read", "bash"})
	names := schemaToolNames(t, got)
	if !names["read"] || !names["bash"] {
		t.Errorf("the allowed tools are missing: %v", names)
	}
	if names["write"] || names["subagent"] {
		t.Errorf("a tool outside the list got through: %v", names)
	}
	// help and lua are always present — see alwaysAllowedTools for why
	// withholding them cannot make an agent safer, only worse at its job.
	if len(names) != 2+len(alwaysAllowedTools) {
		t.Errorf("expected the two allowed tools plus %v, got %v", alwaysAllowedTools, names)
	}
}

// TestGetSubagentToolsSchemaJSONFor_AlwaysOffersHelpAndLua: a restricted agent
// used to be told "call help()" by the gate's own refusal message while help
// was not in its schema — advice it could not follow.
func TestGetSubagentToolsSchemaJSONFor_AlwaysOffersHelpAndLua(t *testing.T) {
	names := schemaToolNames(t, GetSubagentToolsSchemaJSONFor([]string{"find"}))
	for _, name := range alwaysAllowedTools {
		if !names[name] {
			t.Errorf("%q must be offered to every agent, got %v", name, names)
		}
	}
}

func TestGetSubagentToolsSchemaJSONFor_RejectsExplicitSubagent(t *testing.T) {
	got := GetSubagentToolsSchemaJSONFor([]string{"read", "subagent"})
	names := schemaToolNames(t, got)
	if names["subagent"] {
		t.Error("explicit \"subagent\" in the allowed list must still be dropped — recursion is never allowed")
	}
	if !names["read"] {
		t.Errorf("expected \"read\" to survive the filter, got %v", names)
	}
}

func TestGetSubagentToolsSchemaJSONFor_UnknownNameSkipped(t *testing.T) {
	got := GetSubagentToolsSchemaJSONFor([]string{"read", "not-a-real-tool"})
	names := schemaToolNames(t, got)
	if !names["read"] || names["not-a-real-tool"] {
		t.Errorf("expected {read} plus the always-allowed tools, got %v", names)
	}
	if len(names) != 1+len(alwaysAllowedTools) {
		t.Errorf("an unknown name should be skipped silently, got %v", names)
	}
}

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

func TestParseTasks_TimeoutCompatibility(t *testing.T) {
	for _, in := range []any{
		SubagentMinTimeoutSec,
		SubagentMaxTimeoutSec,
		0,
		SubagentMaxTimeoutSec + 1,
		-1,
		float64(SubagentMinTimeoutSec) + 0.9,
		math.NaN(),
		math.Inf(1),
	} {
		tasks, err := parseTasks(map[string]any{"task": "x", "timeout": in}, "model")
		if f, invalidFloat := in.(float64); invalidFloat && (math.IsNaN(f) || math.IsInf(f, 0)) {
			if err == nil {
				t.Errorf("timeout %v: expected malformed-number error", in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("timeout %v should remain accepted for compatibility: %v", in, err)
		}
		if tasks[0].Timeout == nil {
			t.Fatalf("timeout %v was not retained for compatibility", in)
		}
	}
	if tasks, err := parseTasks(map[string]any{"task": "x"}, "model"); err != nil || tasks[0].Timeout != nil {
		t.Fatalf("omitted timeout should remain nil, got tasks=%+v err=%v", tasks, err)
	}
}

func TestSubagentSchema_TimeoutField(t *testing.T) {
	var timeout map[string]any
	for _, entry := range GetToolsSchema() {
		fn, _ := entry["function"].(map[string]any)
		if fn["name"] != "subagent" {
			continue
		}
		params, _ := fn["parameters"].(map[string]any)
		props, _ := params["properties"].(map[string]any)
		timeout, _ = props["timeout"].(map[string]any)
	}
	if timeout == nil || timeout["type"] != "integer" {
		t.Fatalf("subagent schema missing integer timeout field: %+v", timeout)
	}
	if !strings.Contains(timeout["description"].(string), "ignored") {
		t.Errorf("timeout schema does not explain compatibility behavior: %v", timeout)
	}
}

func TestSubagentRunnerGetsUnlimitedContext(t *testing.T) {
	reg := jobs.NewRegistry()
	SetJobStarter(testJobStarter{reg})
	t.Cleanup(func() { SetJobStarter(nil) })

	var gotCtx context.Context
	var gotOpts SubagentOptions
	runner := &mockRunner{RunTaskFunc: func(ctx context.Context, _, _ string, opts SubagentOptions) (string, error) {
		gotCtx = ctx
		gotOpts = opts
		return "done", nil
	}}
	// Both legacy limit fields are accepted, but neither may constrain the
	// context or runner options used for a real child.
	maxIterations := 1
	res := (&SubagentTool{Runner: runner}).Run(connector.WithModelClient(context.Background(), fakeModelClient("test/model")), map[string]any{
		"task":           "x",
		"max_iterations": maxIterations,
		"timeout":        1,
	})
	if !res.Success {
		t.Fatalf("subagent failed: %s", res.Error)
	}
	if gotCtx == nil {
		t.Fatal("runner was not called")
	}
	if _, ok := gotCtx.Deadline(); ok {
		t.Fatal("subagent runner received an unintended deadline")
	}
	if gotOpts.MaxIterations != nil {
		t.Fatalf("legacy MaxIterations reached runner: %v", gotOpts.MaxIterations)
	}
}

func TestSubagentSpawnGetsUnlimitedContext(t *testing.T) {
	reg := jobs.NewRegistry()
	SetJobStarter(testJobStarter{reg})
	t.Cleanup(func() { SetJobStarter(nil) })

	var gotCtx context.Context
	runner := &mockRunner{RunTaskFunc: func(ctx context.Context, _, _ string, _ SubagentOptions) (string, error) {
		gotCtx = ctx
		return "done", nil
	}}
	tool := &SubagentTool{Runner: runner}
	timeout := 1
	st := tool.spawn(context.Background(), subagentTask{Task: "x", Timeout: &timeout}, true, false)
	<-st.done
	if gotCtx == nil {
		t.Fatal("runner was not called")
	}
	if _, ok := gotCtx.Deadline(); ok {
		t.Fatal("async subagent runner received an unintended deadline")
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

// TestRunSingleTask_IgnoresMaxIter ensures the compatibility input does not
// become an execution cap in the runner options.
func TestRunSingleTask_IgnoresMaxIter(t *testing.T) {
	var captured *int
	runner := &mockRunner{
		RunTaskFunc: func(_ context.Context, _ string, _ string, opts SubagentOptions) (string, error) {
			captured = opts.MaxIterations
			return "ok", nil
		},
	}
	v := 42
	task := subagentTask{Task: "do", MaxIterations: &v}
	res := runSingleTask(context.Background(), runner, task, 0, true)
	if !res.Success || res.Content != "ok" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if captured != nil {
		t.Errorf("runner received legacy MaxIterations=%v, want nil", captured)
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
	res := runSingleTask(context.Background(), runner, subagentTask{Task: "x"}, 0, true)
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
	res := runSingleTask(context.Background(), runner, subagentTask{Task: "x"}, 0, true)
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

// TestRunSingleTask_TimedOutOnDeadline is the ErrSubagentTimedOut analogue
// of TestRunSingleTask_TruncatedOnCapHit above (item 28(C)): when the
// runner returns ErrSubagentTimedOut — the wall-clock deadline counterpart
// to ErrSubagentTruncated, wrapped by main.go's agentRunner.run — the
// result must be Success=true with Truncated=true and the partial content
// intact, exactly like an iteration-cap truncation. Before this fix,
// runSingleTask only special-cased ErrSubagentTruncated and a bare
// context.DeadlineExceeded, both of which discard Content on a
// single-task call (resultsToToolResult drops it whenever Success is
// false) — so a timed-out child with real partial text still lost it.
func TestRunSingleTask_TimedOutOnDeadline(t *testing.T) {
	runner := &mockRunner{
		RunTaskFunc: func(_ context.Context, _, _ string, _ SubagentOptions) (string, error) {
			return "partial before timeout\n\n[note: ...]", fmt.Errorf("%w: result may be incomplete: %w", ErrSubagentTimedOut, context.DeadlineExceeded)
		},
	}
	res := runSingleTask(context.Background(), runner, subagentTask{Task: "x"}, 600, true)
	if !res.Success {
		t.Errorf("expected Success=true on a timed-out but partial result, got false (Error=%q)", res.Error)
	}
	if !res.Truncated {
		t.Errorf("expected Truncated=true, got false")
	}
	if res.Content != "partial before timeout\n\n[note: ...]" {
		t.Errorf("unexpected Content: %q", res.Content)
	}

	// resultsToToolResult must carry that content through to the model —
	// the whole point of surfacing it as a partial success rather than a
	// bare failure.
	tr := resultsToToolResult([]subagentResult{res})
	if !tr.Success || !tr.Truncated || tr.Content != res.Content {
		t.Errorf("resultsToToolResult dropped the timed-out child's content: %+v", tr)
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

// ─── per-agent todo isolation (item 24) ──────────────────────────────────

// TestRunSingleTask_ChildGetsOwnTodoList reverts to: runSingleTask never
// stamps TodoAgentCtxKey on the child's context, so the child's todo tool
// calls land in the parent's list (tools/todo.go's per-agent todoStore).
func TestRunSingleTask_ChildGetsOwnTodoList(t *testing.T) {
	resetTodoStoreForTest()
	t.Cleanup(resetTodoStoreForTest)

	RunTool(context.Background(), "todo", map[string]any{"action": "add", "content": "parent plan"})

	runner := &mockRunner{RunTaskFunc: func(ctx context.Context, task string, model string, opts SubagentOptions) (string, error) {
		res := (&TodoTool{}).Run(ctx, map[string]any{"action": "add", "content": "child plan"})
		if !res.Success {
			t.Errorf("child add failed: %s", res.Error)
		}
		return "done", nil
	}}

	result := runSingleTask(ctxWithParentModel("test-model"), runner, subagentTask{Task: "child task"}, 0, false)
	if !result.Success {
		t.Fatalf("runSingleTask failed: %s", result.Error)
	}

	items := AllTodoItems()
	if len(items) != 1 || items[0].Content != "parent plan" {
		t.Fatalf("parent's list was contaminated by the child: %+v", items)
	}
}

// TestRunTasks_SiblingChildrenGetDistinctTodoLists reverts to the same bug,
// but for two children spawned from one "subagent" call (runTasks) —
// guarding against nextTodoAgentID handing out the same id under
// concurrency, which would silently merge two siblings' lists.
func TestRunTasks_SiblingChildrenGetDistinctTodoLists(t *testing.T) {
	resetTodoStoreForTest()
	t.Cleanup(resetTodoStoreForTest)

	var mu sync.Mutex
	seen := map[string]string{}

	runner := &mockRunner{RunTaskFunc: func(ctx context.Context, task string, model string, opts SubagentOptions) (string, error) {
		(&TodoTool{}).Run(ctx, map[string]any{"action": "add", "content": "plan for " + task})
		res := (&TodoTool{}).Run(ctx, map[string]any{"action": "list"})
		mu.Lock()
		seen[task] = res.Content
		mu.Unlock()
		return "ok", nil
	}}

	tasks := []subagentTask{{Task: "alpha"}, {Task: "beta"}}
	runTasks(ctxWithParentModel("test-model"), runner, tasks)

	mu.Lock()
	alpha, beta := seen["alpha"], seen["beta"]
	mu.Unlock()

	if !strings.Contains(alpha, "plan for alpha") || strings.Contains(alpha, "plan for beta") {
		t.Fatalf("alpha's list is wrong or contaminated: %q", alpha)
	}
	if !strings.Contains(beta, "plan for beta") || strings.Contains(beta, "plan for alpha") {
		t.Fatalf("beta's list is wrong or contaminated: %q", beta)
	}

	// And neither sibling's scratch work leaked into the parent's list.
	if items := AllTodoItems(); len(items) != 0 {
		t.Fatalf("parent's list should be untouched by either sibling: %+v", items)
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
		unregisterTool("subagent")
		subagentToolInstance = nil
	})
	SetSubAgentRunner(&mockRunner{
		RunTaskFunc: func(_ context.Context, _, _ string, _ SubagentOptions) (string, error) {
			return "partial answer\n\n[note: ...]", fmt.Errorf("hit cap: %w", ErrSubagentTruncated)
		},
	})

	ctx := connector.WithModelClient(context.Background(), fakeModelClient("test/model"))
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
		unregisterTool("subagent")
		subagentToolInstance = nil
	})
	SetSubAgentRunner(&mockRunner{
		RunTaskFunc: func(_ context.Context, _, _ string, _ SubagentOptions) (string, error) {
			return "complete answer", nil
		},
	})

	ctx := connector.WithModelClient(context.Background(), fakeModelClient("test/model"))
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

// testJobHandle/testJobStarter/testJobWaiter mirror the adapters main()
// wires in production (over the app's shared jobs.Registry) so these tests
// exercise SetJobStarter/SetJobWaiter's actual contract instead of a
// tools-internal registry the package no longer owns (see SetJobStarter's
// doc comment: main is the only layer allowed to import both tools and
// jobs). Importing "jobs" here is fine — it's the same leaf-importing-leaf
// relationship production code in main has, just from a _test.go file.
type testJobHandle struct{ id string }

func (h testJobHandle) ID() string { return h.id }

type testJobStarter struct{ reg *jobs.Registry }

func (s testJobStarter) Start(ctx context.Context, description, kind, parentID string, fn func(context.Context, string) (string, bool, error)) JobHandle {
	return testJobHandle{s.reg.Start(ctx, description, jobs.Kind(kind), parentID, fn).ID}
}

type testJobWaiter struct{ reg *jobs.Registry }

func (w testJobWaiter) Wait(ctx context.Context, id string, timeout time.Duration) (JobStatus, bool) {
	job, ok := w.reg.Wait(ctx, id, timeout)
	if !ok {
		return JobStatus{}, false
	}
	return JobStatus{
		ID:      job.ID,
		Done:    job.Status != jobs.StatusRunning,
		Success: job.Status == jobs.StatusDone || job.Status == jobs.StatusTruncated,
		Content: job.Result,
		Error:   job.Err,
	}, true
}

// TestSubagentAsync_ReturnsJobIDImmediately verifies the tool call itself
// never blocks on the (slow) task when async=true, and that the job later
// completes with the task's result reachable through the injected registry.
func TestSubagentAsync_ReturnsJobIDImmediately(t *testing.T) {
	t.Cleanup(func() {
		unregisterTool("subagent")
		subagentToolInstance = nil
		SetJobStarter(nil)
	})

	reg := jobs.NewRegistry()
	notifier := jobs.NewNotifier()
	SetJobStarter(testJobStarter{reg})
	SetJobNotifier(notifier)
	t.Cleanup(func() { SetJobNotifier(nil) })

	release := make(chan struct{})
	SetSubAgentRunner(&mockRunner{
		RunTaskFunc: func(_ context.Context, _, _ string, _ SubagentOptions) (string, error) {
			<-release
			return "async result", nil
		},
	})

	ctx := connector.WithModelClient(context.Background(), fakeModelClient("test/model"))
	res := RunTool(ctx, "subagent", map[string]any{"task": "slow thing", "async": true})
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}

	var spawned []struct {
		Task  string `json:"task"`
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(spawnedJobsJSON(res.Content)), &spawned); err != nil {
		t.Fatalf("unmarshal spawned jobs: %v (content: %q)", err, res.Content)
	}
	if len(spawned) != 1 || spawned[0].JobID == "" {
		t.Fatalf("expected one spawned job with a job_id, got %+v", spawned)
	}

	if _, ok := reg.Get(spawned[0].JobID); !ok {
		t.Fatalf("job %q not found in registry right after spawn — should be running, not absent", spawned[0].JobID)
	}

	close(release)

	status, ok := testJobWaiter{reg}.Wait(context.Background(), spawned[0].JobID, 2*time.Second)
	if !ok {
		t.Fatalf("wait: unknown job_id %q", spawned[0].JobID)
	}
	if !status.Done || !status.Success || status.Content != "async result" {
		t.Errorf("unexpected final status: %+v", status)
	}

	select {
	case <-notifier.Signal():
	default:
		t.Fatal("async subagent completion did not signal the jobs.Notifier")
	}
	notices := notifier.Drain()
	if len(notices) != 1 || !strings.Contains(notices[0], "finished") || !strings.Contains(notices[0], spawned[0].JobID) {
		t.Fatalf("unexpected async subagent completion notices: %v", notices)
	}
}

// TestSubagentAsync_NoJobStarterConfigured ensures async=true fails loudly
// (not a panic, not a silent fallback to sync) when main() hasn't wired
// SetJobStarter yet.
func TestSubagentAsync_NoJobStarterConfigured(t *testing.T) {
	t.Cleanup(func() {
		unregisterTool("subagent")
		subagentToolInstance = nil
		SetJobStarter(nil)
	})
	SetJobStarter(nil)
	SetSubAgentRunner(&mockRunner{})

	ctx := connector.WithModelClient(context.Background(), fakeModelClient("test/model"))
	res := RunTool(ctx, "subagent", map[string]any{"task": "x", "async": true})
	if res.Success {
		t.Fatalf("expected failure with no JobStarter configured, got success: %+v", res)
	}
	if !strings.Contains(res.Error, "not configured") {
		t.Errorf("expected a clear 'not configured' error, got: %q", res.Error)
	}
}

// TestSubagentAsync_MixedBatchRejected ensures a batch that mixes async and
// non-async tasks fails fast with a clear error instead of silently picking
// one interpretation.
func TestSubagentAsync_MixedBatchRejected(t *testing.T) {
	t.Cleanup(func() {
		unregisterTool("subagent")
		subagentToolInstance = nil
	})
	SetSubAgentRunner(&mockRunner{})

	ctx := connector.WithModelClient(context.Background(), fakeModelClient("test/model"))
	res := RunTool(ctx, "subagent", map[string]any{"tasks": []any{
		map[string]any{"task": "a", "async": true},
		map[string]any{"task": "b", "async": false},
	}})
	if res.Success {
		t.Fatalf("expected mixed async/non-async batch to be rejected, got success: %+v", res)
	}
	if !strings.Contains(res.Error, "mix async") {
		t.Errorf("expected error to mention mixing async tasks, got: %q", res.Error)
	}
}

// TestSubagentAsync_DoesNotStreamToParentToolIdx is the regression test for
// the bug runSingleTask's streamToParent parameter fixes: by the time an
// async job's RunTask actually streams text, the parent's "subagent" tool
// call has already returned and the TUI has closed toolIdx 9's block — so
// nothing should be forwarded to the parent's stream.Output at all.
func TestSubagentAsync_DoesNotStreamToParentToolIdx(t *testing.T) {
	t.Cleanup(func() {
		unregisterTool("subagent")
		subagentToolInstance = nil
		SetJobStarter(nil)
	})
	reg := jobs.NewRegistry()
	SetJobStarter(testJobStarter{reg})

	mo := &mockOutput{}
	release := make(chan struct{})
	SetSubAgentRunner(&mockRunner{
		RunTaskFunc: func(ctx context.Context, _, _ string, _ SubagentOptions) (string, error) {
			// Mirrors main.go's agentRunner: drive the injected Sink directly.
			if sink, ok := ctx.Value(SubagentSinkCtxKey{}).(SubagentSink); ok {
				sink.Text("leaked line\n")
			}
			<-release
			return "async result", nil
		},
	})

	ctx := connector.WithModelClient(context.Background(), fakeModelClient("test/model"))
	ctx = stream.WithOutput(ctx, mo.call)
	ctx = context.WithValue(ctx, stream.ToolIdxCtxKey{}, 9)

	res := RunTool(ctx, "subagent", map[string]any{"task": "slow thing", "async": true})
	if !res.Success {
		t.Fatalf("expected success, got error: %q", res.Error)
	}

	var spawned []struct {
		Task  string `json:"task"`
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(spawnedJobsJSON(res.Content)), &spawned); err != nil {
		t.Fatalf("unmarshal spawned jobs: %v (content: %q)", err, res.Content)
	}

	close(release)
	status, ok := testJobWaiter{reg}.Wait(context.Background(), spawned[0].JobID, 2*time.Second)
	if !ok || !status.Done {
		t.Fatalf("job did not finish: ok=%v status=%+v", ok, status)
	}

	if lines := mo.lines(); len(lines) != 0 {
		t.Errorf("expected no lines forwarded to the parent's stream.Output, got %q", lines)
	}
}

// spawnedJobsJSON extracts the machine-readable prefix of an async subagent
// result. The rest of the content is instructions for the model: what the
// completion and blocked-on-a-question notices mean, and that an unanswered
// child eventually discards its work.
func spawnedJobsJSON(content string) string {
	return strings.SplitN(content, "\n", 2)[0]
}

// TestSubagentAsync_ResultTellsTheParentHowToRespond guards that text. A bare
// list of ids leaves the parent with no reason to suspect a child can block on
// a question, and an unanswered child burns its wall-clock limit and throws
// everything away.
func TestSubagentAsync_ResultExplainsTheChannels(t *testing.T) {
	t.Cleanup(func() {
		unregisterTool("subagent")
		subagentToolInstance = nil
		SetJobStarter(nil)
	})
	SetJobStarter(testJobStarter{jobs.NewRegistry()})
	SetSubAgentRunner(&mockRunner{
		RunTaskFunc: func(_ context.Context, _, _ string, _ SubagentOptions) (string, error) {
			return "done", nil
		},
	})

	ctx := connector.WithModelClient(context.Background(), fakeModelClient("test/model"))
	res := RunTool(ctx, "subagent", map[string]any{"task": "some work", "async": true})
	if !res.Success {
		t.Fatalf("expected success, got %q", res.Error)
	}
	for _, want := range []string{"answer_job(job_id", "wait(job_id", "discarded"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("async result does not mention %q:\n%s", want, res.Content)
		}
	}
	// Item 29: subagent.go's handoff/async result text used to tell the
	// parent to answer a blocked question unconditionally. It must now say
	// to relay it unless the parent already knows.
	if !strings.Contains(res.Content, "relay it") {
		t.Errorf("async result should tell the parent to relay a blocked question, not answer it unconditionally:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "never invent one standing in for a human who hasn't replied") {
		t.Errorf("async result should say never to invent an answer for a human who hasn't replied:\n%s", res.Content)
	}
}

func TestSubagentCompletionNoticeIncludesPreviewAndWaitInstruction(t *testing.T) {
	got := subagentCompletionNotice("worker", "job-1", subagentResult{Success: true, Content: "implemented the fix"})
	for _, want := range []string{"worker", "job-1", "implemented the fix", "finished", "Tell the user", "wait(job_id=\"job-1\")"} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice %q missing %q", got, want)
		}
	}
	long := strings.Repeat("x", subagentNoticePreviewLimit+100)
	got = subagentCompletionNotice("worker", "job-1", subagentResult{Success: true, Content: long})
	if len([]rune(got)) > 1400 {
		t.Fatalf("notice unexpectedly long: %d runes", len([]rune(got)))
	}
	if !strings.Contains(got, "…") {
		t.Fatal("long preview was not truncated")
	}
}
