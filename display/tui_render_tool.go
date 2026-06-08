package display

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func (m TuiModel) renderToolBlock(idx int, b block) string {
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("┃") // orange bar
	toolLabel := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true).Render("tool")
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	line, ok := m.toolDisplayCache[idx]
	if !ok {
		line = formatToolCall(b.toolName, b.content)
		m.toolDisplayCache[idx] = line
	}

	if b.toolState == "running" {
		line += " ⟳"
	} else if b.toolState == "done" {
		dur := b.duration
		if dur == 0 {
			dur = time.Since(b.startTime) // fallback, shouldn't happen
		}
		line += " " + formatDuration(dur)
		line += " " + hintStyle.Render("- click to display")
	}

	return bar + " " + toolLabel + " " + textStyle.Render(line)
}

// formatToolCall parses the raw JSON tool arguments and returns a human-readable
// summary like "read(main.go)" or "bash(Build display package)".
func formatToolCall(toolName, rawJSON string) string {
	if rawJSON == "" {
		return toolName + "(...)"
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &args); err != nil {
		return toolName + "(...)"
	}

	switch toolName {
	case "glob":
		if pattern := formatArg(args["pattern"]); pattern != "" {
			return "glob(" + truncateString(pattern, 60) + ")"
		}
	case "grep":
		if pattern := formatArg(args["pattern"]); pattern != "" {
			return "grep(" + truncateString(pattern, 60) + ")"
		}
	case "todo":
		if action, ok := args["action"].(string); ok && action != "" {
			if content, ok := args["content"].(string); ok && content != "" {
				return "todo(" + action + ": " + truncateString(content, 50) + ")"
			}
			return "todo(" + action + ")"
		}
	case "read", "write", "edit":
		if path, ok := args["path"].(string); ok && path != "" {
			return toolName + "(" + path + ")"
		}
	case "bash":
		if desc, ok := args["description"].(string); ok && desc != "" {
			return "bash(" + truncateString(desc, 60) + ")"
		}
		if cmd, ok := args["command"].(string); ok && cmd != "" {
			return "bash(" + truncateString(cmd, 60) + ")"
		}
	case "subagent":
		if task, ok := args["task"].(string); ok && task != "" {
			return "subagent(" + truncateString(task, 60) + ")"
		}
	}

	return toolName + "(...)"
}

func formatArg(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, ",")
		}
	}
	return ""
}

// formatDuration returns a human-readable duration string.
// Milliseconds under 1s: "23ms". Seconds with 2 decimals: "1.23s".
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "0ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < 10*time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	default:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
}

// extractPath tries to parse JSON and get "path" value.
// Deprecated: use formatToolCall for rendering tool blocks.
func extractPath(s string) string {
	if s == "" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return ""
	}
	if p, ok := obj["path"].(string); ok {
		return p
	}
	return ""
}
