package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	lua "github.com/yuin/gopher-lua"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// fakeProvider adapts a connector.ModelClient (connectortest.Fake) to the
// providers.Provider interface, so a session created through the engine can
// resolve session.model via providers.FindModel without touching any real
// network credential.
type fakeProvider struct {
	name   string
	model  string
	client connector.ModelClient
}

func (p *fakeProvider) Name() string          { return p.name }
func (p *fakeProvider) IsConfigured() bool    { return true }
func (p *fakeProvider) Models() []string      { return []string{p.model} }
func (p *fakeProvider) Client(string) connector.ModelClient {
	return p.client
}
func (p *fakeProvider) ConfigWarnings() []string { return nil }

// TestSessionAwait_ToolsWired verifies the fix for the review finding that
// sessions created via the engine had no tools/schema: a session driven by
// tyci.new_session/session:await must be able to call a real tyci tool from
// within the model conversation it drives, not just from a script's own
// tyci.run_tool. Wired via engineToolRunner + tools.GetTopLevelToolsSchemaJSON
// in sessionAwait (engine.go).
func TestSessionAwait_ToolsWired(t *testing.T) {
	// Register a project-local Lua tool ("wf_echo") so the test does not
	// depend on any built-in tool's exact side effects — see
	// tools.SnapshotLuaToolsForTesting's doc comment for why this must be
	// undone at the end of the test.
	defer tools.SnapshotLuaToolsForTesting()()

	dir := t.TempDir()
	script := `return {
		schema = {
			name = "wf_echo",
			description = "echoes its msg argument",
			parameters = {},
		},
		run = function(ctx, args)
			return { success = true, content = "echo:" .. (args.msg or "") }
		end,
	}`
	if err := os.WriteFile(filepath.Join(dir, "wf_echo.lua"), []byte(script), 0644); err != nil {
		t.Fatalf("write lua tool: %v", err)
	}
	tools.LoadAndRegisterLocalLuaTools(dir)

	// A model that first emits a tool call for wf_echo, then (once it sees
	// the tool result on the next turn) finishes with plain text.
	fake := &connectortest.Fake{
		ProviderName: "wf-fake",
		ModelName:    "wf-fake-1",
		Turns: [][]stream.Event{
			{
				stream.ToolCallStart{ID: "tc1", Name: "wf_echo"},
				stream.ToolCallDelta{ID: "tc1", Delta: `{"msg": "hi"}`},
				stream.ToolCall{ID: "tc1", Name: "wf_echo", Arguments: `{"msg": "hi"}`},
				stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
			},
			{
				stream.TextDelta{Text: "done"},
				stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
			},
		},
	}
	providers.Register(&fakeProvider{name: "wf-fake-provider", model: "wf-fake-model", client: fake})
	t.Cleanup(func() {
		// providers.Default has no unregister; leaving a same-named provider
		// registered across tests is harmless (each test uses a unique
		// name), but future tests reusing "wf-fake-provider" would collide.
	})

	engine := NewEngine(context.Background(), "test prompt")
	engine.luaNewSession(engine.L)
	sessionVal := engine.L.Get(-1)
	engine.L.Pop(1)
	sessionTbl, ok := sessionVal.(*lua.LTable)
	if !ok {
		t.Fatal("tyci.new_session must push a Lua table")
	}
	engine.L.SetField(sessionTbl, "model", lua.LString("wf-fake-provider/wf-fake-model"))
	// luaNewSession stores the actual model on the luaSession keyed by
	// _session_key; update that too so sessionAwait resolves the fake model.
	key := engine.L.GetField(sessionTbl, "_session_key").String()
	sess := engine.sessions[key]
	sess.model = "wf-fake-provider/wf-fake-model"

	engine.L.Push(engine.L.NewFunction(engine.sessionAwait))
	engine.L.Push(sessionTbl)
	if err := engine.L.PCall(1, 1, nil); err != nil {
		t.Fatalf("session:await failed: %v", err)
	}
	reply := engine.L.Get(-1)
	engine.L.Pop(1)

	replyTbl, ok := reply.(*lua.LTable)
	if !ok {
		t.Fatalf("session:await must return a table, got %T", reply)
	}
	content := engine.L.GetField(replyTbl, "content").String()
	if content != "done" {
		t.Errorf("reply.content = %q, want %q", content, "done")
	}

	// The fake recorded every request it received; the second one (issued
	// after the tool call) must carry a tool_result produced by the real
	// wf_echo tool ("echo:hi") — proof the tool was actually dispatched
	// through the global registry, not merely offered in the schema.
	reqs := fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model received %d requests, want 2 (initial + post-tool-call)", len(reqs))
	}
	found := false
	for _, msg := range reqs[1].Messages {
		if msg.Role != "toolResult" {
			continue
		}
		for _, block := range msg.Content {
			if block.Text == "echo:hi" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("second request did not carry the wf_echo tool result; requests: %+v", reqs[1].Messages)
	}

	// The schema offered to the model must include the built-in "wf_echo"
	// tool wired for this session — a session with no Schema would offer no
	// tools at all, which is exactly the gap this test guards against.
	if len(reqs[0].Tools) == 0 {
		t.Fatal("first request carried no tool schema; session tools/schema are not wired")
	}
}
