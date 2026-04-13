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

	// Check for replaceAll mode
	replaceAll := false
	if allVal, ok := input["replaceAll"]; ok {
		switch v := allVal.(type) {
		case bool:
			replaceAll = v
		case string:
			replaceAll = v == "true" || v == "1"
		case float64:
			replaceAll = v != 0
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	// Replace: 1 = first occurrence, -1 = all occurrences
	replaceCount := 1
	if replaceAll {
		replaceCount = -1
	}

	content := strings.Replace(string(data), oldStr, newStr, replaceCount)

	// Check if any replacement was made
	if content == string(data) {
		return ToolResult{Type: "result", Success: false, Error: "text not found in file"}
	}

	err = os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	if replaceAll {
		return ToolResult{Type: "result", Success: true, Content: "edited all occurrences in " + path}
	}
	return ToolResult{Type: "result", Success: true, Content: "edited " + path}
}
