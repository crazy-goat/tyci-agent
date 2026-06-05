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
			"name":        "read",
			"description": "Read file contents. Text output is truncated to 2000 lines or 50KB (whichever first). Use offset (1-indexed line) and limit (max lines) for large files. Returns continuation hints when truncated.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":   map[string]any{"type": "string", "description": "File path to read"},
						"offset": map[string]any{"type": "integer", "description": "Line number to start from (1-indexed). Use offset from continuation hint to continue reading."},
						"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "write",
				"description": "Write content to file",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "File path to write"},
						"content": map[string]any{"type": "string", "description": "Content to write"},
						"append":  map[string]any{"type": "boolean", "description": "Append to file instead of overwriting (optional, default: false)"},
					},
					"required": []string{"path", "content"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "edit",
				"description": "Edit file - replace text",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":       map[string]any{"type": "string", "description": "File path"},
						"oldString":  map[string]any{"type": "string", "description": "Text to replace"},
						"newString":  map[string]any{"type": "string", "description": "Replacement text"},
						"replaceAll": map[string]any{"type": "boolean", "description": "Replace all occurrences (optional, default: false - replaces first only)"},
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
					},
					"required": []string{"command"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "subagent",
				"description": "Delegate complex or independent tasks to a child agent with its own context window. Use when a task is self-contained, can run in parallel with other work, or would benefit from a separate reasoning chain. Good for: research questions, file operations across many files, independent subtasks. Provide a clear, specific task description. The child agent has access to read/write/edit/bash tools. For single task use 'task' (string). For parallel execution use 'tasks' (array). Supports optional 'timeout' (seconds, default 120) and 'model' override.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"task":        map[string]any{"type": "string", "description": "Clear, detailed task description for the child agent. Write it like a prompt: explain what to do, what files to read/write, what to return. The child has read/write/edit/bash tools."},
						"tasks":       map[string]any{"type": "array", "description": "Array of parallel tasks to run concurrently", "items": map[string]any{"type": "object", "properties": map[string]any{
							"task":        map[string]any{"type": "string", "description": "Clear task description for this parallel subtask. The child agent has read/write/edit/bash tools."},
							"model":       map[string]any{"type": "string", "description": "Optional model override (format: provider/model)"},
							"temperature": map[string]any{"type": "number", "description": "Optional temperature (0.0-2.0)"},
						}, "required": []string{"task"}}},
						"model":       map[string]any{"type": "string", "description": "Optional model override for single task (format: provider/model, e.g. opencode-zen/big-pickle)"},
						"temperature": map[string]any{"type": "number", "description": "Optional temperature (0.0-2.0, default: 0.7)"},
						"timeout":     map[string]any{"type": "number", "description": "Optional timeout in seconds for each subagent (default: 120)"},
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
