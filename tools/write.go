package tools

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type WriteTool struct{}

func (t *WriteTool) Name() string {
	return "write"
}

func (t *WriteTool) Run(ctx context.Context, input map[string]any) ToolResult {
	path, ok := input["path"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "path required"}
	}

	content, ok := input["content"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "content required"}
	}

	r, err := parseWriteRange(input["range"])
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	switch r.mode {
	case "append":
		return appendFile(path, content)
	case "all":
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}
		return ToolResult{Type: "result", Success: true, Content: "written " + path}
	case "lines":
		data, err := os.ReadFile(path)
		if err != nil {
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}
		lines, origTrailing := splitFileLines(string(data))
		if r.from < 1 || r.to < r.from || r.to > len(lines) {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("range %d...%d out of bounds (file has %d lines)", r.from, r.to, len(lines))}
		}
		repl, replTrailing := splitReplacementLines(content)
		newLines := make([]string, 0, len(lines)-(r.to-r.from+1)+len(repl))
		newLines = append(newLines, lines[:r.from-1]...)
		newLines = append(newLines, repl...)
		newLines = append(newLines, lines[r.to:]...)

		out := strings.Join(newLines, "\n")
		trailing := origTrailing
		if r.to >= len(lines) {
			trailing = replTrailing
		}
		if trailing && out != "" {
			out += "\n"
		}
		if err := os.WriteFile(path, []byte(out), 0644); err != nil {
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}
		if r.from == r.to {
			return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("replaced line %d in %s", r.from, path)}
		}
		return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("replaced lines %d...%d in %s", r.from, r.to, path)}
	}

	return ToolResult{Type: "result", Success: false, Error: "invalid range"}
}

func appendFile(path, content string) ToolResult {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	return ToolResult{Type: "result", Success: true, Content: "appended to " + path}
}

type writeRange struct {
	mode     string // all, append, lines
	from, to int
}

func parseWriteRange(v any) (writeRange, error) {
	if v == nil {
		return writeRange{mode: "all"}, nil
	}
	switch x := v.(type) {
	case float64:
		n := int(x)
		if n == -1 {
			return writeRange{mode: "append"}, nil
		}
		if n >= 1 {
			return writeRange{mode: "lines", from: n, to: n}, nil
		}
	case int:
		if x == -1 {
			return writeRange{mode: "append"}, nil
		}
		if x >= 1 {
			return writeRange{mode: "lines", from: x, to: x}, nil
		}
	case string:
		s := strings.TrimSpace(x)
		sl := strings.ToLower(s)
		if s == "" || sl == "all" || s == "0...-1" || s == "0..-1" {
			return writeRange{mode: "all"}, nil
		}
		if sl == "append" || s == "-1" {
			return writeRange{mode: "append"}, nil
		}
		sep := "..."
		if !strings.Contains(s, sep) {
			sep = ".."
		}
		if strings.Contains(s, sep) {
			parts := strings.SplitN(s, sep, 2)
			from, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
			to, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err1 != nil || err2 != nil {
				return writeRange{}, fmt.Errorf("invalid range %q", s)
			}
			if from == 0 && to == -1 {
				return writeRange{mode: "all"}, nil
			}
			return writeRange{mode: "lines", from: from, to: to}, nil
		}
		if n, err := strconv.Atoi(s); err == nil {
			if n == -1 {
				return writeRange{mode: "append"}, nil
			}
			if n >= 1 {
				return writeRange{mode: "lines", from: n, to: n}, nil
			}
		}
	}
	return writeRange{}, fmt.Errorf("invalid range; use line number, from...to, all, or -1/append")
}

func splitFileLines(s string) ([]string, bool) {
	if s == "" {
		return []string{}, false
	}
	trailing := strings.HasSuffix(s, "\n")
	body := strings.TrimSuffix(s, "\n")
	if body == "" {
		return []string{""}, trailing
	}
	return strings.Split(body, "\n"), trailing
}

func splitReplacementLines(s string) ([]string, bool) {
	if s == "" {
		return nil, false
	}
	trailing := strings.HasSuffix(s, "\n")
	body := strings.TrimSuffix(s, "\n")
	if body == "" {
		return []string{""}, trailing
	}
	return strings.Split(body, "\n"), trailing
}
