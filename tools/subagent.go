package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/internal/agentdefs"
	"github.com/decodo/tyci/stream"
)

// subagentTimeoutSec is a per-subagent wall-clock backstop. The iteration cap
// bounds model turns, but a single wedged tool call (e.g. a hung shell command)
// could otherwise block the parent's tool call forever; this ensures the parent
// always gets an answer.
const subagentTimeoutSec = 600

// ErrSubagentTruncated is returned (wrapped via fmt.Errorf %w) by a
// SubAgentRunner when the child hit its MaxIterations cap. Tools package
// callers use errors.Is to detect this — distinct from "child failed" or
// "child returned empty result" — and surface it via subagentResult.Truncated
// and ToolResult.Truncated. Sentinel lives in tools/ (not agent/) so the
// layering remains: tools has no upward dependency on agent.
var ErrSubagentTruncated = errors.New("subagent hit its max-iterations cap")

type SubagentTool struct {
	Runner SubAgentRunner
}

func (t *SubagentTool) Name() string { return "subagent" }

// subagentTask represents a single task for a subagent.
type subagentTask struct {
	Task          string `json:"task"`
	Agent         string `json:"agent,omitempty"`
	Model         string `json:"model,omitempty"`
	MaxIterations *int   `json:"max_iterations,omitempty"`
}

// subagentResult holds the outcome of one subagent execution.
type subagentResult struct {
	Task      string       `json:"task"`
	Success   bool         `json:"success"`
	Content   string       `json:"content,omitempty"`
	Thinking  string       `json:"thinking,omitempty"`
	Error     string       `json:"error,omitempty"`
	ToolCalls int          `json:"tool_calls"`
	Usage     stream.Usage `json:"usage"`
	Model     string       `json:"model,omitempty"`
	// Truncated is true when the child ran to its MaxIterations cap and
	// self-stopped with a partial answer (still a "success" but flagged so
	// the parent distinguishes from a clean completion).
	Truncated bool `json:"truncated,omitempty"`
}

// collector implements display.Display and captures all output (thread-safe).
type collector struct {
	mu        sync.Mutex
	thinking  strings.Builder
	text      strings.Builder
	toolCalls int
	usage     stream.Usage
	err       error
}

func newCollector() *collector { return &collector{} }

func (c *collector) Request(content string) {}
func (c *collector) Thinking(text string) {
	c.mu.Lock()
	c.thinking.WriteString(text)
	c.mu.Unlock()
}
func (c *collector) Text(text string) {
	c.mu.Lock()
	c.text.WriteString(text)
	c.mu.Unlock()
}
func (c *collector) ToolCallStart(name string) {
	c.mu.Lock()
	c.toolCalls++
	c.mu.Unlock()
}
func (c *collector) ToolCallDelta(delta string)      {}
func (c *collector) ToolCallEnd(name, result string) {}
func (c *collector) ToolFinish()                     {}
func (c *collector) ToolBlock(msg string)            {}
func (c *collector) Summary(usage stream.Usage, stats stream.Stats) {
	c.mu.Lock()
	c.usage = usage
	c.mu.Unlock()
}
func (c *collector) Total(usage stream.Usage) {}
func (c *collector) Error(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}
func (c *collector) End() {}

func (c *collector) Result() subagentResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return subagentResult{
		Content:   c.text.String(),
		Thinking:  c.thinking.String(),
		ToolCalls: c.toolCalls,
		Usage:     c.usage,
	}
}

// streamingCollector wraps collector and pushes output to stream.OnOutput
// for live TUI display in the subagent modal.
type streamingCollector struct {
	*collector
	toolIdx int
	mu      sync.Mutex
	lineBuf strings.Builder // buffer for partial lines

	// parentOutput saves the stream.OnOutput callback from the parent's
	// runOnce so we can forward output to the TUI even after the subagent's
	// own runOnce overwrites the global stream.OnOutput.
	parentOutput func(toolIdx int, line string)
}

func newStreamingCollector(ctx context.Context, toolIdx int) *streamingCollector {
	// Capture the parent's streaming callback from context before the
	// subagent's inner runOnce installs its own.
	parentOutput := stream.Output(ctx)
	return &streamingCollector{
		collector:    newCollector(),
		toolIdx:      toolIdx,
		parentOutput: parentOutput,
	}
}

func (s *streamingCollector) Text(text string) {
	s.collector.Text(text)
	s.pushText(text)
}

func (s *streamingCollector) Thinking(text string) {
	s.collector.Thinking(text)
	s.pushText(text)
}

// pushText buffers text and pushes complete lines to the parent's streaming
// callback (saved at creation time). We use s.parentOutput instead of the
// global stream.OnOutput because subagent's own runOnce overwrites the global.
func (s *streamingCollector) pushText(text string) {
	if s.parentOutput == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lineBuf.WriteString(text)
	content := s.lineBuf.String()

	for {
		idx := strings.IndexByte(content, '\n')
		if idx < 0 {
			break
		}
		line := content[:idx]
		s.parentOutput(s.toolIdx, line)
		content = content[idx+1:]
	}
	s.lineBuf.Reset()
	s.lineBuf.WriteString(content)
}

