package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type GrepTool struct{}

func (t *GrepTool) Name() string { return "grep" }

func (t *GrepTool) Run(ctx context.Context, input map[string]any) ToolResult {
	pattern, ok := input["pattern"].(string)
	if !ok || pattern == "" {
		return ToolResult{Type: "result", Success: false, Error: "pattern required"}
	}
	cwd := stringParam(input, "cwd", ".")
	includes := stringListParam(input, "include", []string{"**/*"})
	excludes := stringListParam(input, "exclude", nil)
	mode := stringParam(input, "mode", "text")
	caseSensitive := boolParam(input, "caseSensitive", true)
	contextLines := intParam(input, "context", 0)
	if contextLines < 0 {
		contextLines = 0
	}
	limit := intParam(input, "limit", 100)
	if limit <= 0 {
		limit = 100
	}
	output := stringParam(input, "output", "lines")
	maxLineLength := intParam(input, "maxLineLength", 300)
	if maxLineLength <= 0 {
		maxLineLength = 300
	}

	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	includeMatchers, err := compileGlobMatchers(includes)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	excludeMatchers, err := compileGlobMatchers(excludes)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	matcher, err := newContentMatcher(pattern, mode, caseSensitive)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	counts := map[string]int{}
	filesSet := map[string]bool{}
	var blocks []grepBlock
	totalMatches := 0
	truncated := false

	err = filepath.WalkDir(cwdAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == cwdAbs {
			return nil
		}
		rel, err := filepath.Rel(cwdAbs, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() && matchesAny(excludeMatchers, rel) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if matchesAny(excludeMatchers, rel) || !matchesAny(includeMatchers, rel) {
			return nil
		}

		fileBlocks, n, err := grepFile(path, rel, matcher, contextLines, maxLineLength)
		if err != nil || n == 0 {
			return nil
		}
		counts[rel] = n
		filesSet[rel] = true
		totalMatches += n
		if output == "lines" {
			for _, block := range fileBlocks {
				if len(blocks) >= limit {
					truncated = true
					return filepath.SkipAll
				}
				blocks = append(blocks, block)
			}
		} else if len(filesSet) >= limit && output == "files" {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	var b strings.Builder
	switch output {
	case "files":
		files := sortedKeysBool(filesSet)
		shown := minInt(len(files), limit)
		fmt.Fprintf(&b, "Found %d matches in %d files", totalMatches, len(files))
		if truncated || len(files) > shown {
			fmt.Fprintf(&b, " (showing %d)", shown)
		}
		b.WriteString(":")
		for _, f := range files[:shown] {
			b.WriteByte('\n')
			b.WriteString(f)
		}
	case "count":
		files := sortedKeysInt(counts)
		fmt.Fprintf(&b, "Found %d matches in %d files:", totalMatches, len(files))
		for _, f := range files {
			fmt.Fprintf(&b, "\n%s: %d", f, counts[f])
		}
	case "lines":
		fallthrough
	default:
		fmt.Fprintf(&b, "Found %d matches in %d files", totalMatches, len(counts))
		if truncated {
			fmt.Fprintf(&b, " (limit %d reached)", limit)
		}
		b.WriteString(":")
		for _, block := range blocks {
			if contextLines > 0 {
				fmt.Fprintf(&b, "\n%s:%d-%d:", block.file, block.start, block.end)
				for i, line := range block.lines {
					fmt.Fprintf(&b, "\n%d| %s", block.start+i, line)
				}
			} else {
				fmt.Fprintf(&b, "\n%s:%d: %s", block.file, block.line, block.text)
			}
		}
	}
	return ToolResult{Type: "result", Success: true, Content: b.String()}
}

type contentMatcher struct {
	mode          string
	pattern       string
	caseSensitive bool
	re            *regexp.Regexp
}

func newContentMatcher(pattern, mode string, caseSensitive bool) (*contentMatcher, error) {
	m := &contentMatcher{mode: mode, pattern: pattern, caseSensitive: caseSensitive}
	if mode == "regex" {
		rePattern := pattern
		if !caseSensitive {
			rePattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(rePattern)
		if err != nil {
			return nil, err
		}
		m.re = re
		return m, nil
	}
	if !caseSensitive {
		m.pattern = strings.ToLower(pattern)
	}
	if mode != "text" && mode != "word" {
		return nil, fmt.Errorf("invalid mode %q", mode)
	}
	return m, nil
}

func (m *contentMatcher) Match(line string) bool {
	switch m.mode {
	case "regex":
		return m.re.MatchString(line)
	case "word":
		needle := m.pattern
		hay := line
		if !m.caseSensitive {
			hay = strings.ToLower(line)
		}
		idx := strings.Index(hay, needle)
		for idx >= 0 {
			beforeOK := idx == 0 || !isWordRune(runeAtBefore(hay, idx))
			after := idx + len(needle)
			afterOK := after >= len(hay) || !isWordRune(runeAt(hay, after))
			if beforeOK && afterOK {
				return true
			}
			next := strings.Index(hay[idx+len(needle):], needle)
			if next < 0 {
				return false
			}
			idx += len(needle) + next
		}
		return false
	default:
		if m.caseSensitive {
			return strings.Contains(line, m.pattern)
		}
		return strings.Contains(strings.ToLower(line), m.pattern)
	}
}

type grepBlock struct {
	file       string
	line       int
	text       string
	start, end int
	lines      []string
}

func grepFile(path, rel string, matcher *contentMatcher, contextLines, maxLineLength int) ([]grepBlock, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(data) > 0 && !utf8.Valid(data) {
		return nil, 0, nil
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}

	var matchLines []int
	for i, line := range lines {
		if matcher.Match(line) {
			matchLines = append(matchLines, i+1)
		}
	}
	if len(matchLines) == 0 {
		return nil, 0, nil
	}

	var blocks []grepBlock
	if contextLines == 0 {
		for _, ln := range matchLines {
			blocks = append(blocks, grepBlock{file: rel, line: ln, text: truncateLine(lines[ln-1], maxLineLength)})
		}
		return blocks, len(matchLines), nil
	}
	for _, ln := range matchLines {
		start := maxInt(1, ln-contextLines)
		end := minInt(len(lines), ln+contextLines)
		ctx := make([]string, 0, end-start+1)
		for i := start; i <= end; i++ {
			ctx = append(ctx, truncateLine(lines[i-1], maxLineLength))
		}
		blocks = append(blocks, grepBlock{file: rel, line: ln, start: start, end: end, lines: ctx})
	}
	return blocks, len(matchLines), nil
}

func truncateLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return s[:max-1] + "…"
}

func sortedKeysBool(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysInt(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func isWordRune(r rune) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
}
func runeAt(s string, idx int) rune {
	if idx >= len(s) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(s[idx:])
	return r
}
func runeAtBefore(s string, idx int) rune {
	if idx <= 0 {
		return 0
	}
	r, _ := utf8.DecodeLastRuneInString(s[:idx])
	return r
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
