package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/stream"
)

// alwaysTool emits a tool call on every Stream call, so the agent loop never
// finishes on its own and is forced to stop at MaxIterations. The script sits
// in OnExhausted with Turns left empty, which is how a Fake says "every call,
// including the first".
func alwaysTool() *connectortest.Fake {
	return &connectortest.Fake{
		ProviderName: "always-tool",
		ModelName:    "always-1",
		OnExhausted: []stream.Event{
			stream.ToolCall{ID: "tc", Name: "noop", Arguments: "{}"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
}

// When the model keeps calling tools past MaxIterations, Run must stop and
// return ErrMaxIterations so callers (notably the subagent runner) can tell the
// turn was cut off rather than completed.
func TestRun_MaxIterations_ReturnsSentinel(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	_, err := Run(context.Background(), alwaysTool(), &silentDisplay{}, &msgs, Config{
		MaxRetries:    1,
		MaxIterations: 3,
		Tools:         newMockToolRunner(),
	})
	if !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("expected ErrMaxIterations, got %v", err)
	}
}
