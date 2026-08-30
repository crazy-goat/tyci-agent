package workflow

import (
	"context"
	"testing"

	lua "github.com/yuin/gopher-lua"

	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
)

// TestSessionAwait_RecordsLedgerUsage is the regression test for F32:
// sessionAwait's agent.Run call used to hand the session's own collector
// straight to agent.Run with no ledger.Watch wrap (unlike every other
// agent.Run call site delegating work — main.go's runSingleTask, fork.go,
// btw.go, conductor.go), so a workflow session's named-agent tokens/dollars
// were recorded nowhere: ledger.Get() never saw them.
//
// Two turns (an initial reply, no tool call needed here) each carry their
// own usage, mirroring how a real model streams Summary once per call
// (agent/run_once.go) — ledger.Watch is expected to accumulate both into
// one row rather than only the last one.
func TestSessionAwait_RecordsLedgerUsage(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)

	fake := &connectortest.Fake{
		ProviderName: "wf-ledger-fake",
		ModelName:    "wf-ledger-model",
		Turns: [][]stream.Event{
			{
				stream.TextDelta{Text: "partial "},
				stream.Finish{Usage: stream.Usage{Input: 5, Output: 2}},
			},
		},
	}
	providers.Register(&fakeProvider{name: "wf-ledger-provider", model: "wf-ledger-model", client: fake})

	engine := NewEngine(context.Background(), "test prompt")
	engine.luaNewSession(engine.L)
	sessionVal := engine.L.Get(-1)
	engine.L.Pop(1)
	sessionTbl, ok := sessionVal.(*lua.LTable)
	if !ok {
		t.Fatal("tyci.new_session must push a Lua table")
	}
	key := engine.L.GetField(sessionTbl, "_session_key").String()
	sess := engine.sessions[key]
	sess.model = "wf-ledger-provider/wf-ledger-model"

	engine.L.Push(engine.L.NewFunction(engine.sessionAwait))
	engine.L.Push(sessionTbl)
	if err := engine.L.PCall(1, 1, nil); err != nil {
		t.Fatalf("session:await failed: %v", err)
	}
	engine.L.Pop(1)

	snap := ledger.Get()
	var found *ledger.Row
	for i := range snap.Rows {
		r := &snap.Rows[i]
		if r.Provider == "wf-ledger-fake" && r.Model == "wf-ledger-model" {
			found = r
			break
		}
	}
	if found == nil {
		t.Fatalf("no ledger row recorded for wf-ledger-fake/wf-ledger-model; rows: %+v", snap.Rows)
	}
	if found.Kind != ledger.Subagent {
		t.Errorf("Kind = %v, want ledger.Subagent (a workflow session is delegated work)", found.Kind)
	}
	if found.JobID != "" {
		t.Errorf("JobID = %q, want \"\" (a workflow session has no job id of its own)", found.JobID)
	}
	if found.Usage.Input != 5 || found.Usage.Output != 2 {
		t.Errorf("Usage = %+v, want Input=5 Output=2", found.Usage)
	}
	if snap.SubagentUSD != 0 && !found.Priced {
		t.Errorf("SubagentUSD = %v with an unpriced row; want 0", snap.SubagentUSD)
	}
}
