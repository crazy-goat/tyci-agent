package tools

import (
	"path/filepath"
)

type GlobTool struct{}

func (t *GlobTool) Name() string {
	return "glob"
}

func (t *GlobTool) Run(input map[string]any) ToolResult {
	pattern, ok := input["pattern"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "pattern required"}
	}

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	return ToolResult{Type: "result", Success: true, Content: joinLines(matches)}
}

func joinLines(arr []string) string {
	out := ""
	for i, s := range arr {
		if i > 0 {
			out += "\n"
		}
		out += s
	}
	return out
}
