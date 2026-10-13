package display

import (
	"strings"
	"unicode/utf8"

	"github.com/crazy-goat/tyci-agent/connector"
)

// Length limits of one line of an agent log. A thinking block is cut shorter
// than a text note: the note is what the agent says to the reader.
const (
	agentLogThinkingRunes = 100
	agentLogTextRunes     = 200
	agentLogArgsRunes     = 100
)

// formatAgentLogLines renders an agent log with one line for each thinking
// block, each tool call and each text block. A tool call line ends with the
// status of its result: "ok", "err", or "…" while the result is missing. The
// bodies of tool results are never shown. A text block of the operator (the
// user role) starts with "operator: ".
func formatAgentLogLines(msgs []connector.Message) []string {
	status := map[string]string{}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type != "toolResult" || b.ToolCallID == "" {
				continue
			}
			status[b.ToolCallID] = "ok"
			if b.IsError {
				status[b.ToolCallID] = "err"
			}
		}
	}

	var lines []string
	for _, m := range msgs {
		for _, b := range m.Content {
			switch b.Type {
			case "thinking":
				if text := oneLine(b.Thinking, agentLogThinkingRunes); text != "" {
					lines = append(lines, "thinking: "+text)
				}
			case "toolCall":
				st, ok := status[b.ID]
				if !ok {
					st = "…"
				}
				call := oneLine(formatToolSummary(b.Name, string(b.Arguments)), agentLogArgsRunes)
				lines = append(lines, "tool "+call+"  "+st)
			case "text":
				text := oneLine(b.Text, agentLogTextRunes)
				if text == "" {
					continue
				}
				if m.Role == "user" {
					text = "operator: " + text
				}
				lines = append(lines, text)
			}
		}
	}
	return lines
}

// oneLine returns s as one terminal line of at most limit runes. Escape
// sequences and control characters are removed first (sanitizeUntrusted), then
// every run of white space, newlines included, becomes one space. A cut line
// ends with "…".
func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(sanitizeUntrusted(s)), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit]) + "…"
}
