package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/decodo/tyci-agent/agent"
	"github.com/decodo/tyci-agent/api"
	"github.com/decodo/tyci-agent/providers"
	"github.com/decodo/tyci-agent/stream"
)

type SubagentTool struct{}

func (t *SubagentTool) Name() string { return "subagent" }

// subagentTask represents a single task for a subagent.
type subagentTask struct {
	Task        string   `json:"task"`
	Model       string   `json:"model,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
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
func (c *collector) ToolBlock(msg string)            {}
func (c *collector) Summary(usage stream.Usage, stats stream.Stats) {
	c.mu.Lock()
	c.usage = usage
	c.mu.Unlock()
}
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
	provider := providers.ProviderFromContext(ctx)
	if provider == nil {
		// Fallback to global for backward compat during transition
		provider = GetProvider()
		if provider == nil {
			return ToolResult{Type: "result", Success: false, Error: "no LLM provider available (start with --model)"}
		}
	}

	defaultModel := providers.ModelFromContext(ctx)
	if defaultModel == "" {
		defaultModel = GetCurrentModel()
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

	// Run tasks concurrently (no timeout – runs until completion)
	results := runTasks(ctx, provider, tasks, 0)

	// Single task → return plain text (backward compatible)
	if len(results) == 1 {
		r := results[0]
		if !r.Success {
			return ToolResult{Type: "result", Success: false, Error: r.Error}
		}
		return ToolResult{Type: "result", Success: true, Content: r.Content}
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
	if m, ok := input["model"].(string); ok && m != "" {
		t.Model = m
	}
	if temp, ok := input["temperature"]; ok {
		switch v := temp.(type) {
		case float64:
			t.Temperature = &v
		}
	}
	return []subagentTask{t}, nil
}

func taskFromMap(m map[string]any) (subagentTask, error) {
	taskStr, _ := m["task"].(string)
	if taskStr == "" {
		return subagentTask{}, fmt.Errorf("task is required")
	}
	t := subagentTask{Task: taskStr}
	if model, ok := m["model"].(string); ok && model != "" {
		t.Model = model
	}
	if temp, ok := m["temperature"]; ok {
		switch v := temp.(type) {
		case float64:
			t.Temperature = &v
		}
	}
	return t, nil
}

func runTasks(ctx context.Context, globalProvider providers.Provider, tasks []subagentTask, timeoutSec int) []subagentResult {
	results := make([]subagentResult, len(tasks))
	var wg sync.WaitGroup

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t subagentTask) {
			defer wg.Done()
			results[idx] = runSingleTask(ctx, globalProvider, t, timeoutSec)
		}(i, task)
	}

	wg.Wait()
	return results
}

func runSingleTask(ctx context.Context, globalProvider providers.Provider, task subagentTask, timeoutSec int) subagentResult {
	runCtx := ctx
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
	}

	// Create isolated HTTP client with its own connection pool — share nothing
	// with parent agent. This prevents parent cancellation from leaking into
	// subagent requests and vice versa.
	isolatedClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        2,
			MaxIdleConnsPerHost: 1,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	runCtx = context.WithValue(runCtx, api.HTTPClientKey{}, isolatedClient)

	mName := task.Model
	prov := globalProvider

	if mName == "" {
		// No override – use the same provider and model as the parent
		mName = providers.ModelFromContext(ctx)
		if mName == "" {
			mName = GetCurrentModel()
		}
	} else if p, m, ok := providers.FindModel(mName); ok {
		// Model override – resolve provider/model from string
		prov = p
		mName = m
	}

	// Get tool index for streaming (passed by agent.executeTools)
	toolIdx := 0
	if idx, ok := ctx.Value(stream.ToolIdxCtxKey{}).(int); ok {
		toolIdx = idx
	}

	c := newStreamingCollector(ctx, toolIdx)
	msgs := []providers.RichMessage{
		{
			Role:    "user",
			Content: []providers.ContentBlock{{Type: "text", Text: task.Task}},
		},
	}

	cfg := agent.Config{
		Model:         mName,
		System:        providers.BuildSystemPrompt(),
		MaxRetries:    1,
		MaxIterations: 10, // limit tool-call iterations to prevent infinite loops
		Debug:         false,
		Tools:         &subagentToolRunner{},
		Schema:        GetSubagentToolsSchemaJSON(),
	}

	_, err := agent.Run(runCtx, prov, c, &msgs, cfg)

	// Flush any remaining partial line
	c.flushPartial()

	res := c.Result()
	res.Task = task.Task
	res.Model = mName

	if err != nil {
		res.Success = false
		res.Error = err.Error()
	} else {
		res.Success = true
	}

	return res
}

// subagentToolRunner wraps the global tool registry so subagents can use tools.
// Subagent tool itself is excluded to prevent recursion.
type subagentToolRunner struct{}

func (r *subagentToolRunner) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	if name == "subagent" {
		return "", fmt.Errorf("subagent tool is not available to subagents (recursion denied)")
	}
	res := RunTool(ctx, name, args)
	if res.Success {
		return res.Content, nil
	}
	return res.Content, fmt.Errorf("%s", res.Error)
}
