package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/crazy-goat/tyci-agent/display"
)

// replaySessionToDisplay reads a JSONL session file and re-renders every
// message into the active display as a small number of stable, scannable
// blocks. It deliberately avoids Display.Text / Display.Thinking /
// Display.ToolCallStart: those paths route through glamour + streaming
// wrappers and break three things at once on long sessions:
//
//  1. PgUp/PgDown — glamour keeps mutating cachedLines/block state while
//     the user is mid-scroll, so the anchor scrolls under the cursor.
//  2. Mouse selection — selection.Y points into a renderBuffer that got
//     rebuilt under the user (glamour runs on dirty blocks between events),
//     so releasing the mouse puts highlighted cells onto rows that now
//     contain the next message's first line. The screen "blanks" because
//     the selection highlight spans ghost text that no longer exists.
//  3. Performance — a 200-message session produces 1500+ dirty blocks,
//     each running through glamour twice (once streamed, once re-rendered
//     on block boundary). CPU becomes unusable and memory balloons.
//
// Instead, every message becomes one ToolBlock (kind="block") rendered by
// renderErrorOrBlock — pure wrapText, no glamour, deterministic line
// counts. Multiple consecutive blocks are stable so scrolling is predictable
// and selection highlights stay anchored to the rows they were drawn on.
//
// To keep the transcript reasonable even on huge sessions, maxReplayBlocks
// caps the number of replayed message blocks; older messages are folded
// into a single "earlier history collapsed" info block and the full
// conversation is still loaded into the model (session.RebuildMessages).
func replaySessionToDisplay(disp display.Display, sessionPath string) {
	if disp == nil {
		return
	}
	f, err := os.Open(sessionPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot replay session: %v\n", err)
		return
	}
	defer func() { _ = f.Close() }() // read-only

	// Walk JSONL, build one formatted block per message, then push them.
	type replayEntry struct {
		role string
		body string // formatted, multi-line ready for ToolBlock
	}
	var entries []replayEntry

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		evType, _ := raw["type"].(string)
		if evType == "session" || evType == "session_end" || evType == "compaction" {
			continue
		}
		msgRaw, ok := raw["message"].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgRaw["role"].(string)
		body := formatMessageForReplay(role, msgRaw)
		if body == "" {
			continue
		}
		entries = append(entries, replayEntry{role: role, body: body})
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session replay error: %v\n", err)
	}

	// Cap the visible transcript. Older messages are still in `msgs` for
	// the model; we just don't crowd the screen.
	const maxReplayBlocks = 100
	if len(entries) > maxReplayBlocks {
		dropped := len(entries) - maxReplayBlocks
		disp.ToolBlock(fmt.Sprintf("… %d earlier messages collapsed (still loaded into model context) …", dropped))
		disp.End()
		entries = entries[len(entries)-maxReplayBlocks:]
	}

	for _, e := range entries {
		disp.ToolBlock(e.body)
		disp.End()
	}
	disp.ToolBlock("▶ Continuing from session end")
	disp.End()
}

// formatMessageForReplay turns one JSONL message event into a multi-line
// string suitable for a single display.ToolBlock (=> kind="block" =>
// renderErrorOrBlock). It collapses thinking to a single one-liner, merges
// user text fragments, and summarises tool calls / results so the user can
// scroll and select the transcript without glamour mangling every line.
func formatMessageForReplay(role string, msgRaw map[string]any) string {
	content, _ := msgRaw["content"].([]any)

	var sb strings.Builder
	switch role {
	case "user":
		sb.WriteString("[You]\n")
		for _, cb := range content {
			b, ok := cb.(map[string]any)
			if !ok {
				continue
			}
			if txt, _ := b["text"].(string); txt != "" {
				sb.WriteString(txt)
				if !strings.HasSuffix(txt, "\n") {
					sb.WriteString("\n")
				}
			}
		}

	case "assistant":
		// Order: thinking (summary), text blocks, tool call summaries.
		var thinkingBytes int
		var thinkingLines int
		var textParts []string
		var toolCalls []string
		for _, cb := range content {
			b, ok := cb.(map[string]any)
			if !ok {
				continue
			}
			bType, _ := b["type"].(string)
			switch bType {
			case "thinking":
				if txt, _ := b["thinking"].(string); txt != "" {
					thinkingBytes += len(txt)
					thinkingLines += strings.Count(txt, "\n") + 1
				}
			case "text":
				if txt, _ := b["text"].(string); txt != "" {
					textParts = append(textParts, txt)
				}
			case "toolCall":
				name, _ := b["name"].(string)
				args, _ := b["arguments"].(string)
				// Compact JSON: trim, drop trailing whitespace.
				args = strings.TrimSpace(args)
				if args == "" {
					toolCalls = append(toolCalls, fmt.Sprintf("- tool %s()", name))
				} else if len(args) > 120 {
					toolCalls = append(toolCalls, fmt.Sprintf("- tool %s(%s…)", name, args[:120]))
				} else {
					toolCalls = append(toolCalls, fmt.Sprintf("- tool %s(%s)", name, args))
				}
			}
		}
		if thinkingBytes > 0 {
			fmt.Fprintf(&sb, "[Assistant thinking: %d chars / %d lines — collapsed]\n", thinkingBytes, thinkingLines)
		}
		if len(textParts) > 0 {
			sb.WriteString("[Assistant]\n")
			for _, t := range textParts {
				sb.WriteString(t)
				if !strings.HasSuffix(t, "\n") {
					sb.WriteString("\n")
				}
			}
		}
		if len(toolCalls) > 0 {
			sb.WriteString("[Assistant tool calls]\n")
			sb.WriteString(strings.Join(toolCalls, "\n"))
			sb.WriteString("\n")
		}
		if thinkingBytes == 0 && len(textParts) == 0 && len(toolCalls) == 0 {
			return ""
		}

	case "toolResult", "tool":
		// Identify which tool produced this result, then show the result
		// text (truncated). Multiple tool results land in separate entries,
		// one each, so each is scrollable / selectable on its own.
		var toolName string
		var resultParts []string
		for _, cb := range content {
			b, ok := cb.(map[string]any)
			if !ok {
				continue
			}
			bType, _ := b["type"].(string)
			if bType != "text" {
				continue
			}
			if toolName == "" {
				toolName, _ = b["toolName"].(string)
			}
			if txt, _ := b["text"].(string); txt != "" {
				resultParts = append(resultParts, txt)
			}
		}
		if toolName == "" {
			toolName = "tool"
		}
		header := fmt.Sprintf("[Tool result: %s]\n", toolName)
		sb.WriteString(header)
		// Body lines — small-to-medium results pass through verbatim so the
		// user can see actual output. Huge results are truncated to keep
		// the transcript scrollable; the full text is still in the
		// session file for /resume-list debugging if needed.
		const maxToolResultChars = 4000
		body := strings.Join(resultParts, "\n")
		if len(body) > maxToolResultChars {
			body = body[:maxToolResultChars] + fmt.Sprintf("\n… (truncated, %d more chars)", len(body)-maxToolResultChars)
		}
		sb.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			sb.WriteString("\n")
		}

	default:
		return ""
	}

	out := sb.String()
	// Drop trailing blank lines so the block doesn't render as a one-line
	// padded spacer — keeps the message-region line counts predictable,
	// which is exactly what makes scroll math stable in the long-session
	// case.
	return strings.TrimRight(out, "\n")
}
