package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

type WriteTool struct{}

func (t *WriteTool) Name() string {
	return "write"
}

func (t *WriteTool) Run(ctx context.Context, input map[string]any) ToolResult {
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return ToolResult{Type: "result", Success: false, Error: "path required"}
	}
	// Resolved before anything else so the freshness stamps, the hooks and the
	// write itself all agree on which file this is. See tools/workdir.go.
	path = resolvePath(ctx, path)

	// Detect mode: if oldString is present, it's edit mode
	if oldStr, hasOld := input["oldString"].(string); hasOld && oldStr != "" {
		return t.runEditMode(ctx, input, path, oldStr)
	}

	// Otherwise it's write mode
	content, ok := input["content"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "content required (or use oldString+newString for edit mode)"}
	}

	return t.runWriteMode(ctx, input, path, content)
}

// ---------------------------------------------------------------------------
// Write mode
// ---------------------------------------------------------------------------

func (t *WriteTool) runWriteMode(ctx context.Context, input map[string]any, path, content string) ToolResult {
	r, err := parseWriteRange(input["range"])
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	// Append is purely additive and addresses no line numbers, so it cannot
	// discard anything the agent has not read. Every other mode either
	// replaces the whole file or edits by line number, and both are only
	// correct against the bytes the agent actually saw.
	if r.mode != "append" {
		if err := checkFileFresh(path); err != nil {
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}
	}

	switch r.mode {
	case "append":
		return appendFile(path, content)
	case "all":
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}
		recordFileStamp(path)
		return ToolResult{Type: "result", Success: true, Content: "written " + path}
	case "before", "after", "lines":
		data, err := os.ReadFile(path)
		if err != nil {
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}
		lines, origTrailing := splitFileLines(string(data))
		if r.from < 1 || r.from > len(lines) || (r.mode == "lines" && (r.to < r.from || r.to > len(lines))) {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("range out of bounds (file has %d lines)", len(lines))}
		}
		repl, replTrailing := splitReplacementLines(content)
		var newLines []string
		switch r.mode {
		case "before":
			newLines = make([]string, 0, len(lines)+len(repl))
			newLines = append(newLines, lines[:r.from-1]...)
			newLines = append(newLines, repl...)
			newLines = append(newLines, lines[r.from-1:]...)
		case "after":
			newLines = make([]string, 0, len(lines)+len(repl))
			newLines = append(newLines, lines[:r.from]...)
			newLines = append(newLines, repl...)
			newLines = append(newLines, lines[r.from:]...)
		default:
			newLines = make([]string, 0, len(lines)-(r.to-r.from+1)+len(repl))
			newLines = append(newLines, lines[:r.from-1]...)
			newLines = append(newLines, repl...)
			newLines = append(newLines, lines[r.to:]...)
		}

		out := strings.Join(newLines, "\n")
		trailing := origTrailing
		if r.mode == "lines" && r.to >= len(lines) {
			trailing = replTrailing
		}
		if trailing && out != "" {
			out += "\n"
		}
		if err := os.WriteFile(path, []byte(out), 0644); err != nil {
			forgetFileStamp(path)
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}
		recordFileStamp(path)
		switch r.mode {
		case "before":
			return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("inserted before line %d in %s", r.from, path)}
		case "after":
			return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("inserted after line %d in %s", r.from, path)}
		case "lines":
			if r.from == r.to {
				return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("replaced line %d in %s", r.from, path)}
			}
			return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("replaced lines %d...%d in %s", r.from, r.to, path)}
		}
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
		forgetFileStamp(path)
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	// Refresh an existing stamp rather than creating one: appending after a
	// read leaves the agent's knowledge current (it knows the old bytes and
	// what it just added), but appending to a file it never read must not
	// earn it permission to overwrite that file wholesale later.
	refreshFileStampIfKnown(path)
	return ToolResult{Type: "result", Success: true, Content: "appended to " + path}
}

// ---------------------------------------------------------------------------
// Edit mode (absorbed from edit.go)
// ---------------------------------------------------------------------------

func (t *WriteTool) runEditMode(ctx context.Context, input map[string]any, path, oldStr string) ToolResult {
	newStr, ok := input["newString"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "newString required when oldString is provided"}
	}
	dryRun := boolParam(input, "dryRun", false)

	// Guarded even for a dry run: the offsets and line numbers it reports
	// would describe a file the agent has not seen (see tools/filestamp.go).
	if err := checkFileFresh(path); err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

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
		forgetFileStamp(path)
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	recordFileStamp(path)
	return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("replaced %d occurrence(s) in %s at %s", len(selected), path, formatLineRanges(lineRanges))}
}

// ---------------------------------------------------------------------------
// Shared helpers (from edit.go, now part of write.go)
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Range helpers (write mode)
// ---------------------------------------------------------------------------

type writeRange struct {
	mode     string // all, append, lines, before, after
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
		if strings.HasPrefix(sl, "before:") || strings.HasPrefix(sl, "before ") {
			n, err := strconv.Atoi(strings.TrimSpace(s[strings.IndexAny(s, ": ")+1:]))
			if err != nil || n < 1 {
				return writeRange{}, fmt.Errorf("invalid before range %q", s)
			}
			return writeRange{mode: "before", from: n, to: n}, nil
		}
		if strings.HasPrefix(sl, "after:") || strings.HasPrefix(sl, "after ") {
			n, err := strconv.Atoi(strings.TrimSpace(s[strings.IndexAny(s, ": ")+1:]))
			if err != nil || n < 1 {
				return writeRange{}, fmt.Errorf("invalid after range %q", s)
			}
			return writeRange{mode: "after", from: n, to: n}, nil
		}
		if strings.HasSuffix(s, "^") {
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, "^")))
			if err != nil || n < 1 {
				return writeRange{}, fmt.Errorf("invalid before range %q", s)
			}
			return writeRange{mode: "before", from: n, to: n}, nil
		}
		if strings.HasSuffix(s, "+") {
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, "+")))
			if err != nil || n < 1 {
				return writeRange{}, fmt.Errorf("invalid after range %q", s)
			}
			return writeRange{mode: "after", from: n, to: n}, nil
		}
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
	return writeRange{}, fmt.Errorf("invalid range; use line number, from...to, before:N, after:N, all, or -1/append")
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
