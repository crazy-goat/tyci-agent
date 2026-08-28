package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// PhaseSink is an optional Sink capability: a display that can show which
// transport phase the current round is in — sending the request, waiting on
// the server, or (once headers arrive) receiving the model's own output —
// is told the phase name to show right now.
//
// Optional rather than folded into Sink itself, matching toolDurationSink /
// streamProgressDisplay (run_tools.go): only display.TUI shows phases, and
// the other Sink implementations (Minimal, Terminal, BtwSink, the various
// collectors, the ledger watch wrapper) would gain a method they ignore.
// Asserted once per round, right where the two phases it does not already
// know about begin — see below.
type PhaseSink interface {
	// Phase names the phase now starting: "sending" (Request() until the
	// request body is written), "waiting" (until the response's first
	// byte), or "thinking" (from there until the model's own first token —
	// display.TUI reuses its existing "thinking" status for this so a
	// silently prefilling model shows a growing "thinking... Ns" instead of
	// a second bespoke label).
	Phase(name string)
}

func runOnce(ctx context.Context, mc connector.ModelClient, d Sink, msgs *[]connector.Message, cfg Config, totalUsage *stream.Usage) (more bool, usage *stream.Usage, totalEmitted bool, err error) {
	ctx = connector.WithModelClient(ctx, mc)
	// Stamped once per round, alongside the model client, so a tool call made
	// during this round's executeAndAppendToolResults (in practice: the
	// "subagent" tool's inherit_history option) can read back the
	// conversation as it stands right now. Safe to hand out the live slice
	// as-is: nothing mutates *msgs until after tool execution finishes below,
	// and a reader that wants to keep it past that point must copy it (see
	// connector.WithConversation's doc comment and session.ForkMessages).
	ctx = connector.WithConversation(ctx, *msgs)
	if cfg.Compactor != nil {
		ctx = tools.WithCompactor(ctx, cfg.Compactor)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	d.Request(roundInputLabel(*msgs))
	ps, _ := d.(PhaseSink)
	if ps != nil {
		// The request is about to be built and sent — nothing from the
		// transport marks this moment (WroteRequest fires once the body is
		// already on the wire), so it is set here rather than sourced from
		// a stream event like the two phases below.
		ps.Phase("sending")
	}

	events, streamErr := mc.Stream(streamCtx, connector.Request{
		Model:         mc.Model(),
		System:        cfg.System,
		Messages:      *msgs,
		Tools:         cfg.Schema,
		Debug:         cfg.Debug,
		Temperature:   cfg.Temperature,
		MaxTokens:     cfg.MaxTokens,
		NoPromptCache: cfg.NoPromptCache,
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
	var finishReason string

	var toolBlockShown bool

	for ev := range events {
		switch e := ev.(type) {
		case stream.RequestSent:
			if ps != nil {
				ps.Phase("waiting")
			}
		case stream.ResponseStarted:
			if ps != nil {
				ps.Phase("thinking")
			}
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
			finishReason = e.Reason
		case stream.StreamError:
			return false, nil, false, e.Err
		}
	}

	hasText := textBuf.Len() > 0
	hasTools := len(toolCalls) > 0

	// "length" means the reply hit max_tokens and stopped mid-sentence. The
	// reason was already being parsed and then thrown away, so nothing said
	// so: a truncated answer looked like a terse one, and a truncated tool
	// call surfaced as "invalid arguments", sending the model looking for a
	// bug in JSON it had written correctly and simply not finished.
	if finishReason == "length" {
		msg := "reply cut off: it reached the max_tokens limit"
		if cfg.MaxTokens > 0 {
			msg = fmt.Sprintf("reply cut off: it reached the max_tokens limit of %d", cfg.MaxTokens)
		}
		if hasTools {
			msg += " — a tool call may be incomplete"
		}
		msg += ". Raise it with --max-tokens, or \"max_tokens\" in ~/.tyci/config.json."
		d.ToolBlock(msg)
	}

	if hasText || hasTools {
		var content []connector.ContentBlock

		// Add thinking block first (before text, chronologically)
		if thinkingBuf.Len() > 0 {
			content = append(content, connector.ContentBlock{
				Type:     "thinking",
				Thinking: thinkingBuf.String(),
			})
		}

		// Add text block
		if hasText {
			content = append(content, connector.ContentBlock{
				Type: "text",
				Text: textBuf.String(),
			})
		}

		// Add tool call blocks. Skip malformed tool calls without a name —
		// a nameless tool_call cannot be dispatched and, when replayed in a
		// follow-up request, triggers "tool_calls[0] is missing a function
		// name" 400s on strict OpenAI-compatible providers (GLM, DeepSeek).
		// An empty ID is back-filled with a stable value so the matching
		// tool-result message can carry the required tool_call_id.
		for i, tc := range toolCalls {
			if tc.Name == "" {
				continue
			}
			var args json.RawMessage
			if tc.Arguments != "" {
				args = json.RawMessage(tc.Arguments)
			}
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			content = append(content, connector.ContentBlock{
				Type:      "toolCall",
				ID:        id,
				Name:      tc.Name,
				Arguments: args,
			})
		}

		msg := connector.Message{
			Role:    "assistant",
			Content: content,
		}
		*msgs = append(*msgs, msg)

		// Write assistant message to session
		if cfg.Session != nil {
			writeAssistantSessionEvent(cfg.Session, mc.Provider(), mc.Model(), msg, &lastUsage)
		}
	}

	if !hasTools {
		// No tools – show usage, accumulate into session total, and emit
		// the Costs line in one consistent block. usage carries this round's
		// raw Input/Output back to the caller for context-budget accounting
		// (see agent.Run's contextReminded logic); it is never re-added to
		// totalUsage by the caller, so returning it here does not double-count.
		// Only returned when hasUsage is true: a usage-less round (e.g. a
		// provider that omits Finish.Usage) must leave the caller's tracked
		// "last round" usage untouched rather than zeroing it out, or the
		// budget reminder would go permanently blind for the rest of the turn.
		var usageOut *stream.Usage
		if hasUsage(lastUsage) {
			d.Summary(lastUsage, stream.Stats{
				Duration:   time.Since(startTime),
				FirstToken: firstToken,
			})
			if totalUsage != nil {
				totalUsage.Add(lastUsage)
				d.Total(*totalUsage)
			}
			usageOut = &lastUsage
		}
		return false, usageOut, true, nil
	}

	executeAndAppendToolResults(ctx, d, msgs, cfg, toolCalls, toolDeltas)

	// Show usage AFTER tools execution, then accumulate into the session
	// total and emit the Costs line — all in one place, so the Costs
	// line is always in sync with the just-displayed Summary. usage carries
	// this round's raw Input/Output back to the caller (see the !hasTools
	// branch above); it is never re-added to totalUsage by the caller, and
	// is nil when hasUsage is false for the same reason as above.
	emitted := false
	var usageOut *stream.Usage
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
		usageOut = &lastUsage
	}
	return true, usageOut, emitted, nil
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
func roundInputLabel(msgs []connector.Message) string {
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
