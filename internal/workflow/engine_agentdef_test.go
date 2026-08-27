package workflow

import (
	"context"
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

// TestSessionAwait_NamedAgentAppliesToolsWhitelistAndRole is the regression
// test for the fix to sessionAwait: a session created with
// tyci.new_session(model, {agent = "name"}) used to source only
// MaxIterations/Model from the named agent definition, silently dropping
// its tools: whitelist and system prompt — the session got the full,
// ungated top-level tool schema no matter what the definition restricted it
// to, unlike the real subagent path (main.go's subagentToolRunner.Run +
// agentRunner.RunTaskWithSystem), which applies both.
//
// This drives a session with an agent definition restricted to tools:
// [read] and checks two things a model call against it must show:
//  1. a call to a tool NOT on that whitelist (wf_gated_echo) is refused by
//     the runtime tool gate, not merely omitted from the schema (the
//     schema is only a hint — see tools/toolgate.go's own doc comment);
//  2. the system prompt sent actually carries the agent's role text and the
//     subagent contract, not the bare top-level prompt.
func TestSessionAwait_NamedAgentAppliesToolsWhitelistAndRole(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, ".tyci", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	def := "---\nmodel: wf-fake-provider/wf-fake-model\nmax_iterations: 5\ntools: read\n---\nYou are the gated test role.\n"
	if err := os.WriteFile(filepath.Join(agentsDir, "wf-gated-agent.md"), []byte(def), 0644); err != nil {
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

	// A Lua tool the agent's tools: [read] whitelist does NOT include.
	defer tools.SnapshotLuaToolsForTesting()()
	toolsDir := t.TempDir()
	script := `return {
		schema = { name = "wf_gated_echo", description = "should be denied", parameters = {} },
		run = function(ctx, args) return { success = true, content = "should not run" } end,
	}`
	if err := os.WriteFile(filepath.Join(toolsDir, "wf_gated_echo.lua"), []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	tools.LoadAndRegisterLocalLuaTools(toolsDir)

	fake := &connectortest.Fake{
		ProviderName: "wf-fake-provider",
		ModelName:    "wf-fake-model",
		Turns: [][]stream.Event{
			{
				stream.ToolCallStart{ID: "tc1", Name: "wf_gated_echo"},
				stream.ToolCallDelta{ID: "tc1", Delta: `{}`},
				stream.ToolCall{ID: "tc1", Name: "wf_gated_echo", Arguments: `{}`},
				stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
			},
			{
				stream.TextDelta{Text: "done"},
				stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
			},
		},
	}
	providers.Register(&fakeProvider{name: "wf-fake-provider", model: "wf-fake-model", client: fake})

	engine := NewEngine(context.Background(), "prompt")
	opts := engine.L.NewTable()
	engine.L.SetField(opts, "agent", lua.LString("wf-gated-agent"))
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

	if !strings.Contains(reqs[0].System, "SUBAGENT") {
		t.Errorf("system prompt did not carry the subagent contract: %q", reqs[0].System)
	}
	if !strings.Contains(reqs[0].System, "gated test role") {
		t.Errorf("system prompt did not carry the agent's own role text: %q", reqs[0].System)
	}

	found := false
	for _, msg := range reqs[1].Messages {
		if msg.Role != "toolResult" {
			continue
		}
		for _, block := range msg.Content {
			if strings.Contains(block.Text, "not available to this agent") {
				found = true
			}
			if strings.Contains(block.Text, "should not run") {
				t.Fatalf("wf_gated_echo actually ran despite not being on the agent's tools: whitelist; tool result = %q", block.Text)
			}
		}
	}
	if !found {
		t.Errorf("expected the tool call denied by the runtime gate to say so; requests[1].Messages = %+v", reqs[1].Messages)
	}
}
