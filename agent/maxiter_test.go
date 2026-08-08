package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/stream"
)

// alwaysToolProvider emits a tool call on every Stream call, so the agent loop
// never finishes on its own and is forced to stop at MaxIterations.
type alwaysToolProvider struct{}

func (alwaysToolProvider) Provider() string { return "always-tool" }
func (alwaysToolProvider) Model() string    { return "always-1" }

func (alwaysToolProvider) Stream(ctx context.Context, req connector.Request) (<-chan stream.Event, error) {
	ch := make(chan stream.Event, 2)
	ch <- stream.ToolCall{ID: "tc", Name: "noop", Arguments: "{}"}
	ch <- stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}}
	close(ch)
	return ch, nil
}

// When the model keeps calling tools past MaxIterations, Run must stop and
// return ErrMaxIterations so callers (notably the subagent runner) can tell the
// turn was cut off rather than completed.
func TestRun_MaxIterations_ReturnsSentinel(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	_, err := Run(context.Background(), alwaysToolProvider{}, &silentDisplay{}, &msgs, Config{
		Model:         "always-1",
		MaxRetries:    1,
		MaxIterations: 3,
		Tools:         newMockToolRunner(),
	})
	if !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("expected ErrMaxIterations, got %v", err)
	}
}
