package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// SubagentOptions are per-call knobs the parent supplies for a single
// subagent invocation. Built per-call in main.go and tools/subagent.go;
// not serialized, so future fields are added as Go struct additions.
type SubagentOptions struct {
	// MaxIterations caps the child agent's tool-call turns. Semantics
	// (mirrors agent.Options.MaxIterations):
	//   - nil       → use ResolveMaxIter's default (currently unlimited)
	//   - 0 or <0   → unlimited
	//   - >0        → cap the child at that many turns
	// Note: 0 is *not* "no turns allowed" — it's "use the unlimited
	// default". A parent that wants to forbid tool calls entirely must
	// avoid invoking the subagent at all (or use a child without tools).
	MaxIterations *int

	// Tools, when non-empty, restricts the child to these tool names.
	// Empty/nil means "every tool except subagent" (today's behavior).
	Tools []string

	// Temperature, when non-nil, is the sampling temperature for the child.
	// Pointer because 0 is meaningful ("deterministic"), not "unset".
	Temperature *float64

	// Fallbacks are "provider/model" specs, NOT resolved clients: the tools
	// package is a leaf and must not reach into the provider catalog. The
	// composition root (main.go) resolves them, exactly as it already does
	// for the top-level agent — see agent.Config.Fallbacks.
	Fallbacks []string

	// SystemPromptMode mirrors agentdefs.Def.SystemPromptMode ("append" or
	// "replace"). The tools package does not build prompts — it only carries
	// the mode to the composition root, which owns providers.
	SystemPromptMode string
}

// DefaultSubagentMaxIterations is the cap applied when SubagentOptions has
// no explicit MaxIterations. -1 means unlimited. Defined as a constant (not
// a function) so both main.go and tests see the same value.
//
// This is a deliberate behavior change from the hard-coded 10 that preceded
// it: a caller that omits MaxIterations now runs unbounded, held only by
// SubagentOptions' semantics and the wall-clock backstop in subagent.go. A
// caller that wants a finite cap has to pass an explicit positive integer.
const DefaultSubagentMaxIterations = -1

// TruncatedMarker is the literal suffix appended to a single-task subagent
// result's content when the result is flagged as truncated, so the parent
// LLM has a stable, parseable token (in addition to the inline [note: ...]
// prose). The parallel-array path encodes truncation per-item via
// json.Marshal; single-task has no such structural path because the agent
// runner turns tool results into a `(string, error)` at the package
// boundary, so this marker is the only way to surface the flag. Exported
// because the package-main caller (cmd_interactive.go toolsAdapter) needs
// to use the same literal.
const TruncatedMarker = "[truncated=true]"

// ResolveMaxIter converts SubagentOptions into the concrete cap to pass to
// agent.Options.MaxIterations. Separated from main.go so it can be table-
// tested in tools/subagent_test.go.
func ResolveMaxIter(opts SubagentOptions) int {
	if opts.MaxIterations == nil {
		return DefaultSubagentMaxIterations
	}
	v := *opts.MaxIterations
	if v <= 0 {
		return DefaultSubagentMaxIterations
	}
	return v
}

// SubAgentRunner is the interface that tools/subagent.go uses to run
// agent tasks without importing the agent package directly.
// This keeps the tools package as a leaf layer with no upward dependencies.
type SubAgentRunner interface {
	// RunTask executes a single agent task and returns the result text.
	RunTask(ctx context.Context, task string, model string, opts SubagentOptions) (string, error)

	// RunTaskWithSystem executes a single agent task with a custom system prompt.
	RunTaskWithSystem(ctx context.Context, task string, model string, system string, opts SubagentOptions) (string, error)
}

