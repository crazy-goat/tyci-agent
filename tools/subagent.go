package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/decodo/tyci-agent/agent"
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
func (c *collector) ToolCallDelta(delta string) {}
func (c *collector) ToolCallEnd(name, result string) {}
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

func (t *SubagentTool) Run(ctx context.Context, input map[string]any) ToolResult {
	provider := GetProvider()
	if provider == nil {
		return ToolResult{Type: "result", Success: false, Error: "no LLM provider available (start with --model)"}
	}

	defaultModel := GetCurrentModel()
	if defaultModel == "" {
		return ToolResult{Type: "result", Success: false, Error: "no model specified and no default model set"}
	}

	// Parse timeout (seconds)
	var timeoutSec int
	if to, ok := input["timeout"]; ok {
		switch v := to.(type) {
		case float64:
			timeoutSec = int(v)
		case int:
			timeoutSec = v
		}
	}

	// Parse tasks
	tasks, err := parseTasks(input, defaultModel)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	if len(tasks) == 0 {
		return ToolResult{Type: "result", Success: false, Error: "no tasks provided"}
	}

	// Run tasks concurrently
	results := runTasks(ctx, provider, tasks, timeoutSec)

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
	if timeoutSec <= 0 {
		timeoutSec = 120 // default timeout: 2 minutes
	}
	var cancel context.CancelFunc
	runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	mName := task.Model
	prov := globalProvider

	if mName == "" {
		// No override – use the same provider and model as the parent
		mName = GetCurrentModel()
	} else if p, m, ok := providers.FindModel(mName); ok {
		// Model override – resolve provider/model from string
		prov = p
		mName = m
	}

	c := newCollector()
	msgs := []providers.Message{
		{Role: "user", Content: task.Task},
	}

	cfg := agent.Config{
		Model:      mName,
		System:     providers.BuildSystemPrompt(),
		MaxRetries: 1,
		Debug:      false,
		Tools:      &subagentToolRunner{},
		Schema:     GetSubagentToolsSchemaJSON(),
	}

	err := agent.Run(runCtx, prov, c, &msgs, cfg)
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
