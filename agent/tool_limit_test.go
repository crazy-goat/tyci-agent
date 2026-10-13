package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/stream"
)

// A turn with three tool calls and a limit of two: the first two run, the
// third gets an error result, and Run stops after that batch.
func TestRun_MaxToolCalls_StopsAfterBatch(t *testing.T) {
	fake := &connectortest.Fake{Turns: [][]stream.Event{
		{
			stream.ToolCall{ID: "a", Name: "noop", Arguments: "{}"},
			stream.ToolCall{ID: "b", Name: "noop", Arguments: "{}"},
			stream.ToolCall{ID: "c", Name: "noop", Arguments: "{}"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
		{stream.TextDelta{Text: "never reached"}, stream.Finish{}},
	}}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	runner := newMockToolRunner()
	_, err := Run(context.Background(), fake, &silentDisplay{}, &msgs, Config{
		MaxRetries:   1,
		MaxToolCalls: 2,
		Tools:        runner,
	})
	if !errors.Is(err, ErrMaxToolCalls) {
		t.Fatalf("err = %v, want ErrMaxToolCalls", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("tool runner got %d calls, want 2", len(runner.calls))
	}
}

// Zero means unlimited: the run ends on MaxIterations, not on the tool call
// limit, and every call runs.
func TestRun_MaxToolCallsZeroIsUnlimited(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	runner := newMockToolRunner()
	_, err := Run(context.Background(), alwaysTool(), &silentDisplay{}, &msgs, Config{
		MaxRetries:    1,
		MaxIterations: 3,
		MaxToolCalls:  0,
		Tools:         runner,
	})
	if !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("err = %v, want ErrMaxIterations", err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("tool runner got %d calls, want 3", len(runner.calls))
	}
}
