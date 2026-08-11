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

// SubagentTimeoutSec is a per-subagent wall-clock backstop. The iteration cap
// bounds model turns, but a single wedged tool call (e.g. a hung shell command)
// could otherwise block the parent's tool call forever; this ensures the parent
// always gets an answer.
const SubagentTimeoutSec = 600

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

// JobHandle is the minimal handle Start returns — just enough for runAsync
// to report the assigned id back to the model.
type JobHandle interface{ ID() string }

// JobStarter is the async-spawn counterpart to tools.JobWaiter (see
// wait.go): a local, minimal contract so this package never imports "jobs"
// (same import-cycle rationale as JobWaiter's doc comment). main() supplies
// an adapter over the app's shared jobs.Registry via SetJobStarter.
type JobStarter interface {
	Start(ctx context.Context, description string, fn func(ctx context.Context, jobID string) (result string, truncated bool, err error)) JobHandle
}

// JobIDCtxKey is the context key under which an async job's own ID is
// stashed (see runAsync) so tools invoked from inside that job — "ask" and
// "report_progress" — can find out "which job am I running inside" without
// this package importing "jobs" (same layering rule as JobWaiter/JobStarter
// above). /btw side-conversations set the same key (see btw.go's startBtw)
// so ask/report_progress work there too, for free.
type JobIDCtxKey struct{}

// jobStarter is nil until SetJobStarter is called; runAsync fails loudly
// (not silently blocking or panicking) until then.
var jobStarter JobStarter

// SetJobStarter wires the "subagent" tool's async=true spawn path to a
// JobStarter. Called once from main() with an adapter over the app's shared
// jobs.Registry — the same registry /btw side-conversations and the "wait"
// tool's job_id polling (SetJobWaiter) run on, so a job started here is
// pollable from anywhere.
func SetJobStarter(s JobStarter) { jobStarter = s }

// subagentTask represents a single task for a subagent.
type subagentTask struct {
	Task          string `json:"task"`
	Agent         string `json:"agent,omitempty"`
	Model         string `json:"model,omitempty"`
	MaxIterations *int   `json:"max_iterations,omitempty"`
	// Async, when true, makes the tool register a background job and return
	// its id immediately instead of blocking until the task finishes. See
	// SubagentTool.Run and runAsync.
	Async bool `json:"async,omitempty"`
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

// SubagentSink is the Sink-shaped callback set the runner (main.go's
// agentRunner) must drive when executing a child agent, so the child's
// Text/Thinking calls actually reach *streamingCollector's forwarding logic
// and stream live to the parent TUI's subagent modal instead of only
// appearing once the whole task finishes. It is passed to the runner via
// context (SubagentSinkCtxKey), not a RunTask parameter, and the runner
// asserts it to agent.Sink itself — defined structurally here (matching
// agent.Sink's method set) so this package never has to import agent (see
// ErrSubagentTruncated's comment on layering).
type SubagentSink interface {
	Request(content string)
	Thinking(text string)
	Text(text string)
	ToolCallStart(name string)
	ToolCallDelta(delta string)
	ToolCallEnd(name string, result string)
	ToolFinish()
	ToolBlock(msg string)
	Summary(usage stream.Usage, stats stream.Stats)
	Total(usage stream.Usage)
	Error(err error)
	End()
	// CollectedText returns the text accumulated so far via Text calls, so
	// the runner can read back the final answer after agent.Run completes.
	CollectedText() string
}

// SubagentSinkCtxKey is the context key under which runSingleTask stores the
// SubagentSink for the runner to pick up and drive directly, instead of
// building its own non-forwarding Sink.
type SubagentSinkCtxKey struct{}

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

// CollectedText returns the text accumulated so far via Text calls,
// satisfying SubagentSink so the runner can read back the final answer once
// this collector — rather than a separate one — is the actual Sink that
// drove agent.Run.
func (s *streamingCollector) CollectedText() string {
	s.collector.mu.Lock()
	defer s.collector.mu.Unlock()
	return s.collector.text.String()
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

	if async, mixed := asyncTaskMode(tasks); mixed {
		return ToolResult{Type: "result", Success: false, Error: "cannot mix async and non-async tasks in the same call; issue separate subagent calls"}
	} else if async {
		return t.runAsync(ctx, tasks)
	}

	// Run tasks concurrently, each bounded by SubagentTimeoutSec.
	results := runTasks(ctx, t.Runner, tasks, SubagentTimeoutSec)

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
	if a, ok := input["async"].(bool); ok {
		t.Async = a
	}
	return []subagentTask{t}, nil
}

// asyncTaskMode reports whether the batch should run async: async is true
// only when every task requested it, mixed is true when some but not all
// did (an ambiguous request the caller must reject rather than guess at).
func asyncTaskMode(tasks []subagentTask) (async bool, mixed bool) {
	n := 0
	for _, t := range tasks {
		if t.Async {
			n++
		}
	}
	if n == 0 {
		return false, false
	}
	if n == len(tasks) {
		return true, false
	}
	return false, true
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
	if a, ok := m["async"].(bool); ok {
		t.Async = a
	}
	return t, nil
}

// runAsync registers each task as a background job via jobStarter and
// returns immediately with the assigned job ids, instead of blocking until
// the tasks finish (runTasks' path). The job's context is detached from ctx
// (context.WithoutCancel) because ctx dies with this tool call's turn, but
// the job must keep running after Run returns; a fresh wall-clock backstop
// (SubagentTimeoutSec, same as the sync path) replaces the cancellation the
// job no longer inherits. Poll results via the "wait" tool's job_id mode.
func (t *SubagentTool) runAsync(ctx context.Context, tasks []subagentTask) ToolResult {
	if jobStarter == nil {
		return ToolResult{Type: "result", Success: false, Error: "async subagent spawn unavailable: job registry not configured"}
	}

	type spawned struct {
		Task  string `json:"task"`
		JobID string `json:"job_id"`
	}
	out := make([]spawned, 0, len(tasks))
	for _, task := range tasks {
		task := task
		jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SubagentTimeoutSec*time.Second)
		job := jobStarter.Start(jobCtx, task.Task, func(runCtx context.Context, jobID string) (string, bool, error) {
			defer cancel()
			runCtx = context.WithValue(runCtx, JobIDCtxKey{}, jobID)
			res := runSingleTask(runCtx, t.Runner, task, 0, false)
			if !res.Success {
				return res.Content, res.Truncated, errors.New(res.Error)
			}
			return res.Content, res.Truncated, nil
		})
		out = append(out, spawned{Task: task.Task, JobID: job.ID()})
	}

	data, err := json.Marshal(out)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("marshal spawned jobs: %v", err)}
	}
	return ToolResult{Type: "result", Success: true, Content: string(data)}
}

