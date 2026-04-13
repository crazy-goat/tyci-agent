package tools

import (
	"os"
)

type ReadTool struct{}

func (t *ReadTool) Name() string {
	return "read"
}

func (t *ReadTool) Run(input map[string]any) ToolResult {
	path, ok := input["path"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "path required"}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	return ToolResult{Type: "result", Success: true, Content: string(data)}
}
