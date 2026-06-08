package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type GlobTool struct{}

func (t *GlobTool) Name() string { return "glob" }

func (t *GlobTool) Run(ctx context.Context, input map[string]any) ToolResult {
	patterns := stringListParam(input, "pattern", nil)
	if len(patterns) == 0 {
		return ToolResult{Type: "result", Success: false, Error: "pattern required"}
	}
	cwd := stringParam(input, "cwd", ".")
	excludes := defaultExcludes(input)
	limit := intParam(input, "limit", 500)
	if limit <= 0 {
		limit = 500
	}
	includeDirs := boolParam(input, "includeDirs", false)
	absolute := boolParam(input, "absolute", false)

	cwdAbs, err := filepath.Abs(cwd)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	matchers, err := compileGlobMatchers(patterns)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	excludeMatchers, err := compileGlobMatchers(excludes)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	var results []string
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
		if matchesAny(excludeMatchers, rel) {
			return nil
		}
		if d.IsDir() && !includeDirs {
			return nil
		}
		if !matchesAny(matchers, rel) {
			return nil
		}

		out := rel
		if absolute {
			out = path
		}
		results = append(results, out)
		if len(results) >= limit {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	sort.Strings(results)

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d paths", len(results))
	if truncated {
		fmt.Fprintf(&b, " (limit %d reached)", limit)
	}
	b.WriteString(":")
	for _, p := range results {
		b.WriteByte('\n')
		b.WriteString(p)
	}
	return ToolResult{Type: "result", Success: true, Content: b.String()}
}

type globMatcher struct{ re *regexp.Regexp }

func compileGlobMatchers(patterns []string) ([]globMatcher, error) {
	var out []globMatcher
	for _, p := range patterns {
		if p == "" {
			continue
		}
		for _, expanded := range expandBraces(filepath.ToSlash(p)) {
			re, err := regexp.Compile(globToRegex(expanded))
			if err != nil {
				return nil, fmt.Errorf("invalid glob %q: %w", p, err)
			}
			out = append(out, globMatcher{re: re})
		}
	}
	return out, nil
}

func matchesAny(matchers []globMatcher, path string) bool {
	if len(matchers) == 0 {
		return false
	}
	path = filepath.ToSlash(path)
	for _, m := range matchers {
		if m.re.MatchString(path) {
			return true
		}
	}
	return false
}

func globToRegex(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 3
				} else {
					b.WriteString(".*")
					i += 2
				}
			} else {
				b.WriteString("[^/]*")
				i++
			}
		case '?':
			b.WriteString("[^/]")
			i++
		case '.', '+', '(', ')', '|', '^', '$', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	b.WriteString("$")
	return b.String()
}

func expandBraces(pattern string) []string {
	start := strings.IndexByte(pattern, '{')
	if start < 0 {
		return []string{pattern}
	}
	end := strings.IndexByte(pattern[start:], '}')
	if end < 0 {
		return []string{pattern}
	}
	end += start
	prefix, suffix := pattern[:start], pattern[end+1:]
	parts := strings.Split(pattern[start+1:end], ",")
	var out []string
	for _, part := range parts {
		for _, rest := range expandBraces(suffix) {
			out = append(out, prefix+part+rest)
		}
	}
	return out
}

func stringParam(input map[string]any, key, def string) string {
	if v, ok := input[key].(string); ok {
		return v
	}
	return def
}

func boolParam(input map[string]any, key string, def bool) bool {
	val, ok := input[key]
	if !ok {
		return def
	}
	switch v := val.(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	case float64:
		return v != 0
	case int:
		return v != 0
	}
	return def
}

func stringListParam(input map[string]any, key string, def []string) []string {
	val, ok := input[key]
	if !ok || val == nil {
		return def
	}
	switch v := val.(type) {
	case string:
		if v == "" {
			return def
		}
		return []string{v}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return def
		}
		return out
	}
	return def
}
