package display

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/tools"
)

func (m TuiModel) renderToolBlock(idx int, b block) string {
	barStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // orange bar
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	if b.failed {
		barStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
		labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
		textStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	}
	bar := barStyle.Render("┃")
	toolLabel := labelStyle.Render("tool")

	line, ok := m.toolDisplayCache[idx]
	if !ok {
		line = formatToolCall(b.toolName, b.content)
		m.toolDisplayCache[idx] = line
	}

	if b.toolState == "running" {
		line += " ⟳"
		// Advertised while running too: the modal shows the tool's live
		// output, which is the moment it is most worth opening.
		line += " " + hintStyle.Render("- click for progress")
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
	// Skills has a different default: no args means "list"
	if toolName == "skills" {
		if rawJSON == "" {
			return "skills(list)"
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(rawJSON), &args); err != nil {
			return "skills(list)"
		}
		name, ok := args["name"].(string)
		if ok && name != "" {
			return "skills(" + name + ")"
		}
		return "skills(list)"
	}

	if rawJSON == "" {
		return toolName + "(...)"
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &args); err != nil {
		return toolName + "(...)"
	}

	switch toolName {
	case "find":
		if pattern := formatArg(args["pattern"]); pattern != "" {
			if method, ok := args["method"].(string); ok && method != "" {
				return "find(" + method + ", " + truncateString(pattern, 60) + ")"
			}
			return "find(" + truncateString(pattern, 60) + ")"
		}
	case "todo":
		if action, ok := args["action"].(string); ok && action != "" {
			// Surface the matching item for any action that targets a single
			// todo by id (doing/done/blocked/update/remove). Without the
			// disambiguation "todo(update)" / "todo(done)" is opaque in the
			// transcript — show id plus the resolved content.
			id, _ := args["id"].(float64)
			if id > 0 && targetsSingleItem(action) {
				if content := lookupTodoContent(int(id)); content != "" {
					return "todo(" + action + ", " + fmt.Sprintf("%d", int(id)) + ". " + truncateString(content, 40) + ")"
				}
				return "todo(" + action + ", " + fmt.Sprintf("%d", int(id)) + ")"
			}
			if content, ok := args["content"].(string); ok && content != "" {
				return "todo(" + action + ": " + truncateString(content, 50) + ")"
			}
			return "todo(" + action + ")"
		}
	case "read", "write":
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
	case "lua":
		// A script is code, so there is nothing in the arguments a person can
		// read at a glance — "lua(...)" was every script in the transcript
		// looking identical. The model supplies a description, as it does for
		// bash; failing that, the script's first line of actual code is a
		// better guess than nothing.
		if desc, ok := args["description"].(string); ok && desc != "" {
			return "lua(" + truncateString(desc, 60) + ")"
		}
		if script, ok := args["script"].(string); ok && script != "" {
			return "lua(" + truncateString(firstCodeLine(script), 60) + ")"
		}
	case "memory":
		// The arguments already say everything: which action, and on which
		// note. No new parameter needed, just stop hiding them.
		action, _ := args["action"].(string)
		if action == "" {
			action = "list"
		}
		if name, ok := args["name"].(string); ok && name != "" {
			return "memory(" + action + ", " + truncateString(name, 40) + ")"
		}
		return "memory(" + action + ")"
	case "cron":
		// Same reasoning as memory: which action, on which job.
		action, _ := args["action"].(string)
		if action == "" {
			action = "list"
		}
		if name, ok := args["name"].(string); ok && name != "" {
			return "cron(" + action + ", " + truncateString(name, 40) + ")"
		}
		return "cron(" + action + ")"
	case "help":
		if name, ok := args["tool"].(string); ok && name != "" {
			return "help(" + name + ")"
		}
		return "help(list)"

	case "subagent":
		title := subagentTitleFromArgs(args)
		async, _ := args["async"].(bool)
		switch {
		case title != "" && async:
			return "subagent(async, " + truncateString(title, 60) + ")"
		case title != "":
			return "subagent(" + truncateString(title, 60) + ")"
		}
	case "wait":
		seconds := ""
		if s, ok := args["seconds"].(float64); ok {
			seconds = fmt.Sprintf("%d", int(s))
		}
		jobID, _ := args["job_id"].(string)
		note, _ := args["note"].(string)
		switch {
		case jobID != "" && note != "":
			return "wait(job=" + jobID + ", " + truncateString(note, 40) + ")"
		case jobID != "":
			return "wait(job=" + jobID + ", " + seconds + "s)"
		case note != "":
			return "wait(" + seconds + "s, " + truncateString(note, 50) + ")"
		case seconds != "":
			return "wait(" + seconds + "s)"
		}
	case "lock":
		if path, ok := args["path"].(string); ok && path != "" {
			return "lock(" + truncateString(path, 60) + ")"
		}
	case "unlock":
		if path, ok := args["path"].(string); ok && path != "" {
			return "unlock(" + truncateString(path, 60) + ")"
		}
	case "web":
		method, _ := args["method"].(string)
		what, _ := args["what"].(string)
		if method != "" && what != "" {
			return "web(" + method + ", " + truncateString(what, 60) + ")"
		}
		if method != "" {
			return "web(" + method + ")"
		}
	}

	return toolName + "(...)"
}

func subagentTitleFromArgs(args map[string]any) string {
	if task, ok := args["task"].(string); ok && task != "" {
		return task
	}
	if tasks, ok := args["tasks"].([]any); ok && len(tasks) > 0 {
		if first, ok := tasks[0].(map[string]any); ok {
			if task, ok := first["task"].(string); ok && task != "" {
				if len(tasks) == 1 {
					return task
				}
				return fmt.Sprintf("%s +%d", task, len(tasks)-1)
			}
		}
		return fmt.Sprintf("%d tasks", len(tasks))
	}
	return ""
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

// lookupTodoContent returns the content of a todo item by id, or "".
// Used by formatToolCall to enrich todo(doing/done/blocked, N) renders with
// what the item actually says, so the transcript is self-explanatory.
func lookupTodoContent(id int) string {
	for _, item := range tools.AllTodoItems() {
		if item.ID == id {
			return strings.TrimSpace(item.Content)
		}
	}
	return ""
}

// targetsSingleItem reports whether the todo action operates on a single
// todo identified by id (used to decide whether to surface the id+content
// in the TUI render).
func targetsSingleItem(action string) bool {
	switch action {
	case "doing", "done", "blocked", "update", "remove":
		return true
	}
	return false
}

// firstCodeLine is the fallback label for a lua script with no description:
// its first line that is neither blank nor a comment. Comments are skipped
// because a script that opens with "-- read every handler" would otherwise be
// labelled with the comment marker rather than what it does.
func firstCodeLine(script string) string {
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		return line
	}
	return "script"
}
