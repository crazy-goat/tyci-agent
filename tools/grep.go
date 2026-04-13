package tools

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type GrepTool struct{}

func (t *GrepTool) Name() string {
	return "grep"
}

func (t *GrepTool) Run(input map[string]any) ToolResult {
	pattern, ok := input["pattern"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "pattern required"}
	}

	dir := "."
	if d, ok := input["path"].(string); ok {
		dir = d
	}

	filePattern := "*"
	if f, ok := input["include"].(string); ok {
		filePattern = f
	}

	matches, err := grepRecursive(dir, filePattern, pattern)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	return ToolResult{Type: "result", Success: true, Content: matches}
}

func grepRecursive(dir, pattern, search string) (string, error) {
	var results []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		matched, _ := filepath.Match(pattern, name)
		if !matched && pattern != "*" && !strings.Contains(name, pattern) {
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.Contains(line, search) {
				results = append(results, path+":"+strconv.Itoa(i+1)+": "+line)
			}
		}
		return nil
	})
	return joinLines(results), err
}
