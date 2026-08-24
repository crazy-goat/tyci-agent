package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// findLastStepWarning returns the index of the harness-injected
// buildLastStepWarning message in msgs, or -1 if it is not present.
func findLastStepWarning(msgs []connector.Message) int {
	for i, m := range msgs {
		if m.Role != "user" {
			continue
		}
		for _, c := range m.Content {
			if c.Type == "text" && strings.Contains(c.Text, "this is your LAST step") {
				return i
			}
		}
	}
	return -1
}

// TestRun_InjectsLastStepWarning_BeforeFinalIteration pins item 28(A): one
// iteration before the cap is hit, the harness must inject a message
// telling the model this is its last turn and that it must not call any
// tools — because at the true last iteration a tool call would end the loop
// before the model ever sees the result. Revert the injection in Run (the
// lastStepWarned block right before runOnce) and this test fails: either the
// warning message never appears, or it appears too late for the model to
// have seen it going into its final turn.
func TestRun_InjectsLastStepWarning_BeforeFinalIteration(t *testing.T) {
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

	idx := findLastStepWarning(msgs)
	if idx < 0 {
		t.Fatalf("expected a last-step warning message in msgs, got none: %#v", msgs)
	}

	// The warning text itself must forbid tool calls this turn — the
	// critical detail from the spec: if the model's final turn is a tool
	// call, the loop exits before it ever sees the result.
	warningText := msgs[idx].Content[0].Text
	if !strings.Contains(warningText, "Do NOT call any tools") {
		t.Errorf("last-step warning does not forbid tool calls: %q", warningText)
	}
	if !strings.Contains(warningText, "automated check") {
		t.Errorf("last-step warning is not framed as an automated/harness check: %q", warningText)
	}

	// It must have been injected before the model's FINAL turn, i.e. before
	// the model's own last assistant message — not after. Exactly one
	// assistant turn (the model's final tool call attempt) may follow it.
	assistantsAfter := 0
	for _, m := range msgs[idx+1:] {
		if m.Role == "assistant" {
			assistantsAfter++
		}
	}
	if assistantsAfter != 1 {
		t.Errorf("expected exactly 1 assistant turn after the warning (the final iteration), got %d", assistantsAfter)
	}
}

// Top-level runs with an explicit caller deadline still receive the final-step
// warning; ordinary subagent runs do not create such a deadline.
func TestRun_InjectsLastStepWarning_OnDeadline(t *testing.T) {
	// A deadline well inside lastStepDeadlineThreshold, so the very first
	// iteration must already trigger the warning.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// A model that finishes immediately (no tool call) — the point of this
	// test is only whether the warning gets injected before the call the
	// deadline is about to cut off, not the iteration-cap machinery.
	p := &connectortest.Fake{ProviderName: "p", ModelName: "m", Turns: [][]stream.Event{
		{
			stream.TextDelta{Text: "ok"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}}

	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}},
	}
	_, err := Run(ctx, p, &silentDisplay{}, &msgs, Config{
		MaxRetries: 1,
		// No MaxIterations set: only the deadline should trigger the warning.
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if idx := findLastStepWarning(msgs); idx < 0 {
		t.Fatalf("expected a last-step warning injected ahead of the near deadline, got none: %#v", msgs)
	}
}