// flushPartial flushes any remaining partial line when subagent finishes.
func (s *streamingCollector) flushPartial() {
	if s.parentOutput == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lineBuf.Len() > 0 {
		s.parentOutput(s.toolIdx, s.lineBuf.String())
		s.lineBuf.Reset()
	}
}

// StreamProgress implements the streamer interface so that the subagent's
// inner runOnce can forward tool output (e.g. bash lines) to the parent TUI.
// toolIdx here is the index within the subagent's own tool batch; we ignore it
// and always forward using the subagent's own toolIdx (the parent's perspective).
func (s *streamingCollector) StreamProgress(_ int, line string) {
	if s.parentOutput == nil {
		return
	}
	s.parentOutput(s.toolIdx, line)
}

func (t *SubagentTool) Run(ctx context.Context, input map[string]any) ToolResult {
	if t.Runner == nil {
		return ToolResult{Type: "result", Success: false, Error: "subagent runner not configured"}
	}

	var defaultModel string
	if mc := connector.ModelClientFromContext(ctx); mc != nil {
		defaultModel = mc.Model()
	}
	if defaultModel == "" {
		return ToolResult{Type: "result", Success: false, Error: "no model specified and no default model set"}
	}

	// Parse tasks
	tasks, err := parseTasks(input, defaultModel)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	if len(tasks) == 0 {
		return ToolResult{Type: "result", Success: false, Error: "no tasks provided"}
	}

	// Run tasks concurrently, each bounded by subagentTimeoutSec.
	results := runTasks(ctx, t.Runner, tasks, subagentTimeoutSec)

	// Single task → return plain text (backward compatible)
	if len(results) == 1 {
		r := results[0]
		if !r.Success {
			return ToolResult{Type: "result", Success: false, Error: r.Error}
		}
		return ToolResult{Type: "result", Success: true, Content: r.Content, Truncated: r.Truncated}
	}

	// Multiple tasks → return JSON array
	data, err := json.Marshal(results)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("marshal results: %v", err)}
	}
	return ToolResult{Type: "result", Success: true, Content: string(data)}
}

// parseTasks extracts tasks from input.
// Accepts:
//
//	{ "task": "..." }                                    — single task, current model
//	{ "task": "...", "model": "..." }                     — single task with model override
//	{ "tasks": [{ "task": "...", "model": "..." }, ...] } — parallel tasks
func parseTasks(input map[string]any, defaultModel string) ([]subagentTask, error) {
	// Try "tasks" array first
	if tasksRaw, ok := input["tasks"]; ok {
		arr, ok := tasksRaw.([]any)
		if !ok {
			return nil, fmt.Errorf("tasks must be an array")
		}
		if len(arr) == 0 {
			return nil, fmt.Errorf("tasks array is empty")
		}
		var tasks []subagentTask
		for i, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("tasks[%d] must be an object", i)
			}
			t, err := taskFromMap(m)
			if err != nil {
				return nil, fmt.Errorf("tasks[%d]: %w", i, err)
			}
			tasks = append(tasks, t)
		}
		return tasks, nil
	}

	// Single task from "task" field
	taskStr, ok := input["task"].(string)
	if !ok || taskStr == "" {
		return nil, fmt.Errorf("task or tasks required")
	}

	t := subagentTask{Task: taskStr}
	if agent, ok := input["agent"].(string); ok && agent != "" {
		t.Agent = agent
	}
	if m, ok := input["model"].(string); ok && m != "" {
		t.Model = m
	}
	if mi, ok := input["max_iterations"]; ok {
		v, err := toInt(mi)
		if err != nil {
			return nil, fmt.Errorf("max_iterations: %w", err)
		}
		t.MaxIterations = &v
	}
	return []subagentTask{t}, nil
}

// toInt accepts any JSON number preserved by encoding/json (float64) or a
// plain int (from tests / typed callers). Rejects NaN/Inf and values outside
// the int range so a runaway model value (e.g. max_iterations: 1e20) doesn't
// silently saturate to math.MaxInt64 and let the child run forever.
func toInt(v any) (int, error) {
	switch x := v.(type) {
	case int:
		return x, nil
	case int64:
		return int(x), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, fmt.Errorf("expected integer, got %v", x)
		}
		if x > float64(math.MaxInt) || x < float64(math.MinInt) {
			return 0, fmt.Errorf("value out of int range: %v", x)
		}
		return int(x), nil
	case float32:
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return 0, fmt.Errorf("expected integer, got %v", x)
		}
		f := float64(x)
		if f > float64(math.MaxInt) || f < float64(math.MinInt) {
			return 0, fmt.Errorf("value out of int range: %v", x)
		}
		return int(x), nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", v)
	}
}

