package tools

import (
	"os"
	"strings"
)

type EditTool struct{}

func (t *EditTool) Name() string {
	return "edit"
}

func (t *EditTool) Run(input map[string]any) ToolResult {
	path, ok := input["path"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "path required"}
	}

	oldStr, ok := input["oldString"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "oldString required"}
	}

	newStr, ok := input["newString"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "newString required"}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	content := strings.Replace(string(data), oldStr, newStr, 1)
	err = os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	return ToolResult{Type: "result", Success: true, Content: "edited " + path}
}
