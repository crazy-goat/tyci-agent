package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
)

func runOnce(ctx context.Context, p providers.Provider, d display.Display, msgs *[]providers.RichMessage, cfg Config, totalUsage *stream.Usage) (more bool, usage *stream.Usage, totalEmitted bool, err error) {
	ctx = providers.WithProvider(ctx, p)
	ctx = providers.WithModel(ctx, cfg.Model)
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	d.Request(roundInputLabel(*msgs))

	events, streamErr := p.Stream(streamCtx, providers.Request{
		Model:    cfg.Model,
		System:   cfg.System,
		Messages: *msgs,
		Tools:    cfg.Schema,
		Debug:    cfg.Debug,
	})
	if streamErr != nil {
		// Provider failed before streaming started. runOnce does not emit
		// d.Total on error paths – the caller (agent.Run) handles them.
		return false, nil, false, streamErr
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
			return false, nil, false, e.Err
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
		// No tools – show usage, accumulate into session total, and emit
		// the Costs line in one consistent block. Return usage=nil so the
		// caller doesn't double-count.
		if hasUsage(lastUsage) {
			d.Summary(lastUsage, stream.Stats{
				Duration:   time.Since(startTime),
				FirstToken: firstToken,
			})
			if totalUsage != nil {
				totalUsage.Add(lastUsage)
				d.Total(*totalUsage)
			}
		}
		return false, nil, true, nil
	}

	executeAndAppendToolResults(ctx, d, msgs, cfg, toolCalls, toolDeltas)

	// Show usage AFTER tools execution, then accumulate into the session
	// total and emit the Costs line — all in one place, so the Costs
	// line is always in sync with the just-displayed Summary.
	// We return usage=nil so the caller (agent.Run) doesn't add the same
	// usage to totalUsage a second time.
	emitted := false
	if hasUsage(lastUsage) {
		d.Summary(lastUsage, stream.Stats{
			Duration:   time.Since(startTime),
			FirstToken: firstToken,
		})
		if totalUsage != nil {
			totalUsage.Add(lastUsage)
			d.Total(*totalUsage)
			emitted = true
		}
	}
	return true, nil, emitted, nil
}

// hasUsage reports whether a Usage value contains any token data.
// Includes input, output, reasoning, and cache tokens.
func hasUsage(u stream.Usage) bool {
	return u.Input > 0 || u.Output > 0 || u.Reasoning > 0 || u.CacheRead > 0 || u.CacheWrite > 0
}

// roundInputLabel returns a short label describing the input being sent
// to the model for the current round: "user prompt" for the first round
// and "return of tool" for subsequent rounds that feed tool results
// back. The run-mode Minimal display uses this for the [ REQ] line.
func roundInputLabel(msgs []providers.RichMessage) string {
	if n := len(msgs); n > 0 {
		switch msgs[n-1].Role {
		case "user":
			return "user prompt"
		case "toolResult", "tool":
			return "return of tool"
		}
	}
	return "request"
}
