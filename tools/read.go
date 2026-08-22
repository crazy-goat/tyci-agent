package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// DefaultMaxLines is the max lines to return when no explicit limit is given.
	DefaultMaxLines = 2000
	// DefaultMaxBytes is the max bytes to return when no explicit limit is given.
	DefaultMaxBytes = 50 * 1024 // 50KB
	// HardMaxBytes is the absolute cap – we never read more than this from disk.
	HardMaxBytes = 256 * 1024 // 256KB
)

// truncationResult holds info about truncation applied to the output.
type truncationResult struct {
	content          string
	truncated        bool
	truncatedBy      string // "lines", "bytes", or ""
	totalLines       int
	totalBytes       int
	outputLines      int
	firstLineExceeds bool
}

// truncateHead keeps first N complete lines that fit within limits.
func truncateHead(text string, maxLines, maxBytes int) truncationResult {
	totalBytes := len(text)
	lines := strings.Split(text, "\n")
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return truncationResult{
			content:     text,
			totalLines:  totalLines,
			totalBytes:  totalBytes,
			outputLines: totalLines,
		}
	}

	// Check if first line alone exceeds byte limit
	if len(lines[0]) > maxBytes {
		return truncationResult{
			truncated:        true,
			truncatedBy:      "bytes",
			totalLines:       totalLines,
			totalBytes:       totalBytes,
			outputLines:      0,
			firstLineExceeds: true,
		}
	}

	var out []string
	outBytes := 0
	truncatedBy := "lines"

	for i, line := range lines {
		if i >= maxLines {
			truncatedBy = "lines"
			break
		}
		lineBytes := len(line)
		if i > 0 {
			lineBytes++ // +1 for newline
		}
		if outBytes+lineBytes > maxBytes {
			truncatedBy = "bytes"
			break
		}
		out = append(out, line)
		outBytes += lineBytes
	}

	content := strings.Join(out, "\n")
	return truncationResult{
		content:     content,
		truncated:   len(out) < totalLines || outBytes < totalBytes,
		truncatedBy: truncatedBy,
		totalLines:  totalLines,
		totalBytes:  totalBytes,
		outputLines: len(out),
	}
}

type ReadTool struct{}

func (t *ReadTool) Name() string {
	return "read"
}

func (t *ReadTool) Run(ctx context.Context, input map[string]any) ToolResult {
	path, ok := input["path"].(string)
	// Resolved against this call's working directory, which for a child agent
	// in its own worktree is not the process's. See tools/workdir.go.
	path = resolvePath(ctx, path)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "path required"}
	}

	offset := intParam(input, "offset", 0)
	limit := intParam(input, "limit", 0)
	lineNumbers := boolParam(input, "lineNumbers", false)

	// Check if path exists and if it's a directory (use Lstat to avoid following symlinks)
	info, err := os.Lstat(path)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	// Handle directory listing
	if info.IsDir() {
		return listDirectory(path)
	}

	// If it's a symlink, check if it points to a directory
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return ToolResult{Type: "result", Success: false, Error: err.Error()}
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		targetInfo, err := os.Stat(target)
		if err == nil && targetInfo.IsDir() {
			return listDirectory(path)
		}
	}

	// Read the whole file so continuation hints are accurate. Output is still capped below.
	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	// Remember what we saw, so a later write to this path can tell whether
	// the file still looks the way the agent thinks it does. See
	// tools/filestamp.go. Recorded even for a partial (offset/limit) read:
	// the guard's question is "did the file change under us", and that is
	// answerable from the whole-file state we just loaded regardless of how
	// much of it we return.
	recordFileStamp(path)

	text := string(data)
	lines := strings.Split(text, "\n")
	totalFileLines := len(lines)

	// offset jest 1-indeksowany (linie), zamień na 0-indeksowany
	startLine := 0
	if offset > 0 {
		startLine = offset - 1
	}
	if startLine >= totalFileLines {
		return ToolResult{Type: "result", Success: false,
			Error: fmt.Sprintf("offset %d is beyond end of file (%d lines total)", offset, totalFileLines)}
	}

	// Weź fragment od startLine
	var selectedText string
	var userLimited bool
	if limit > 0 {
		endLine := startLine + limit
		if endLine > totalFileLines {
			endLine = totalFileLines
		}
		selectedText = strings.Join(lines[startLine:endLine], "\n")
		userLimited = endLine < totalFileLines
	} else {
		selectedText = strings.Join(lines[startLine:], "\n")
	}

	// Apply output caps. If user set limit, respect it by lines but still cap bytes.
	maxLines := DefaultMaxLines
	maxBytes := DefaultMaxBytes
	if limit > 0 {
		maxLines = limit
	}
	tr := truncateHead(selectedText, maxLines, maxBytes)

	startLineDisplay := startLine + 1

	// If the first selected line alone exceeds byte cap, return a clear continuation hint.
	if tr.firstLineExceeds {
		firstLineSize := len(lines[startLine])
		msg := fmt.Sprintf("[Line %d is %dB, exceeds %dKB output limit. No partial line returned. Use bash or read a later offset after this long line.]",
			startLineDisplay, firstLineSize, maxBytes/1024)
		return ToolResult{Type: "result", Success: true, Content: msg}
	}

	content := tr.content
	if lineNumbers && content != "" {
		content = addLineNumbers(content, startLineDisplay)
	}

	// Always say how to continue when returned output is incomplete.
	endLineDisplay := startLineDisplay + tr.outputLines - 1
	if tr.outputLines == 0 {
		endLineDisplay = startLineDisplay
	}
	if tr.truncated {
		nextOffset := endLineDisplay + 1
		if nextOffset <= startLineDisplay {
			nextOffset = startLineDisplay + 1
		}
		if tr.truncatedBy == "lines" {
			content += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. More content available. Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, nextOffset)
		} else {
			content += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Output hit %dKB limit. More content available. Use offset=%d to continue.]",
				startLineDisplay, endLineDisplay, totalFileLines, maxBytes/1024, nextOffset)
		}
	} else if userLimited {
		remaining := totalFileLines - (startLine + limit)
		nextOffset := startLine + limit + 1
		content += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. %d more lines available. Use offset=%d to continue.]",
			startLineDisplay, startLine+limit, totalFileLines, remaining, nextOffset)
	}

	return ToolResult{Type: "result", Success: true, Content: content}
}

func addLineNumbers(content string, start int) string {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d| %s", start+i, line)
	}
	return b.String()
}

func listDirectory(path string) ToolResult {
	entries, err := os.ReadDir(path)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n", path)
	for _, e := range entries {
		if e.IsDir() {
			fmt.Fprintf(&b, "  %s/\n", e.Name())
		} else {
			fmt.Fprintf(&b, "  %s\n", e.Name())
		}
	}
	return ToolResult{Type: "result", Success: true, Content: b.String()}
}
