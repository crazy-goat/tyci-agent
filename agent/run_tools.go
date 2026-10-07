package agent

import (
	"context"
	"strings"
	"time"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/stream"
)

type streamProgressDisplay interface {
	StreamProgress(toolIdx int, line string)
}

// toolFailureSink is an optional display capability. It carries the structured
// outcome of the tool call separately from the human-readable result text, so
// output that happens to begin with "Error:" or "❌ exit code" remains a
// successful tool result.
type toolFailureSink interface {
	ToolCallFailed(failed bool)
}

func executeAndAppendToolResults(ctx context.Context, d Sink, msgs *[]connector.Message, cfg Config, toolCalls []stream.ToolCall, toolDeltas map[string]*strings.Builder) {
	showToolCalls(d, toolCalls, toolDeltas)
	ctx = installToolStreaming(ctx, d)

	results, durations, failed := executeTools(ctx, cfg.Tools, toolCalls)

	appendToolResults(d, msgs, cfg, toolCalls, results, durations, failed)
}

func showToolCalls(d Sink, toolCalls []stream.ToolCall, toolDeltas map[string]*strings.Builder) {
	for _, tc := range toolCalls {
		d.ToolCallStart(tc.Name)
		if delta, ok := toolDeltas[tc.ID]; ok && delta.Len() > 0 {
			d.ToolCallDelta(delta.String())
		} else if tc.Arguments != "" {
			d.ToolCallDelta(tc.Arguments)
		}
	}
}

func installToolStreaming(ctx context.Context, d Sink) context.Context {
	if s, ok := d.(streamProgressDisplay); ok {
		return stream.WithOutput(ctx, func(toolIdx int, line string) {
			s.StreamProgress(toolIdx, line)
		})
	}
	return ctx
}

// toolDurationSink is an optional Sink capability: a display that shows how
// long each tool took can be told the real figure for the call whose
// ToolCallEnd comes next.
//
// An optional interface rather than a wider ToolCallEnd signature, matching
// streamProgressDisplay above: only the TUI shows per-tool timings, and the
// other four displays would gain a parameter they ignore.
type toolDurationSink interface {
	ToolCallDuration(d time.Duration)
}

func appendToolResults(d Sink, msgs *[]connector.Message, cfg Config, toolCalls []stream.ToolCall, results []string, durations []time.Duration, failed []bool) {
	ds, _ := d.(toolDurationSink)
	fs, _ := d.(toolFailureSink)
	for i, tc := range toolCalls {
		// Before ToolCallEnd, which is what consumes the queue entry this
		// duration and status belong to.
		if ds != nil && i < len(durations) {
			ds.ToolCallDuration(durations[i])
		}
		if fs != nil && i < len(failed) {
			fs.ToolCallFailed(failed[i])
		}
		d.ToolCallEnd(tc.Name, results[i])
		*msgs = append(*msgs, connector.Message{
			Role: "toolResult",
			Content: []connector.ContentBlock{{

				Type:       "text",
				Text:       results[i],
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				IsError:    strings.HasPrefix(results[i], "Error:"),
			}},
		})
		if cfg.Session != nil {
			writeToolResultSessionEvent(cfg.Session, tc.ID, tc.Name, results[i])
		}
	}
	d.ToolFinish()
}
