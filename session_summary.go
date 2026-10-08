package main

import (
	"strconv"
	"strings"

	"github.com/crazy-goat/tyci-agent/display"
	"github.com/crazy-goat/tyci-agent/providers"
	"github.com/crazy-goat/tyci-agent/session"
)

// summarizeResume renders a compact "loaded session …" info block on the
// active display. The conversation array itself is loaded elsewhere by
// session.RebuildMessages so the model still has the full history; this
// function intentionally does NOT shovel every event through
// Display.Text / Display.Thinking — doing so wrecks the TUI on long
// sessions: blocks scroll off-screen, glamour renders thousands of
// lines, selection rewrites the screen with stale ANSI, and PgUp/PgDown
// become useless because there is nothing left to overflow into.
//
// Output shape: a single ToolBlock line that the user can read at a glance:
//
//	"📋 Resumed session a1b2… (42 messages, 12345 in / 6789 out tokens).
//	 Last user: <first 80 chars>. Last assistant: <first 80 chars>."
func summarizeResume(disp display.Display, sessID string, msgs []providers.RichMessage, total session.TotalUsage, corruptCount int) {
	if disp == nil {
		return
	}

	// Walk the conversation backwards to find the last user message and
	// the last assistant message with text (ignore huge thinking blocks
	// for the snippet — the model can still see them in `msgs`).
	var lastUser, lastAssistantText string
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		switch m.Role {
		case "user":
			if lastUser == "" {
				for _, b := range m.Content {
					if b.Type == "text" && b.Text != "" {
						lastUser = b.Text
					}
				}
			}
		case "assistant":
			if lastAssistantText == "" {
				for _, b := range m.Content {
					if b.Type == "text" && b.Text != "" {
						lastAssistantText = b.Text
					}
				}
			}
		}
		if lastUser != "" && lastAssistantText != "" {
			break
		}
	}

	disp.ToolBlock(buildResumeSummary(sessID, len(msgs), total, lastUser, lastAssistantText, corruptCount))
	disp.End()
}

// buildResumeSummary formats a single info line for summarizeResume. Kept
// separate so callers can also format the line into stderr/logs without
// routing through the Display interface (used by tests and one-shot prompt
// mode where the user does want to see a copy on stderr too).
func buildResumeSummary(sessID string, msgCount int, total session.TotalUsage, lastUser, lastAssistant string, corruptCount int) string {
	var b strings.Builder
	b.WriteString("📋 Resumed session ")
	b.WriteString(sessID)
	b.WriteString(" (")
	b.WriteString(strconv.Itoa(msgCount))
	b.WriteString(" messages, ")
	b.WriteString(strconv.Itoa(total.Input))
	b.WriteString(" in / ")
	b.WriteString(strconv.Itoa(total.Output))
	b.WriteString(" out tokens")
	if total.TotalCost > 0 {
		b.WriteString(", $")
		b.WriteString(strconv.FormatFloat(total.TotalCost, 'f', 4, 64))
		b.WriteString(" total")
	}
	if corruptCount > 0 {
		b.WriteString(", ")
		b.WriteString(strconv.Itoa(corruptCount))
		b.WriteString(" corrupt lines skipped")
	}
	b.WriteString(")")
	if lastUser != "" {
		b.WriteString("\nLast user: ")
		b.WriteString(truncateForSummary(lastUser, 80))
	}
	if lastAssistant != "" {
		b.WriteString("\nLast assistant: ")
		b.WriteString(truncateForSummary(lastAssistant, 80))
	}
	b.WriteString("\n▶ Continuing from session end")
	return b.String()
}

// truncateForSummary returns s with at most maxLen runes, appending "…"
// when truncated. Used to keep the resume summary readable in one or two
// screen lines regardless of how chatty the last turn was.
func truncateForSummary(s string, maxLen int) string {
	if maxLen <= 0 || s == "" {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}
