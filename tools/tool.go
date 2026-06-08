package tools

import (
	"context"
	"encoding/json"

	"github.com/decodo/tyci-agent/providers"
)

// GlobalProvider allows subagent tool to access the LLM provider.
var GlobalProvider providers.Provider

// GlobalModel holds the current model string (e.g. "opencode-zen/big-pickle")
var GlobalModel string

// SetProvider sets the global provider for subagent tool.
func SetProvider(p providers.Provider) {
	GlobalProvider = p
}

// GetProvider returns the global provider.
func GetProvider() providers.Provider {
	return GlobalProvider
}

// SetCurrentModel sets the current model string.
func SetCurrentModel(m string) {
	GlobalModel = m
}

// GetCurrentModel returns the current model string.
func GetCurrentModel() string {
	return GlobalModel
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
						"pattern":     map[string]any{"description": "Glob pattern or array, e.g. **/*.ts or src/**/*.{ts,tsx}"},
						"cwd":         map[string]any{"type": "string", "description": "Base directory (default: .)"},
						"exclude":     map[string]any{"description": "Glob pattern or array to exclude"},
						"limit":       map[string]any{"type": "integer", "description": "Max paths (default: 500)"},
						"includeDirs": map[string]any{"type": "boolean", "description": "Include directories (default: false)"},
						"absolute":    map[string]any{"type": "boolean", "description": "Return absolute paths (default: false)"},
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
						"pattern":       map[string]any{"type": "string", "description": "Text or regex to search for"},
						"cwd":           map[string]any{"type": "string", "description": "Base directory (default: .)"},
						"include":       map[string]any{"description": "Glob pattern or array to include (default: **/*)"},
						"exclude":       map[string]any{"description": "Glob pattern or array to exclude"},
						"mode":          map[string]any{"type": "string", "enum": []string{"text", "regex", "word"}, "description": "Search mode (default: text)"},
						"caseSensitive": map[string]any{"type": "boolean", "description": "Case-sensitive search (default: true)"},
						"context":       map[string]any{"type": "integer", "description": "Lines before/after each match (default: 0)"},
						"limit":         map[string]any{"type": "integer", "description": "Max results (default: 100)"},
						"output":        map[string]any{"type": "string", "enum": []string{"lines", "files", "count"}, "description": "Output format (default: lines)"},
						"maxLineLength": map[string]any{"type": "integer", "description": "Trim long lines (default: 300)"},
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
				"description": "Read file contents. Use offset/limit for ranges. Set lineNumbers=true when you need exact line numbers for edits.",
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
				"description": "Delegate complex or independent tasks to a child agent with its own context window. Use when a task is self-contained, can run in parallel with other work, or would benefit from a separate reasoning chain. Good for: research questions, file operations across many files, independent subtasks. Provide a clear, specific task description. The child agent has access to read/write/edit/bash tools. Runs until completion (no timeout). For single task use 'task' (string). For parallel execution use 'tasks' (array).",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"task": map[string]any{"type": "string", "description": "Clear, detailed task description for the child agent. Write it like a prompt: explain what to do, what files to read/write, what to return. The child has read/write/edit/bash tools."},
						"tasks": map[string]any{"type": "array", "description": "Array of parallel tasks to run concurrently", "items": map[string]any{"type": "object", "properties": map[string]any{
							"task":        map[string]any{"type": "string", "description": "Clear task description for this parallel subtask. The child agent has read/write/edit/bash tools."},
							"model":       map[string]any{"type": "string", "description": "Optional model override (format: provider/model)"},
							"temperature": map[string]any{"type": "number", "description": "Optional temperature (0.0-2.0)"},
						}, "required": []string{"task"}}},
						"model":       map[string]any{"type": "string", "description": "Optional model override for single task (format: provider/model, e.g. opencode-zen/big-pickle)"},
						"temperature": map[string]any{"type": "number", "description": "Optional temperature (0.0-2.0, default: 0.7)"},
					},
				},
			},
		},
	}
}

var toolsSchema json.RawMessage
var subagentToolsSchema json.RawMessage

func init() {
	data, _ := json.Marshal(GetToolsSchema())
	toolsSchema = data
	data, _ = json.Marshal(GetSubagentToolsSchema())
	subagentToolsSchema = data
}

func GetToolsSchemaJSON() json.RawMessage {
	return toolsSchema
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
	"bash":     &BashTool{},
	"glob":     &GlobTool{},
	"grep":     &GrepTool{},
	"todo":     &TodoTool{},
	"read":     &ReadTool{},
	"write":    &WriteTool{},
	"edit":     &EditTool{},
	"subagent": &SubagentTool{},
}

func RunTool(ctx context.Context, name string, arguments map[string]any) ToolResult {
	tool, ok := toolRegistry[name]
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "unknown tool: " + name}
	}
	return tool.Run(ctx, arguments)
}
