package tools

import "encoding/json"

type ToolResult struct {
	Type    string `json:"type"`
	Success bool   `json:"success"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

type Tool interface {
	Name() string
	Run(input map[string]any) ToolResult
}

func GetToolsSchema() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read",
				"description": "Read file contents",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":   map[string]any{"type": "string", "description": "File path to read"},
						"offset": map[string]any{"type": "integer", "description": "Start reading from this byte offset (optional)"},
						"limit":  map[string]any{"type": "integer", "description": "Maximum bytes to read (optional)"},
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
						"command": map[string]any{"type": "string", "description": "Command to execute"},
					},
					"required": []string{"command"},
				},
			},
		},
	}
}

var toolsSchema json.RawMessage

type responsesTool struct {
	Type        string      `json:"type"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters"`
}

func init() {
	data, _ := json.Marshal(GetToolsSchema())
	toolsSchema = data
}

func GetToolsSchemaJSON() json.RawMessage {
	return toolsSchema
}

func GetToolsSchemaForResponses() []responsesTool {
	return []responsesTool{
		{
			Type:        "function",
			Name:        "read",
			Description: "Read file contents",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":   map[string]any{"type": "string", "description": "File path to read"},
					"offset": map[string]any{"type": "integer", "description": "Start reading from this byte offset (optional)"},
					"limit":  map[string]any{"type": "integer", "description": "Maximum bytes to read (optional)"},
				},
				"required": []string{"path"},
			},
		},
		{
			Type:        "function",
			Name:        "write",
			Description: "Write content to file",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "File path to write"},
					"content": map[string]any{"type": "string", "description": "Content to write"},
					"append":  map[string]any{"type": "boolean", "description": "Append to file instead of overwriting (optional, default: false)"},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Type:        "function",
			Name:        "edit",
			Description: "Edit file - replace text",
			Parameters: map[string]any{
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
		{
			Type:        "function",
			Name:        "bash",
			Description: "Execute shell command",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "Command to execute"},
				},
				"required": []string{"command"},
			},
		},
	}
}

var toolRegistry = map[string]Tool{
	"bash":  &BashTool{},
	"read":  &ReadTool{},
	"write": &WriteTool{},
	"edit":  &EditTool{},
}

func RunTool(name string, arguments map[string]any) ToolResult {
	tool, ok := toolRegistry[name]
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "unknown tool: " + name}
	}
	return tool.Run(arguments)
}
