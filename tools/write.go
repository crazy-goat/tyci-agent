package tools

import (
	"os"
)

type WriteTool struct{}

func (t *WriteTool) Name() string {
	return "write"
}

func (t *WriteTool) Run(input map[string]any) ToolResult {
	path, ok := input["path"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "path required"}
	}

	content, ok := input["content"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "content required"}
	}

	err := os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	return ToolResult{Type: "result", Success: true, Content: "written " + path}
}