func runTasks(ctx context.Context, runner SubAgentRunner, tasks []subagentTask, timeoutSec int) []subagentResult {
	results := make([]subagentResult, len(tasks))
	var wg sync.WaitGroup

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t subagentTask) {
			defer wg.Done()
			results[idx] = runSingleTask(ctx, runner, t, timeoutSec, true)
		}(i, task)
	}

	wg.Wait()
	return results
}

// streamToParent controls whether the child's output is forwarded live to
// the parent TUI's tool block (via stream.ToolIdxCtxKey / stream.Output).
// It must be false for a job spawned by runAsync: by the time the job
// finishes, the parent's "subagent" tool call has already returned and the
// TUI has closed that tool block, so toolIdx no longer refers to anything
// live — forwarding into it would write to a stale or reused block instead
// of just doing nothing. Job.Result (collected by the plain, non-streaming
// collector below) remains the source of truth for async output, retrieved
// later via the "wait" tool.
func runSingleTask(ctx context.Context, runner SubAgentRunner, task subagentTask, timeoutSec int, streamToParent bool) subagentResult {
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

	// Get tool index for streaming (passed by agent.executeTools). Only
	// resolved when streamToParent, so an async job never picks up the
	// parent's (soon to be closed) toolIdx or its stream.Output callback —
	// see runSingleTask's doc comment.
	var c *streamingCollector
	if streamToParent {
		toolIdx := 0
		if idx, ok := ctx.Value(stream.ToolIdxCtxKey{}).(int); ok {
			toolIdx = idx
		}
		c = newStreamingCollector(ctx, toolIdx)
	} else {
		c = &streamingCollector{collector: newCollector()}
	}

	// Hand the runner our forwarding Sink via context (see SubagentSink's
	// doc comment) so it drives agent.Run with this collector directly,
	// instead of building its own non-forwarding one — that was the actual
	// bug behind the subagent modal staying blank while running: c existed
	// but nothing ever called its Text/Thinking.
	runCtx = context.WithValue(runCtx, SubagentSinkCtxKey{}, c)

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
