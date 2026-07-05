package agent

import (
	"fmt"
	"strings"

	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
)

const (
	defaultCompactionTriggerMessages = 40
	defaultCompactionTailMessages    = 12
	defaultCompactionToolChars       = 2000
)

type CompactionConfig struct {
	Enabled         bool
	TriggerMessages int
	TailMessages    int
	ToolChars       int
}

func (c CompactionConfig) normalized() CompactionConfig {
	if !c.Enabled {
		return c
	}
	if c.TriggerMessages <= 0 {
		c.TriggerMessages = defaultCompactionTriggerMessages
	}
	if c.TailMessages <= 0 {
		c.TailMessages = defaultCompactionTailMessages
	}
	if c.ToolChars <= 0 {
		c.ToolChars = defaultCompactionToolChars
	}
	return c
}

func maybeCompactHistory(msgs *[]providers.RichMessage, sess *session.Session, cfg CompactionConfig) {
	cfg = cfg.normalized()
	if !cfg.Enabled || msgs == nil || len(*msgs) <= cfg.TriggerMessages {
		return
	}
	if cfg.TailMessages >= len(*msgs) {
		return
	}
	cut := len(*msgs) - cfg.TailMessages
	cut = adjustCompactionCut(*msgs, cut)
	if cut <= 1 {
		return
	}
	older := append([]providers.RichMessage(nil), (*msgs)[:cut]...)
	tail := append([]providers.RichMessage(nil), (*msgs)[cut:]...)
	summary := buildCompactionSummary(older, cfg)
	if strings.TrimSpace(summary) == "" {
		return
	}
	summaryMsg := providers.RichMessage{
		Role: "user",
		Content: []providers.ContentBlock{{
			Type: "text",
			Text: summary,
		}},
	}
	*msgs = append([]providers.RichMessage{summaryMsg}, tail...)
	if sess != nil {
		tailStartID, dropped := session.LastNMessageMetadata(sess.Path(), cfg.TailMessages)
		tailPayload := richMessagesToSessionPayloads(tail)
		_ = sess.WriteCompaction(summaryMsg.Content[0].Text, tailStartID, tailPayload, dropped)
	}
}

func buildCompactionSummary(msgs []providers.RichMessage, cfg CompactionConfig) string {
	var b strings.Builder
	b.WriteString("<system-reminder>\n")
	b.WriteString("Conversation summary for older context compacted out of RAM. Use this as prior context together with the recent messages that follow.\n\n")
	b.WriteString("Summary of earlier conversation:\n")
	for _, msg := range msgs {
		line := summarizeMessage(msg, cfg.ToolChars)
		if line == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("</system-reminder>")
	return b.String()
}

func summarizeMessage(msg providers.RichMessage, toolChars int) string {
	var parts []string
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			text := strings.TrimSpace(block.Text)
			if text == "" {
				continue
			}
			if msg.Role == "toolResult" {
				text = truncateMiddle(text, toolChars)
				label := block.ToolName
				if label == "" {
					label = "tool"
				}
				parts = append(parts, fmt.Sprintf("tool %s result: %s", label, text))
				continue
			}
			parts = append(parts, truncateMiddle(text, toolChars))
		case "thinking":
			continue
		case "toolCall":
			args := strings.TrimSpace(string(block.Arguments))
			if args != "" {
				parts = append(parts, fmt.Sprintf("called %s with %s", block.Name, truncateMiddle(args, toolChars)))
			} else {
				parts = append(parts, fmt.Sprintf("called %s", block.Name))
			}
		case "toolResult":
			text := strings.TrimSpace(block.Text)
			if text == "" {
				continue
			}
			parts = append(parts, fmt.Sprintf("tool %s result: %s", block.ToolName, truncateMiddle(text, toolChars)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return msg.Role + ": " + strings.Join(parts, " | ")
}

func truncateMiddle(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	if max < 32 {
		return s[:max]
	}
	head := max / 2
	tail := max - head - len(" … ")
	if tail < 0 {
		tail = 0
	}
	return s[:head] + " … " + s[len(s)-tail:]
}

func adjustCompactionCut(msgs []providers.RichMessage, cut int) int {
	for cut > 1 && tailHasOrphanToolResults(msgs[cut:]) {
		cut--
	}
	return cut
}

func tailHasOrphanToolResults(msgs []providers.RichMessage) bool {
	seen := make(map[string]struct{})
	for _, msg := range msgs {
		for _, block := range msg.Content {
			if block.Type == "toolCall" && block.ID != "" {
				seen[block.ID] = struct{}{}
			}
		}
		if msg.Role != "toolResult" {
			continue
		}
		for _, block := range msg.Content {
			if block.ToolCallID == "" {
				continue
			}
			if _, ok := seen[block.ToolCallID]; !ok {
				return true
			}
		}
	}
	return false
}

func richMessagesToSessionPayloads(msgs []providers.RichMessage) []session.MessagePayload {
	out := make([]session.MessagePayload, 0, len(msgs))
	for _, msg := range msgs {
		payload := session.MessagePayload{Role: msg.Role}
		for _, block := range msg.Content {
			payload.Content = append(payload.Content, session.ContentBlock{
				Type:       block.Type,
				Text:       block.Text,
				Thinking:   block.Thinking,
				ID:         block.ID,
				Name:       block.Name,
				Arguments:  block.Arguments,
				IsError:    block.IsError,
				ToolCallID: block.ToolCallID,
				ToolName:   block.ToolName,
			})
		}
		out = append(out, payload)
	}
	return out
}
