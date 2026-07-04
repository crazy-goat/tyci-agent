package tools

import (
	"context"
	"encoding/json"
)

// SubAgentRunner is the interface that tools/subagent.go uses to run
// agent tasks without importing the agent package directly.
// This keeps the tools package as a leaf layer with no upward dependencies.
type SubAgentRunner interface {
	// RunTask executes a single agent task and returns the result text.
	RunTask(ctx context.Context, task string, model string) (string, error)

	// RunTaskWithSystem executes a single agent task with a custom system prompt.
	RunTaskWithSystem(ctx context.Context, task string, model string, system string) (string, error)
}

type ToolResult struct {
	Type    string `json:"type"`
	Success bool   `json:"success"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
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
				"name":        "glob",
				"description": "Find files using glob patterns. Returns paths relative to cwd unless absolute=true.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern":          map[string]any{"description": "Glob pattern or array, e.g. **/*.ts or src/**/*.{ts,tsx}"},
						"cwd":              map[string]any{"type": "string", "description": "Base directory (default: .)"},
						"exclude":          map[string]any{"description": "Glob pattern or array to exclude"},
						"respectGitignore": map[string]any{"type": "boolean", "description": "Skip paths matched by .gitignore/.aiignore (default: true). Set false to include ignored files."},
						"limit":            map[string]any{"type": "integer", "description": "Max paths (default: 500)"},
						"includeDirs":      map[string]any{"type": "boolean", "description": "Include directories (default: false)"},
						"absolute":         map[string]any{"type": "boolean", "description": "Return absolute paths (default: false)"},
					},
					"required": []string{"pattern"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "grep",
				"description": "Search file contents using text, regex, or whole-word mode. Returns matches with file and line numbers by default.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern":          map[string]any{"type": "string", "description": "Text or regex to search for"},
						"cwd":              map[string]any{"type": "string", "description": "Base directory (default: .)"},
						"include":          map[string]any{"description": "Glob pattern or array to include (default: **/*)"},
						"exclude":          map[string]any{"description": "Glob pattern or array to exclude"},
						"respectGitignore": map[string]any{"type": "boolean", "description": "Skip paths matched by .gitignore/.aiignore (default: true). Set false to search ignored files."},
						"mode":             map[string]any{"type": "string", "enum": []string{"text", "regex", "word"}, "description": "Search mode (default: text)"},
						"caseSensitive":    map[string]any{"type": "boolean", "description": "Case-sensitive search (default: true)"},
						"context":          map[string]any{"type": "integer", "description": "Lines before/after each match (default: 0)"},
						"limit":            map[string]any{"type": "integer", "description": "Max results (default: 100)"},
						"output":           map[string]any{"type": "string", "enum": []string{"lines", "files", "count"}, "description": "Output format (default: lines)"},
						"maxLineLength":    map[string]any{"type": "integer", "description": "Trim long lines (default: 300)"},
					},
					"required": []string{"pattern"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "todo",
				"description": "Manage a per-run in-memory todo list. Use for multi-step tasks. Returns the full list.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"action":   map[string]any{"type": "string", "enum": []string{"add", "update", "doing", "blocked", "done", "remove", "list", "clear"}},
						"id":       map[string]any{"type": "integer", "description": "Todo id for update/done/remove"},
						"content":  map[string]any{"type": "string", "description": "Todo text"},
						"status":   map[string]any{"type": "string", "enum": []string{"todo", "doing", "done", "blocked"}, "description": "Default: todo"},
						"priority": map[string]any{"type": "string", "enum": []string{"low", "normal", "high"}, "description": "Default: normal"},
						"parentId": map[string]any{"type": "integer", "description": "Optional parent todo id"},
					},
					"required": []string{"action"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read",
				"description": "Read file contents. Use offset/limit for ranges. Set lineNumbers=true when you need exact line numbers for edits. Code files return their outline (symbol map: types, functions, methods, constants, fields) by default to save tokens — use symbol=NAME to pull one definition, offset/limit for a range, or full=true for the whole file. When exploring unfamiliar code, this outline-first flow is far more token-efficient than reading top-to-bottom.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":        map[string]any{"type": "string", "description": "File path to read"},
						"offset":      map[string]any{"type": "integer", "description": "Start line, 1-indexed"},
						"limit":       map[string]any{"type": "integer", "description": "Maximum lines to read"},
						"lineNumbers": map[string]any{"type": "boolean", "description": "Prefix lines as N| text (default: false)"},
						"outline":     map[string]any{"type": "boolean", "description": "Return only the symbol map (definitions + line numbers) instead of full contents. Use this first when exploring a large or unfamiliar code file to survey it cheaply; falls back to a normal read for non-code files."},
						"symbol":      map[string]any{"type": "string", "description": "Return the exact source body of the named definition (function/class/method/type). Use after outline to read one symbol precisely instead of guessing line ranges."},
						"full":        map[string]any{"type": "boolean", "description": "Force reading the entire file. Code files return an outline by default; set full=true to override and get all contents."},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "write",
				"description": "Write file content. Overwrites by default. range can replace, insert, or append.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "File path to write"},
						"content": map[string]any{"type": "string", "description": "Content to write"},
						"range":   map[string]any{"description": "Optional: line number, 'from...to', 'before:N', 'after:N', 'all', or -1/'append'"},
					},
					"required": []string{"path", "content"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "edit",
				"description": "Replace exact text in a file. By default oldString must match exactly once.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":       map[string]any{"type": "string", "description": "File path"},
						"oldString":  map[string]any{"type": "string", "description": "Exact text to replace"},
						"newString":  map[string]any{"type": "string", "description": "Replacement text"},
						"occurrence": map[string]any{"description": "Optional: occurrence number or 'all'. Default requires exactly one match"},
						"dryRun":     map[string]any{"type": "boolean", "description": "Preview without writing (default: false)"},
					},
					"required": []string{"path", "oldString", "newString"},
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
				"description": "Delegate a complex or independent task to a child agent with its own context window. Use when a task is self-contained, can run in parallel with other work, or would benefit from a separate reasoning chain. Good for: research questions, file operations across many files, independent subtasks. Provide a clear, specific task description AND state exactly what the child should return — the parent sees only the child's final text, not its tool calls. The child has read/grep/glob/write/edit/bash/todo tools (it cannot spawn further subagents) and a bounded tool-call budget, so keep each task narrow and completable. For a single task use 'task' (string); for parallel execution use 'tasks' (array).",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"task":  map[string]any{"type": "string", "description": "Clear, detailed task description for the child agent. Write it like a prompt: explain what to do, what files to read/write, what to return. The child has read/write/edit/bash tools."},
						"agent": map[string]any{"type": "string", "description": "Named agent to use (looks up ~/.tyci/agents/<name>.md for system prompt and config)"},
						"tasks": map[string]any{"type": "array", "description": "Array of parallel tasks to run concurrently", "items": map[string]any{"type": "object", "properties": map[string]any{
							"task":  map[string]any{"type": "string", "description": "Clear task description for this parallel subtask, including what to return. The child has read/grep/glob/write/edit/bash/todo tools."},
							"agent": map[string]any{"type": "string", "description": "Named agent to use"},
							"model": map[string]any{"type": "string", "description": "Optional model override (format: provider/model)"},
						}, "required": []string{"task"}}},
						"model": map[string]any{"type": "string", "description": "Optional model override for single task (format: provider/model, e.g. opencode-zen/big-pickle)"},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "load_skill",
				"description": "Load a skill by name and return its full content. Skills are markdown files stored in ~/.tyci/skills/<name>/SKILL.md.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string", "description": "Name of the skill to load (directory name)"},
					},
					"required": []string{"name"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "list_skills",
				"description": "List all available skills with their descriptions.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
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

var toolRegistry = map[string]Tool{
	"bash":        &BashTool{},
	"glob":        &GlobTool{},
	"grep":        &GrepTool{},
	"todo":        &TodoTool{},
	"read":        &ReadTool{},
	"write":       &WriteTool{},
	"edit":        &EditTool{},
	"load_skill":  &LoadSkillTool{},
	"list_skills": &ListSkillsTool{},
	"web":         &WebTool{},
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
