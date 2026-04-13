package tools

import (
	"os"
	"strconv"
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

	// Handle offset and limit for partial file reading
	offset := 0
	limit := 0

	if offsetVal, ok := input["offset"]; ok {
		switch v := offsetVal.(type) {
		case float64:
			offset = int(v)
		case int:
			offset = v
		case string:
			if parsed, err := strconv.Atoi(v); err == nil {
				offset = parsed
			}
		}
	}

	if limitVal, ok := input["limit"]; ok {
		switch v := limitVal.(type) {
		case float64:
			limit = int(v)
		case int:
			limit = v
		case string:
			if parsed, err := strconv.Atoi(v); err == nil {
				limit = parsed
			}
		}
	}

	file, err := os.Open(path)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	defer file.Close()

	// If offset specified, seek to that position
	if offset > 0 {
		_, err = file.Seek(int64(offset), 0)
		if err != nil {
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}
	}

	// Read with limit if specified
	if limit > 0 {
		buf := make([]byte, limit)
		n, err := file.Read(buf)
		if err != nil && err.Error() != "EOF" {
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}
		return ToolResult{Type: "result", Success: true, Content: string(buf[:n])}
	}

	// Read entire file (or from offset to end)
	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	if offset > 0 && offset < len(data) {
		return ToolResult{Type: "result", Success: true, Content: string(data[offset:])}
	}

	return ToolResult{Type: "result", Success: true, Content: string(data)}
}
