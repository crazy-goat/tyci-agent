package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

type EditTool struct{}

func (t *EditTool) Name() string { return "edit" }

func (t *EditTool) Run(ctx context.Context, input map[string]any) ToolResult {
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return ToolResult{Type: "result", Success: false, Error: "path required"}
	}
	oldStr, ok := input["oldString"].(string)
	if !ok || oldStr == "" {
		return ToolResult{Type: "result", Success: false, Error: "oldString required"}
	}
	newStr, ok := input["newString"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "newString required"}
	}
	dryRun := boolParam(input, "dryRun", false)

	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	text := string(data)
	matches := findOccurrences(text, oldStr)
	if len(matches) == 0 {
		return ToolResult{Type: "result", Success: false, Error: "oldString not found in file"}
	}

	occ, err := parseOccurrence(input)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	var selected []int
	if occ.all {
		selected = matches
	} else if occ.n > 0 {
		if occ.n > len(matches) {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("occurrence %d requested, but only %d matches found", occ.n, len(matches))}
		}
		selected = []int{matches[occ.n-1]}
	} else {
		if len(matches) != 1 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("oldString matched %d times; use occurrence or make oldString more specific", len(matches))}
		}
		selected = matches
	}

	lineRanges := matchLineRanges(text, selected, len(oldStr))
	if dryRun {
		return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("would replace %d occurrence(s) in %s at %s", len(selected), path, formatLineRanges(lineRanges))}
	}

	newText := replaceAtOffsets(text, selected, len(oldStr), newStr)
	if err := os.WriteFile(path, []byte(newText), 0644); err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("replaced %d occurrence(s) in %s at %s", len(selected), path, formatLineRanges(lineRanges))}
}

type occurrenceSpec struct {
	all bool
	n   int
}

func parseOccurrence(input map[string]any) (occurrenceSpec, error) {
	val, ok := input["occurrence"]
	if !ok || val == nil {
		return occurrenceSpec{}, nil
	}
	switch v := val.(type) {
	case string:
		s := strings.TrimSpace(strings.ToLower(v))
		if s == "" {
			return occurrenceSpec{}, nil
		}
		if s == "all" {
			return occurrenceSpec{all: true}, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return occurrenceSpec{}, fmt.Errorf("occurrence must be positive number or 'all'")
		}
		return occurrenceSpec{n: n}, nil
	case float64:
		n := int(v)
		if n < 1 {
			return occurrenceSpec{}, fmt.Errorf("occurrence must be positive number or 'all'")
		}
		return occurrenceSpec{n: n}, nil
	case int:
		if v < 1 {
			return occurrenceSpec{}, fmt.Errorf("occurrence must be positive number or 'all'")
		}
		return occurrenceSpec{n: v}, nil
	}
	return occurrenceSpec{}, fmt.Errorf("occurrence must be positive number or 'all'")
}

func findOccurrences(text, needle string) []int {
	var out []int
	for pos := 0; ; {
		idx := strings.Index(text[pos:], needle)
		if idx < 0 {
			return out
		}
		abs := pos + idx
		out = append(out, abs)
		pos = abs + len(needle)
	}
}

func replaceAtOffsets(text string, offsets []int, oldLen int, replacement string) string {
	sort.Ints(offsets)
	var b strings.Builder
	b.Grow(len(text) + len(offsets)*len(replacement))
	pos := 0
	for _, off := range offsets {
		b.WriteString(text[pos:off])
		b.WriteString(replacement)
		pos = off + oldLen
	}
	b.WriteString(text[pos:])
	return b.String()
}

type lineRange struct{ from, to int }

func matchLineRanges(text string, offsets []int, length int) []lineRange {
	out := make([]lineRange, 0, len(offsets))
	for _, off := range offsets {
		from := lineNumberAt(text, off)
		end := off + length
		if end > off && end <= len(text) && text[end-1] == '\n' {
			end--
		}
		to := lineNumberAt(text, end)
		out = append(out, lineRange{from: from, to: to})
	}
	return out
}

func lineNumberAt(text string, offset int) int {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	return strings.Count(text[:offset], "\n") + 1
}

func formatLineRanges(ranges []lineRange) string {
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if r.from == r.to {
			parts = append(parts, fmt.Sprintf("line %d", r.from))
		} else {
			parts = append(parts, fmt.Sprintf("lines %d...%d", r.from, r.to))
		}
	}
	return strings.Join(parts, ", ")
}
