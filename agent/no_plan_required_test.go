package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/stream"
)

// TestRun_FirstNonTodoToolCallRunsWithoutPlan is a smoke test of the loop:
// a bash call in the first turn runs and its result is not a refusal.
func TestRun_FirstNonTodoToolCallRunsWithoutPlan(t *testing.T) {
	p := &connectortest.Fake{
		ProviderName: "np",
		ModelName:    "np-1",
		Turns: [][]stream.Event{{
			stream.ToolCallStart{Name: "bash"},
			stream.ToolCallDelta{Delta: `{"command":"echo hello"}`},
			stream.ToolCall{ID: "tc1", Name: "bash", Arguments: `{"command":"echo hello"}`},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		}},
		OnExhausted: []stream.Event{},
	}
	d := &captureDisplay{}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "do something"}}},
	}

	if _, err := Run(context.Background(), p, d, &msgs, Config{MaxRetries: 1, Tools: newMockToolRunner()}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	found := false
	for _, end := range d.toolCallEnds {
		if end.Name != "bash" {
			continue
		}
		found = true
		if strings.Contains(end.Result, "plan") {
			t.Errorf("bash call was refused: %q", end.Result)
		}
	}
	if !found {
		t.Fatal("bash tool call did not run")
	}
}
