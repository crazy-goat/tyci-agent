package bus

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestToolResultKindIsRegisteredDurable checks that KindToolResult has the
// Durable class and the ToolResult payload type.
func TestToolResultKindIsRegisteredDurable(t *testing.T) {
	info, ok := lookupKind(KindToolResult)
	if !ok {
		t.Fatalf("%q is not registered", KindToolResult)
	}
	if info.class != Durable {
		t.Errorf("class = %v, want Durable", info.class)
	}
	if want := reflect.TypeFor[ToolResult](); info.typ != want {
		t.Errorf("type = %v, want %v", info.typ, want)
	}
}

// TestToolResultJSONRoundTrip checks the wire names and that a payload
// survives publish and decode through the bus.
func TestToolResultJSONRoundTrip(t *testing.T) {
	in := ToolResult{CallID: "c3", Content: "out", Failed: true, DurationMS: 42, Truncated: true, FullPath: "/tmp/x"}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"call_id", "content", "failed", "duration_ms", "truncated", "full_path"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("JSON has no key %q: %s", k, raw)
		}
	}

	b := New()
	defer b.Close()
	sub := b.Subscribe("test", Filter{To: orchestrator})
	if _, err := Publish(b, KindToolResult, orchestrator, orchestrator, OriginSystem, in); err != nil {
		t.Fatal(err)
	}
	msgs := sub.Drain()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	got, err := Decode[ToolResult](msgs[0])
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Errorf("round trip = %+v, want %+v", got, in)
	}
}
