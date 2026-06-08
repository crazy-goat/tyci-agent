package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/decodo/tyci-agent/display"
	"github.com/decodo/tyci-agent/providers"
	"github.com/decodo/tyci-agent/stream"
)

func runOnce(ctx context.Context, p providers.Provider, d display.Display, msgs *[]providers.RichMessage, cfg Config) (more bool, usage *stream.Usage, err error) {
	events, streamErr := p.Stream(ctx, providers.Request{
		Model:    cfg.Model,
		System:   cfg.System,
		Messages: *msgs,
		Tools:    cfg.Schema,
		Debug:    cfg.Debug,
	})
	if streamErr != nil {
		return false, nil, streamErr
	}

	var toolCalls []stream.ToolCall
	var toolDeltas = make(map[string]*strings.Builder) // accumulate deltas per tool call ID
	var lastUsage stream.Usage
	var textBuf strings.Builder
	var thinkingBuf strings.Builder
	startTime := time.Now()
	var firstToken time.Duration
	var hasFirstToken bool

	var toolBlockShown bool

	for ev := range events {
		switch e := ev.(type) {
		case stream.ThinkingDelta:
			if !hasFirstToken {
				firstToken = time.Since(startTime)
				hasFirstToken = true
			}
			d.Thinking(e.Text)
			thinkingBuf.WriteString(e.Text)
		case stream.TextDelta:
			if !hasFirstToken {
				firstToken = time.Since(startTime)
				hasFirstToken = true
			}
			d.Text(e.Text)
			textBuf.WriteString(e.Text)
		case stream.ToolCallStart:
			if !hasFirstToken {
				firstToken = time.Since(startTime)
				hasFirstToken = true
			}
			// Track for full arguments
			if _, ok := toolDeltas[e.ID]; !ok {
				toolDeltas[e.ID] = new(strings.Builder)
			}
			// Gray box with hourglass on first tool detection – instant feedback
			if !toolBlockShown {
				d.ToolBlock("⏳ waiting for tools...")
				toolBlockShown = true
			}
		case stream.ToolCallDelta:
			if b, ok := toolDeltas[e.ID]; ok {
				b.WriteString(e.Delta)
			} else {
				toolDeltas[e.ID] = new(strings.Builder)
				toolDeltas[e.ID].WriteString(e.Delta)
			}
		case stream.ToolCall:
			if !hasFirstToken {
				firstToken = time.Since(startTime)
				hasFirstToken = true
			}
			toolCalls = append(toolCalls, e)
		case stream.Finish:
			lastUsage = e.Usage
		case stream.StreamError:
			return false, nil, e.Err
		}
	}

	hasText := textBuf.Len() > 0
	hasTools := len(toolCalls) > 0

	if hasText || hasTools {
		var content []providers.ContentBlock

		// Add thinking block first (before text, chronologically)
		if thinkingBuf.Len() > 0 {
			content = append(content, providers.ContentBlock{
				Type:     "thinking",
				Thinking: thinkingBuf.String(),
			})
		}

		// Add text block
		if hasText {
			content = append(content, providers.ContentBlock{
				Type: "text",
				Text: textBuf.String(),
			})
		}

		// Add tool call blocks
		for _, tc := range toolCalls {
			var args json.RawMessage
			if tc.Arguments != "" {
				args = json.RawMessage(tc.Arguments)
			}
			content = append(content, providers.ContentBlock{
				Type:      "toolCall",
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: args,
			})
		}

		msg := providers.RichMessage{
			Role:    "assistant",
			Content: content,
		}
		*msgs = append(*msgs, msg)

		// Write assistant message to session
		if cfg.Session != nil {
			writeAssistantSessionEvent(cfg.Session, p.Name(), cfg.Model, msg, &lastUsage)
		}
	}

	if !hasTools {
		// No tools – show usage and stop
		if lastUsage.Input > 0 || lastUsage.Output > 0 {
			d.Summary(lastUsage, stream.Stats{
				Duration:   time.Since(startTime),
				FirstToken: firstToken,
			})
		}
		return false, &lastUsage, nil
	}

	executeAndAppendToolResults(ctx, d, msgs, cfg, toolCalls, toolDeltas)

	// Show usage AFTER tools execution
	if lastUsage.Input > 0 || lastUsage.Output > 0 {
		d.Summary(lastUsage, stream.Stats{
			Duration:   time.Since(startTime),
			FirstToken: firstToken,
		})
	}
	return true, &lastUsage, nil
}
