package connect

import (
	"encoding/json"
	"testing"
)

func TestModelsDevModelKeepsReasoningAndToolCall(t *testing.T) {
	in := `{"id":"m","reasoning":true,"tool_call":false,` +
		`"reasoning_options":[{"type":"effort","values":["low","high"]},{"type":"toggle"}]}`
	var m ModelsDevModel
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back ModelsDevModel
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Reasoning || back.ToolCall == nil || *back.ToolCall {
		t.Fatalf("lost reasoning/tool_call: %s", out)
	}
	if len(back.ReasoningOptions) != 2 || back.ReasoningOptions[0].Values[1] != "high" {
		t.Fatalf("lost reasoning_options: %s", out)
	}
}
