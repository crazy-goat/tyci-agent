package agent

import (
	"context"
	"strings"

	"github.com/decodo/tyci-agent/display"
	"github.com/decodo/tyci-agent/providers"
	"github.com/decodo/tyci-agent/stream"
)

type streamProgressDisplay interface {
	StreamProgress(toolIdx int, line string)
}

func executeAndAppendToolResults(ctx context.Context, d display.Display, msgs *[]providers.RichMessage, cfg Config, toolCalls []stream.ToolCall, toolDeltas map[string]strings.Builder) {
	showToolCalls(d, toolCalls, toolDeltas)
	prevOnOutput := installToolStreaming(d)
	results := executeTools(ctx, cfg.Tools, toolCalls)
	stream.OnOutput = prevOnOutput
	appendToolResults(d, msgs, cfg, toolCalls, results)
}

func showToolCalls(d display.Display, toolCalls []stream.ToolCall, toolDeltas map[string]strings.Builder) {
	for _, tc := range toolCalls {
		d.ToolCallStart(tc.Name)
		if delta, ok := toolDeltas[tc.ID]; ok && delta.Len() > 0 {
			d.ToolCallDelta(delta.String())
		} else if tc.Arguments != "" {
			d.ToolCallDelta(tc.Arguments)
		}
	}
}

func installToolStreaming(d display.Display) func(int, string) {
	prevOnOutput := stream.OnOutput
	if s, ok := d.(streamProgressDisplay); ok {
		stream.OnOutput = func(toolIdx int, line string) {
			s.StreamProgress(toolIdx, line)
		}
	} else {
		stream.OnOutput = nil
	}
	return prevOnOutput
}

func appendToolResults(d display.Display, msgs *[]providers.RichMessage, cfg Config, toolCalls []stream.ToolCall, results []string) {
	for i, tc := range toolCalls {
		d.ToolCallEnd(tc.Name, results[i])
		*msgs = append(*msgs, providers.RichMessage{
			Role: "toolResult",
			Content: []providers.ContentBlock{{
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
}
