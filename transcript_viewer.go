package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/display"
)

// transcriptBlockCap caps a single block's rendered text so one huge tool
// result does not flood the viewer. Only the block itself is truncated;
// the viewer never drops whole blocks.
const transcriptBlockCap = 4000

// TranscriptProvider builds display lines for jobID from the resumable
// store. ok is false when no stashed transcript exists for that job (still
// running or never stashed). The returned title is suitable for the viewer's
// header. Lines are self-identifying and grep-friendly (one per block).
func buildTranscriptProvider() display.TranscriptProvider {
	return func(jobID string) (string, []string, bool) {
		resumableMu.Lock()
		entry, ok := resumable[jobID]
		if !ok {
			resumableMu.Unlock()
			return "", nil, false
		}
		msgs := deepCopyMessages(entry.msgs)
		// Title: prefer the job's description from JobRegistry; fall back
		// to jobID when the registry already evicted it.
		title := jobID
		if j, ok2 := JobRegistry.Get(jobID); ok2 && j != nil && j.Description != "" {
			title = fmt.Sprintf("%s — %s", jobID, truncateForTitle(j.Description))
		}
		resumableMu.Unlock()

		lines := formatTranscriptLines(msgs)
		return title, lines, true
	}
}

func truncateForTitle(s string) string {
	if len([]rune(s)) <= 60 {
		return s
	}
	return string([]rune(s)[:57]) + "..."
}

// deepCopyMessages copies msgs deeply so the caller can mutate the copy
// without aliasing the stashed entry's backing arrays (notably Arguments).
func deepCopyMessages(msgs []connector.Message) []connector.Message {
	out := make([]connector.Message, len(msgs))
	for i, m := range msgs {
		out[i].Role = m.Role
		if len(m.Content) > 0 {
			out[i].Content = append([]connector.ContentBlock(nil), m.Content...)
			for j := range out[i].Content {
				if len(m.Content[j].Arguments) > 0 {
					out[i].Content[j].Arguments = append(json.RawMessage(nil), m.Content[j].Arguments...)
				}
			}
		}
	}
	return out
}

func formatTranscriptLines(msgs []connector.Message) []string {
	var out []string
	for i, m := range msgs {
		role := m.Role
		if role == "" {
			role = "unknown"
		}
		if len(m.Content) == 0 {
			out = append(out, fmt.Sprintf("[%d] %s: (empty)", i, role))
			continue
		}
		for _, b := range m.Content {
			var line string
			switch b.Type {
			case "thinking":
				text := b.Thinking
				if text == "" {
					text = b.Text
				}
				text = stripAnsiTranscript(text)
				// Collapse newlines for single-line grep-friendliness.
				text = strings.ReplaceAll(text, "\n", " ")
				text = strings.ReplaceAll(text, "\r", "")
				if len([]rune(text)) > 120 {
					text = string([]rune(text)[:120]) + "…"
				}
				line = fmt.Sprintf("[%d] thinking: %s", i, text)
				out = append(out, line)
			case "toolCall":
				args := string(b.Arguments)
				args = stripAnsiTranscript(args)
				argsRunes := []rune(args)
				if len(argsRunes) > 2000 {
					args = string(argsRunes[:2000]) + fmt.Sprintf("…[+%d chars]", len(argsRunes)-2000)
				}
				// Keep on one line.
				args = strings.ReplaceAll(args, "\n", " ")
				line = fmt.Sprintf("[%d] tool_call %s(%s)", i, b.Name, args)
				out = append(out, line)
			case "toolResult":
				text := stripAnsiTranscript(b.Text)
				textRunes := []rune(text)
				if len(textRunes) > transcriptBlockCap {
					text = string(textRunes[:transcriptBlockCap]) + fmt.Sprintf("…[+%d chars]", len(textRunes)-transcriptBlockCap)
				}
				// Preserve internal newlines as escaped \n? No — keep verbatim
				// but flattened to one logical line per block for grep.
				text = strings.ReplaceAll(text, "\n", "\\n")
				line = fmt.Sprintf("[%d] tool_result %s", i, text)
				out = append(out, line)
			default: // "text" and any unknown type
				text := stripAnsiTranscript(b.Text)
				textRunes := []rune(text)
				if len(textRunes) > transcriptBlockCap {
					text = string(textRunes[:transcriptBlockCap]) + fmt.Sprintf("…[+%d chars]", len(textRunes)-transcriptBlockCap)
				}
				text = strings.ReplaceAll(text, "\n", "\\n")
				line = fmt.Sprintf("[%d] %s: %s", i, role, text)
				out = append(out, line)
			}
		}
	}
	return out
}

func stripAnsiTranscript(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
