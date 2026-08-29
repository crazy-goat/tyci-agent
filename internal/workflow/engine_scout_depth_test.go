package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"

	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// schemaToolNames extracts the set of tool names offered in a marshaled
// tool schema — same shape as main_delegation_depth_test.go's helper of the
// same name (package main), reimplemented here because this package cannot
// import package main.
func schemaToolNames(t *testing.T, data []byte) map[string]bool {
	t.Helper()
	var entries []map[string]any
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		if fn, ok := e["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok {
				names[n] = true
			}
		}
	}
	return names
}

// TestSessionAwait_NamedAgentSchemaScoutGateAgreement is the regression test
// for review finding 4: a session created with tyci.new_session(model,
// {agent = "name"}) used to build its schema via
// tools.GetSubagentToolsSchemaJSONFor(def.Tools), which — for a definition
// with no tools: whitelist — falls through to
// tools.GetSubagentToolsSchema(), unconditionally including "scout"
// (item 21's depth gate is not consulted there at all). Meanwhile runCtx
// never carried a depth (nothing ever called tools.WithDepth on it), so it
// defaulted to depth 0 — offering "scout" in the schema while RunTool's own
// depth-0 gate always refused it. The fix (engine.go's sessionAwait) now
// runs a named-agent session at sessionDepth = e.ctx's own depth + 1 (an
// ordinary child depth, exactly like runSingleTask assigns every other
// subagent), stamping that same depth on runCtx (tools.WithDepth) and, for
// an UNRESTRICTED definition (no tools: whitelist — this test's case),
// building the schema from tools.GetSubagentToolsSchemaJSONForAtDepth so it
// re-adds "scout" at that same depth. (A RESTRICTED definition takes a
// different branch on purpose — engineToolRunner's static
// AllowOnlySubagent(def.Tools) gate has no per-call "scout" exemption the
// way main.go's subagentToolRunner.Run does, so offering "scout" there
// would be a schema/gate mismatch of its own; that case is not this test's
// concern.) This drives a real, unrestricted named-agent session through
// sessionAwait and confirms both halves agree: the schema offered "scout",
// and calling it actually ran instead of being refused with a
// nesting-depth error.
func TestSessionAwait_NamedAgentSchemaScoutGateAgreement(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, ".tyci", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	def := "---\nmodel: scout-depth-provider/scout-depth-model\n---\nYou are the scout-depth test role.\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "scout-depth-agent.md"), []byte(def), 0644); err != nil {
		t.Fatal(err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// A real scout dispatch (through the shared tools registry) needs a
	// SubAgentRunner wired — same fakeSubAgentRunner engine_subagent_test.go
	// uses for tyci.subagent(), reused here so a scout's own nested run
	// resolves deterministically without a second scripted model turn.
	runner := &fakeSubAgentRunner{}
	tools.SetSubAgentRunner(runner)
	t.Cleanup(func() { tools.SetSubAgentRunner(nil) })

	fake := &connectortest.Fake{
		ProviderName: "scout-depth-provider",
		ModelName:    "scout-depth-model",
		Turns: [][]stream.Event{
			{
				stream.ToolCall{ID: "tc1", Name: "scout", Arguments: `{"task":"look around"}`},
				stream.Finish{Reason: "tool_calls"},
			},
			{
				stream.TextDelta{Text: "done"},
				stream.Finish{Reason: "stop"},
			},
		},
	}
	providers.Register(&fakeProvider{name: "scout-depth-provider", model: "scout-depth-model", client: fake})

	engine := NewEngine(context.Background(), "prompt")
	opts := engine.L.NewTable()
	engine.L.SetField(opts, "agent", lua.LString("scout-depth-agent"))
	sessTbl, sess := newSessionTable(t, engine, "", opts)
	if sess.agentDef == nil {
		t.Fatal("session.agentDef is nil — opts.agent did not resolve")
	}

	engine.L.Push(engine.L.NewFunction(engine.sessionAwait))
	engine.L.Push(sessTbl)
	if err := engine.L.PCall(1, 1, nil); err != nil {
		t.Fatalf("session:await failed: %v", err)
	}
	engine.L.Pop(1)

	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model received %d requests, want 2 (initial + post-tool-call)", len(reqs))
	}

	if names := schemaToolNames(t, reqs[0].Tools); !names["scout"] {
		t.Fatalf("expected the named-agent session's schema to offer \"scout\" (depth 1), got tools: %v", names)
	}

	found := false
	for _, msg := range reqs[1].Messages {
		if msg.Role != "toolResult" {
			continue
		}
		for _, block := range msg.Content {
			if strings.Contains(block.Text, "not available at nesting depth") {
				t.Fatalf("schema offered \"scout\" but the runtime gate refused it: %q", block.Text)
			}
			if strings.Contains(block.Text, "child result for: look around") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected the scout call to actually run and reach the fake subagent runner; requests[1].Messages = %+v", reqs[1].Messages)
	}
}
