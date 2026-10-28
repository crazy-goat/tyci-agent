package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/stream"
)

// SchemaFunc is called on every request, so a tool added during a session is
// offered on the next request.
func TestRunOnce_SchemaFuncIsReadPerRequest(t *testing.T) {
	calls := 0
	cfg := Config{SchemaFunc: func() json.RawMessage {
		calls++
		return json.RawMessage(fmt.Sprintf(`[%d]`, calls))
	}}
	for i := 1; i <= 2; i++ {
		fake := connectortest.Text("hi")
		msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}}
		var total stream.Usage
		if _, _, _, err := runOnce(context.Background(), fake, &silentDisplay{}, &msgs, cfg, &total); err != nil {
			t.Fatal(err)
		}
		got := fake.Requests()
		if want := fmt.Sprintf(`[%d]`, i); len(got) != 1 || string(got[0].Tools) != want {
			t.Fatalf("request %d: tools = %v, want %s", i, got, want)
		}
	}
}

// A non-nil Schema wins, so a /btw child keeps the schema it was given.
func TestRunOnce_SchemaWinsOverSchemaFunc(t *testing.T) {
	cfg := Config{
		Schema: json.RawMessage(`[9]`),
		SchemaFunc: func() json.RawMessage {
			t.Fatal("SchemaFunc was called although Schema is set")
			return nil
		},
	}
	fake := connectortest.Text("hi")
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}}
	var total stream.Usage
	if _, _, _, err := runOnce(context.Background(), fake, &silentDisplay{}, &msgs, cfg, &total); err != nil {
		t.Fatal(err)
	}
	if got := fake.Requests(); len(got) != 1 || string(got[0].Tools) != `[9]` {
		t.Fatalf("tools = %v, want [9]", got)
	}
}
