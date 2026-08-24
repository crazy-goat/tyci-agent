package api

import (
	"context"
	"testing"

	"github.com/decodo/tyci/stream"
)

func TestResponsesStreamerTextToolAndUsage(t *testing.T) {
	doer := &stubDoer{body: `event: response.output_text.delta
data: {"delta":"Hello "}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"world"}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":""}}
event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"path\":"}
event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"\"README.md\"}"}
event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"README.md\"}","status":"completed"}}
event: response.completed
data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":12,"output_tokens":8,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}}
`}
	var events []stream.Event
	err := (ResponsesStreamer{HTTP: doer}).Stream(context.Background(), "sk-test", "https://api.example.invalid/v1/responses", ResponsesRequest{
		Model:  "gpt-5.6-luna",
		Input:  []ResponsesInputItem{{Role: "user", Content: []ResponsesContentPart{{Type: "input_text", Text: "hi"}}}},
		Stream: true,
	}, func(ev stream.Event) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var text string
	var started *stream.ToolCallStart
	var tool *stream.ToolCall
	var finish *stream.Finish
	for _, ev := range events {
		switch e := ev.(type) {
		case stream.TextDelta:
			text += e.Text
		case stream.ToolCallStart:
			started = &e
		case stream.ToolCall:
			tool = &e
		case stream.Finish:
			finish = &e
		}
	}
	if text != "Hello world" {
		t.Fatalf("text = %q, want Hello world", text)
	}
	if started == nil || started.ID != "call_1" || started.Name != "read" {
		t.Fatalf("tool start = %#v", started)
	}
	if tool == nil || tool.ID != "call_1" || tool.Name != "read" || tool.Arguments != `{"path":"README.md"}` {
		t.Fatalf("tool = %#v", tool)
	}
	if finish == nil {
		t.Fatal("missing finish")
	}
	if finish.Reason != "stop" {
		t.Errorf("finish reason = %q, want stop", finish.Reason)
	}
	if finish.Usage.Input != 12 || finish.Usage.Output != 8 || finish.Usage.Reasoning != 2 || finish.Usage.CacheRead != 3 {
		t.Errorf("usage = %+v", finish.Usage)
	}
}

func TestResponsesStreamerIncompleteMapsToLength(t *testing.T) {
	doer := &stubDoer{body: `data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":1,"output_tokens":2}}}
`}
	var finish stream.Finish
	err := (ResponsesStreamer{HTTP: doer}).Stream(context.Background(), "", "https://api.example.invalid/v1/responses", ResponsesRequest{
		Model:  "model",
		Input:  []ResponsesInputItem{},
		Stream: true,
	}, func(ev stream.Event) error {
		if e, ok := ev.(stream.Finish); ok {
			finish = e
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if finish.Reason != "length" {
		t.Fatalf("finish reason = %q, want length", finish.Reason)
	}
}
