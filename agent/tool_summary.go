package agent

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/crazy-goat/tyci-agent/connector"
)

const summaryArgRunes = 80

// summaryArgKeys are the argument names that best describe a tool call.
var summaryArgKeys = []string{"command", "path", "file_path", "pattern", "query", "url", "task", "description"}

// lastToolSummary describes the newest tool call in msgs as "name: main
// argument" (first 80 runes, no file contents), for example "bash: go test
// ./...". It returns "" when msgs holds no tool call.
func lastToolSummary(msgs []connector.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		blocks := msgs[i].Content
		for j := len(blocks) - 1; j >= 0; j-- {
			if blocks[j].Type == "toolCall" {
				return blocks[j].Name + mainArgument(blocks[j].Arguments)
			}
		}
	}
	return ""
}

func mainArgument(raw json.RawMessage) string {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return ""
	}
	keys := append([]string(nil), summaryArgKeys...)
	rest := make([]string, 0, len(args))
	for k := range args {
		rest = append(rest, k)
	}
	sort.Strings(rest)
	for _, k := range append(keys, rest...) {
		if v, ok := args[k].(string); ok && v != "" {
			r := []rune(strings.Join(strings.Fields(v), " "))
			if len(r) > summaryArgRunes {
				r = r[:summaryArgRunes]
			}
			return ": " + string(r)
		}
	}
	return ""
}
