package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/crazy-goat/tyci-agent/agent"
	"github.com/crazy-goat/tyci-agent/conductor"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/stream"
)

// fixedToolRunner answers every tool call with the same text.
type fixedToolRunner struct{}

func (fixedToolRunner) Run(context.Context, string, map[string]any) (string, error) {
	return "tool output", nil
}

// TestRunPromptPrintsOnlyAnswerOnStdout drives runPrompt, the code behind
// `tyci run --prompt`, with a fake model and a plainSink on buffers. The model
// asks for a tool first, so the answer comes in the second round. Stdout must
// hold the answer and one trailing newline. Stderr must stay empty.
func TestRunPromptPrintsOnlyAnswerOnStdout(t *testing.T) {
	exited, _, _ := withCapturedExit(t, nil)

	client := &connectortest.Fake{
		ProviderName: "run-test-prov",
		ModelName:    "run-test-model",
		Turns: [][]stream.Event{
			{
				stream.ToolCallStart{ID: "call-1", Name: "echo"},
				stream.ToolCall{ID: "call-1", Name: "echo", Arguments: `{"text":"hi"}`},
				stream.Finish{Reason: "tool_calls"},
			},
			{
				stream.TextDelta{Text: "the answer"},
				stream.Finish{Reason: "stop"},
			},
		},
	}
	var out, errOut bytes.Buffer
	disp := &plainSink{out: &out, err: &errOut}
	cond := conductor.New(conductor.Options{
		Client: client,
		Sink:   disp,
		Config: agent.Config{Tools: fixedToolRunner{}},
	})

	runPrompt(cond, disp, "say the answer", context.Background(), nil)

	if exited() {
		t.Fatalf("did not expect exitFunc on a successful run")
	}
	if got := out.String(); got != "the answer\n" {
		t.Errorf("stdout = %q, want %q", got, "the answer\n")
	}
	if got := errOut.String(); got != "" {
		t.Errorf("stderr = %q, want empty", got)
	}
	if got := client.Calls(); got != 2 {
		t.Errorf("model calls = %d, want 2 (tool round, then answer)", got)
	}
}
