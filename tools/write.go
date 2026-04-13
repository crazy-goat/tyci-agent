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

	// Check for append mode
	appendMode := false
	if appendVal, ok := input["append"]; ok {
		switch v := appendVal.(type) {
		case bool:
			appendMode = v
		case string:
			appendMode = v == "true" || v == "1"
		case float64:
			appendMode = v != 0
		}
	}

	if appendMode {
		// Append to file
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}
		defer f.Close()

		_, err = f.WriteString(content)
		if err != nil {
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}
		return ToolResult{Type: "result", Success: true, Content: "appended to " + path}
	}

	// Overwrite file
	err := os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	return ToolResult{Type: "result", Success: true, Content: "written " + path}
}