func taskFromMap(m map[string]any) (subagentTask, error) {
	taskStr, _ := m["task"].(string)
	if taskStr == "" {
		return subagentTask{}, fmt.Errorf("task is required")
	}
	t := subagentTask{Task: taskStr}
	if agent, ok := m["agent"].(string); ok && agent != "" {
		t.Agent = agent
	}
	if model, ok := m["model"].(string); ok && model != "" {
		t.Model = model
	}
	if mi, ok := m["max_iterations"]; ok {
		v, err := toInt(mi)
		if err != nil {
			return subagentTask{}, fmt.Errorf("max_iterations: %w", err)
		}
		t.MaxIterations = &v
	}
	return t, nil
}

func runTasks(ctx context.Context, runner SubAgentRunner, tasks []subagentTask, timeoutSec int) []subagentResult {
	results := make([]subagentResult, len(tasks))
	var wg sync.WaitGroup

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t subagentTask) {
			defer wg.Done()
			results[idx] = runSingleTask(ctx, runner, t, timeoutSec)
		}(i, task)
	}

	wg.Wait()
	return results
}

func runSingleTask(ctx context.Context, runner SubAgentRunner, task subagentTask, timeoutSec int) subagentResult {
	// Resolve the named agent definition, if any, before anything else. An
	// unknown agent name used to silently degrade to a plain subagent with
	// the parent's defaults — indistinguishable from success and a trap for
	// a typo'd name. Fail loudly instead.
	var def agentdefs.Def
	if task.Agent != "" {
		var ok bool
		def, ok = agentdefs.Get("", task.Agent)
		if !ok {
			return subagentResult{
				Task:    task.Task,
				Success: false,
				Error:   fmt.Sprintf("agent %q not found (looked in ~/.tyci/agents and ./.tyci/agents)", task.Agent),
			}
		}
	}

	runCtx := ctx
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
	}

	// The child's isolated HTTP connection pool is created by the runner (see
	// agentRunner.run in main.go), which binds it to the provider it resolves.
	// It used to be stuffed into runCtx here; the transport is no longer
	// something this package has to know about.

	// Model precedence: explicit per-task override wins, then the named
	// agent's frontmatter `model`, then the parent's model inherited via
	// context. Before this, def.Model was read nowhere — a named agent
	// declared as a cheap model still burned the parent's (often expensive)
	// model on every call.
	mName := task.Model
	if mName == "" {
		mName = def.Model
	}
	if mName == "" {
		if mc := connector.ModelClientFromContext(ctx); mc != nil {
			mName = mc.Model()
		}
	}

	// max_iterations precedence: explicit per-task override wins, then the
	// named agent's frontmatter `max_iterations` (only when positive — 0/unset
	// means "the definition didn't say"), then nil so ResolveMaxIter applies
	// its own default downstream.
	maxIter := task.MaxIterations
	if maxIter == nil && def.MaxIterations > 0 {
		v := def.MaxIterations
		maxIter = &v
	}

	// Temperature and Fallback are read straight off the named agent's
	// definition — unlike Model/MaxIterations there is no per-task override
	// for either (the subagent tool schema deliberately does not expose
	// them; see tools.go's "subagent" schema comment), so def is the only
	// source. When task.Agent is empty, def is the zero value: Temperature
	// is nil and Fallback is nil, i.e. nothing changes for a plain subagent.
	opts := SubagentOptions{
		MaxIterations:    maxIter,
		Tools:            def.Tools,
		Temperature:      def.Temperature,
		Fallbacks:        def.Fallback,
		SystemPromptMode: def.SystemPromptMode,
	}

	// Get tool index for streaming (passed by agent.executeTools)
	toolIdx := 0
	if idx, ok := ctx.Value(stream.ToolIdxCtxKey{}).(int); ok {
		toolIdx = idx
	}

	c := newStreamingCollector(ctx, toolIdx)

	// Run the task via the runner interface. def.SystemPrompt is empty when
	// no agent was named (def is the zero value), so this also covers the
	// plain-subagent case without a separate branch on task.Agent.
	var content string
	var err error
	if def.SystemPrompt != "" {
		content, err = runner.RunTaskWithSystem(runCtx, task.Task, mName, def.SystemPrompt, opts)
	} else {
		content, err = runner.RunTask(runCtx, task.Task, mName, opts)
	}

	// Flush any remaining partial line
	c.flushPartial()

	res := c.Result()
	res.Task = task.Task
	res.Model = mName
	res.Content = content

	if err != nil {
		res.Success = false
		// A hit timeout surfaces as a bare "context deadline exceeded"; make it
		// actionable so the parent understands the child was cut off, not that
		// its task was malformed.
		if timeoutSec > 0 && errors.Is(err, context.DeadlineExceeded) {
			res.Error = fmt.Sprintf("subagent exceeded its %ds time limit and was stopped; narrow the task or split it", timeoutSec)
		} else if errors.Is(err, ErrSubagentTruncated) {
			// Hit the iteration cap but produced text: surface as a partial
			// success, not an error. The content already carries the
			// [note: ...] context from the runner.
			res.Success = true
			res.Truncated = true
		} else {
			res.Error = err.Error()
		}
	} else {
		res.Success = true
	}

	return res
}
