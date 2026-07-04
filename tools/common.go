package tools

import "strconv"

var builtInExcludes = []string{
	".git/**",
	"node_modules/**",
	"dist/**",
	"build/**",
	"coverage/**",
	"vendor/**",
	".next/**",
	".cache/**",
	"tmp/**",
	"*.lock",
}

func defaultExcludes(input map[string]any) []string {
	return stringListParam(input, "exclude", builtInExcludes)
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

func intParam(input map[string]any, key string, def int) int {
	val, ok := input[key]
	if !ok {
		return def
	}
	switch v := val.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return n
	}
	return def
}
