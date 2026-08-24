package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestBtwReadOnlyGate_DeniesMutatingTools(t *testing.T) {
	gate := BtwReadOnlyGate()
	for _, name := range []string{"write", "bash", "lua", "todo", "subagent", "wait", "message", "kill_job", "cron", "memory", "lock", "unlock", "promote_btw"} {
		if err := gate(name); err == nil {
			t.Errorf("read-only /btw gate allowed %q", name)
		}
	}
	for _, name := range []string{"find", "read", "skills", "web", "help", "agents"} {
		if err := gate(name); err != nil {
			t.Errorf("read-only /btw gate denied %q: %v", name, err)
		}
	}
}

func TestBtwEvaluationSchema_MatchesReadOnlyGate(t *testing.T) {
	var schema []map[string]any
	if err := json.Unmarshal(BtwEvaluationSchemaJSON(), &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	gate := BtwReadOnlyGate()
	for _, entry := range schema {
		fn, _ := entry["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if err := gate(name); err != nil {
			t.Errorf("schema advertises denied tool %q: %v", name, err)
		}
	}
	if err := gate("promote_btw"); err == nil {
		t.Fatal("promotion must not be available during the read-only evaluation")
	}
	for _, name := range []string{"write", "bash", "lua", "todo", "subagent", "wait", "promote_btw"} {
		ctx := WithToolGate(context.Background(), gate)
		if got := RunTool(ctx, name, nil); got.Success {
			t.Errorf("read-only /btw runtime allowed %q", name)
		}
	}
}