// ToolResult is the outcome of a single tool execution, returned to the
// calling LLM as a JSON-encoded tool message.
type ToolResult struct {
	Type    string `json:"type"`
	Success bool   `json:"success"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
	// Truncated is true when the tool ran to completion but the result is
	// known to be incomplete (e.g. a subagent hit its MaxIterations cap and
	// the child self-stopped with a partial answer). The content is still
	// usable, but callers and parents should treat it with reduced
	// confidence. Distinct from Success=false.
	Truncated bool `json:"truncated,omitempty"`
}

type Tool interface {
	Name() string
	Run(ctx context.Context, input map[string]any) ToolResult
}

func GetToolsSchema() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "find",
				"description": "Find files by glob pattern or search their contents. Use method=\"glob\" for file path patterns (e.g. **/*.go) or method=\"grep\" for text/regex/word search inside files.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"method":        map[string]any{"type": "string", "enum": []string{"glob", "grep"}, "description": "Search method: \"glob\" to find files by path pattern, \"grep\" to search file contents (default: \"glob\")"},
						"pattern":       map[string]any{"description": "Glob pattern (method: glob) or text/regex (method: grep)"},
						"cwd":           map[string]any{"type": "string", "description": "Base directory (default: .)"},
						"include":       map[string]any{"description": "Grep only — glob pattern or array to include (default: **/*)"},
						"exclude":       map[string]any{"description": "Glob pattern or array to exclude (default: .git/**, node_modules/**, etc.)"},
						"mode":          map[string]any{"type": "string", "enum": []string{"text", "regex", "word"}, "description": "Grep only — search mode (default: text)"},
						"caseSensitive": map[string]any{"type": "boolean", "description": "Grep only — case-sensitive search (default: true)"},
						"context":       map[string]any{"type": "integer", "description": "Grep only — lines of context before/after each match (default: 0)"},
						"limit":         map[string]any{"type": "integer", "description": "Max results (default: 500 for glob, 100 for grep)"},
						"output":        map[string]any{"type": "string", "enum": []string{"lines", "files", "count"}, "description": "Grep only — output format (default: lines)"},
						"maxLineLength": map[string]any{"type": "integer", "description": "Grep only — trim long lines (default: 300)"},
					},
					"required": []string{"pattern"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "todo",
				"description": "Manage a per-run in-memory todo list. Use for multi-step tasks. Returns the full list. When you need to add several unrelated items at once, prefer action=\"add_batch\" with items=[...] instead of issuing N separate add calls — the result returns the full new list with assigned ids in one round-trip.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"action":   map[string]any{"type": "string", "enum": []string{"add", "add_batch", "update", "doing", "blocked", "done", "remove", "list", "clear"}},
						"id":       map[string]any{"type": "integer", "description": "Todo id for update/done/remove"},
						"content":  map[string]any{"type": "string", "description": "Todo text (used by add)"},
						"status":   map[string]any{"type": "string", "enum": []string{"todo", "doing", "done", "blocked"}, "description": "Default: todo"},
						"parentId": map[string]any{"type": "integer", "description": "Optional parent todo id"},
						"items": map[string]any{
							"type":        "array",
							"description": "add_batch only — array of todo entries to create. Each: {content: string (required), status?: 'todo'|'doing'|'done'|'blocked', parentId?: number}. Example: [{content:\"Write tests\"}, {content:\"Fix bug\", status:\"doing\"}]",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"content":  map[string]any{"type": "string", "description": "Todo text"},
									"status":   map[string]any{"type": "string", "enum": []string{"todo", "doing", "done", "blocked"}, "description": "Default: todo"},
									"parentId": map[string]any{"type": "integer", "description": "Optional parent todo id"},
								},
								"required": []string{"content"},
							},
						},
					},
					"required": []string{"action"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read",
				"description": "Read file contents. Use offset/limit for ranges. Set lineNumbers=true when you need exact line numbers for edits. Returns full contents; truncate/inspect with offset and limit.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":        map[string]any{"type": "string", "description": "File path to read"},
						"offset":      map[string]any{"type": "integer", "description": "Start line, 1-indexed"},
						"limit":       map[string]any{"type": "integer", "description": "Maximum lines to read"},
						"lineNumbers": map[string]any{"type": "boolean", "description": "Prefix lines as N| text (default: false)"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "write",
				"description": "Write file content or replace text. Use content (with optional range) to write; use oldString+newString to replace exact text (edit mode).",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":       map[string]any{"type": "string", "description": "File path to write"},
						"content":    map[string]any{"type": "string", "description": "Content to write (write mode; omit when using oldString)"},
						"range":      map[string]any{"description": "Write mode only: line number, 'from...to', 'before:N', 'after:N', 'all', or -1/'append'"},
						"oldString":  map[string]any{"type": "string", "description": "Exact text to replace (edit mode; triggers edit when present)"},
						"newString":  map[string]any{"type": "string", "description": "Replacement text (edit mode; required with oldString)"},
						"occurrence": map[string]any{"description": "Edit mode only: occurrence number or 'all'. Default requires exactly one match"},
						"dryRun":     map[string]any{"type": "boolean", "description": "Edit mode only: preview without writing (default: false)"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "bash",
				"description": "Execute shell command",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"description": map[string]any{"type": "string", "description": "Short description of what this command does"},
						"command":     map[string]any{"type": "string", "description": "Command to execute"},
						"timeout":     map[string]any{"type": "integer", "description": "Optional timeout in seconds for this command (default: 120)"},
					},
					"required": []string{"command"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "subagent",
				"description": "Delegate a complex or independent task to a child agent with its own context window. Use when a task is self-contained, can run in parallel with other work, or would benefit from a separate reasoning chain. Good for: research questions, file operations across many files, independent subtasks. Provide a clear, specific task description AND state exactly what the child should return — the parent sees only the child's final text, not its tool calls. The child has read/write/find/bash/todo tools (it cannot spawn further subagents) and is bounded by an optional max_iterations cap and a 600s wall-clock timeout; keep each task narrow and completable. For a single task use 'task' (string); for parallel execution use 'tasks' (array).",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"task":  map[string]any{"type": "string", "description": "Clear, detailed task description for the child agent. Write it like a prompt: explain what to do, what files to read/write, what to return. The child has read/write/bash tools."},
						"agent": map[string]any{"type": "string", "description": "Named agent to use. Definitions live in ./.tyci/agents/<name>.md (project) or ~/.tyci/agents/<name>.md (global), project winning; each supplies the child's system prompt and may pin its model, max_iterations and allowed tools. The available names are listed in the system prompt; an unknown name is an error, not a fallback."},
						"tasks": map[string]any{"type": "array", "description": "Array of parallel tasks to run concurrently", "items": map[string]any{"type": "object", "properties": map[string]any{
							"task":           map[string]any{"type": "string", "description": "Clear task description for this parallel subtask, including what to return. The child has read/write/find/bash/todo tools."},
							"agent":          map[string]any{"type": "string", "description": "Named agent to use"},
							"model":          map[string]any{"type": "string", "description": "Optional model override (format: provider/model)"},
							"max_iterations": map[string]any{"type": "integer", "description": "Cap this child's tool-call turns. Set a positive integer to bound a risky subtask (e.g. exploration, code review); omit to use the runner default (currently unlimited, bounded by a 600s wall-clock timeout). 0 and negative values mean unlimited."},
						}, "required": []string{"task"}}},
						"model":          map[string]any{"type": "string", "description": "Optional model override for single task (format: provider/model, e.g. opencode-zen/big-pickle)"},
						"max_iterations": map[string]any{"type": "integer", "description": "Cap on the child's tool-call turns. Omit or 0 to use the runner's default (currently unlimited); negative = unlimited. Useful for bounding long-running subtasks like exploration or code review."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "wait",
				"description": "Deliberately wait for a period of time instead of polling in a busy loop. Use when you know an operation (yours or a background job's) will take roughly N seconds and you want to pause before checking again — e.g. wait(seconds=600, note=\"waiting for the deploy to finish\") instead of repeatedly re-checking. If job_id is provided, waits for that background job to finish (or until timeout) and reports its status instead of just sleeping; omit job_id for a plain timed wait.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"seconds": map[string]any{"type": "integer", "description": fmt.Sprintf("How long to wait, in seconds. Clamped to [%d, %d].", MinWaitSeconds, MaxWaitSeconds)},
						"job_id":  map[string]any{"type": "string", "description": "Optional id of a background job to wait/poll for instead of a plain sleep. Requires job tracking to be configured; omit for a plain wait."},
						"note":    map[string]any{"type": "string", "description": "Optional note describing what you're waiting for, echoed back for context."},
					},
					"required": []string{"seconds"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "skills",
				"description": "Manage skills. Call without parameters to list all available skills. Call with name to load a specific skill's full content. Skills are markdown files stored in ~/.tyci/skills/<name>/SKILL.md.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string", "description": "Name of the skill to load (directory name). If omitted, lists all available skills."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "web",
				"description": "Access the web. Use method=search for real-time web search (current events, docs, anything not in training data). Use method=lookup for fast encyclopedic facts, Wikipedia summaries, and quick references — it's enough for most knowledge questions and cheaper. Use method=get to fetch a specific URL.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"method":     map[string]any{"type": "string", "enum": []string{"search", "lookup", "get"}, "description": "search=real-time web search (Exa AI, needs internet); lookup=encyclopedic facts (DuckDuckGo+Wikipedia, works for most knowledge); get=fetch URL"},
						"what":       map[string]any{"type": "string", "description": "Search query, lookup term, or URL to fetch"},
						"numResults": map[string]any{"type": "integer", "description": "search only — number of results (default 8, max 25)"},
						"type":       map[string]any{"type": "string", "enum": []string{"auto", "fast", "deep"}, "description": "search only — Exa search mode (default auto)"},
						"format":     map[string]any{"type": "string", "enum": []string{"markdown", "original"}, "description": "get only — output format (default markdown)"},
					},
					"required": []string{"method", "what"},
				},
			},
		},
	}
}

var toolsSchema json.RawMessage
var subagentToolsSchema json.RawMessage

func init() {
	// Load Lua tools from user directories
	LoadAndRegisterLuaTools()

	data, _ := json.Marshal(GetToolsSchema())
	toolsSchema = data
	data, _ = json.Marshal(GetSubagentToolsSchema())
	subagentToolsSchema = data
}

func GetToolsSchemaJSON() json.RawMessage {
	return toolsSchema
}

// GetAllToolsSchema returns all tools including MCP tools.
func GetAllToolsSchema() []map[string]any {
	schema := GetToolsSchema()
	if mcpRunner := GetMCPToolRunner(); mcpRunner != nil {
		schema = append(schema, mcpRunner.MCPToolsSchema()...)
	}
	return schema
}

// GetAllToolsSchemaJSON returns all tools schema as JSON including MCP tools.
func GetAllToolsSchemaJSON() json.RawMessage {
	data, _ := json.Marshal(GetAllToolsSchema())
	return data
}

// GetSubagentToolsSchema returns tool definitions excluding "subagent" (prevents recursion).
func GetSubagentToolsSchema() []map[string]any {
	schema := GetToolsSchema()
	filtered := make([]map[string]any, 0, len(schema))
	for _, s := range schema {
		if fn, ok := s["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name == "subagent" {
				continue
			}
		}
		filtered = append(filtered, s)
	}
	return filtered
}

func GetSubagentToolsSchemaJSON() json.RawMessage {
	return subagentToolsSchema
}

// GetSubagentToolsSchemaJSONFor returns the subagent-visible tool schema
// restricted to allowed (a named agent's frontmatter `tools:` list).
// Empty/nil allowed means no restriction — returns the cached
// GetSubagentToolsSchemaJSON() unchanged, so the common case (no
// whitelist) stays a cheap map lookup instead of a fresh marshal per call.
//
// "subagent" is dropped even if allowed lists it explicitly: recursion into
// child subagents is never permitted, regardless of what an agent
// definition's frontmatter says. An allowed name that matches no known tool
// is silently skipped, not an error — a typo in an agent's `tools:` line
// should degrade to "that tool is unavailable", not a startup crash.
func GetSubagentToolsSchemaJSONFor(allowed []string) json.RawMessage {
	if len(allowed) == 0 {
		return GetSubagentToolsSchemaJSON()
	}
	want := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		if name == "subagent" {
			continue
		}
		want[name] = true
	}
	schema := GetSubagentToolsSchema()
	filtered := make([]map[string]any, 0, len(schema))
	for _, s := range schema {
		fn, ok := s["function"].(map[string]any)
		if !ok {
			continue
		}
		name, ok := fn["name"].(string)
		if !ok || !want[name] {
			continue
		}
		filtered = append(filtered, s)
	}
	data, _ := json.Marshal(filtered)
	return data
}

var toolRegistry = map[string]Tool{
	"bash":   &BashTool{},
	"find":   &FindTool{},
	"todo":   &TodoTool{},
	"read":   &ReadTool{},
	"write":  &WriteTool{},
	"skills": &SkillsTool{},
	"web":    &WebTool{},
	// Waiter is nil until a future "jobs" package is wired in (see
	// tools/wait.go); plain wait (no job_id) works without it.
	"wait": &WaitTool{},
}

// subagentToolInstance is the singleton SubagentTool used by the registry.
var subagentToolInstance *SubagentTool

// SetSubAgentRunner sets the runner for the subagent tool and registers it.
// Must be called before any subagent tool usage.
func SetSubAgentRunner(runner SubAgentRunner) {
	subagentToolInstance = &SubagentTool{Runner: runner}
	toolRegistry["subagent"] = subagentToolInstance
}

func RunTool(ctx context.Context, name string, arguments map[string]any) ToolResult {
	tool, ok := toolRegistry[name]
	if ok {
		return tool.Run(ctx, arguments)
	}

	// Check MCP tools
	if mcpRunner := GetMCPToolRunner(); mcpRunner != nil && mcpRunner.HasTool(name) {
		return mcpRunner.RunTool(ctx, name, arguments)
	}

	return ToolResult{Type: "result", Success: false, Error: "unknown tool: " + name}
}

// MaxParallelFor reports the dispatcher's per-tool concurrency limit for the
// given tool name. 0 (the default for tools that don't implement MaxParallel)
// means no limit — calls run concurrently like every other tool. 1 forces
// the dispatcher to run calls of that tool serially inside a single goroutine
// when several appear in the same LLM response. Used by agent.executeTools
// to honour tools whose state is not safe under concurrent calls from the
// same batch. MCP tools are always reported as 0 (no special handling).
func MaxParallelFor(name string) int {
	if mcpRunner := GetMCPToolRunner(); mcpRunner != nil && mcpRunner.HasTool(name) {
		return 0
	}
	tool, ok := toolRegistry[name]
	if !ok {
		return 0
	}
	if mp, ok := tool.(interface{ MaxParallel() int }); ok {
		return mp.MaxParallel()
	}
	return 0
}
