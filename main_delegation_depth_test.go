package main

// Item 21 ("Grandchildren" / scout): the depth-derived delegation gate.
//
// These tests drive the REAL production wiring (withTestWiring/wireTools,
// the real agentRunner, the real subagentToolRunner) rather than hand-rolled
// mocks, so they exercise exactly the code path a live session runs.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// schemaToolNames extracts the set of tool names offered in a marshaled
// tool schema, for asserting presence/absence of "subagent"/"scout".
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

// TestDepthGate_SubagentAndScoutAcrossDepths pins item 21's core rule —
// "subagent" only at depth 0, "scout" only at depth 1-3, neither at depth
// 4 — against both halves of the invariant the task requires stay in
// lockstep: the schema a depth is actually offered
// (tools.GetTopLevelToolsSchemaJSON for depth 0, tools.
// GetSubagentToolsSchemaJSONForAtDepth for depth >= 1) and what the real
// runtime dispatch (tools.RunTool directly for depth 0 — cmd_interactive.
// go's toolsAdapter.Run, the production top-level path, is a bare
// passthrough to RunTool with no depth check of its own, so calling
// RunTool here exercises exactly the same enforcement; subagentToolRunner.
// Run for depth >= 1, the real per-child gate) actually permits for the
// same depth.
func TestDepthGate_SubagentAndScoutAcrossDepths(t *testing.T) {
	withTestWiring(t)

	for _, depth := range []int{0, 1, 2, 3, 4} {
		var offered map[string]bool
		if depth == 0 {
			offered = schemaToolNames(t, tools.GetTopLevelToolsSchemaJSON())
		} else {
			offered = schemaToolNames(t, tools.GetSubagentToolsSchemaJSONForAtDepth(nil, depth))
		}

		for _, name := range []string{"subagent", "scout"} {
			want := tools.ToolAllowedAtDepth(depth, name)
			if offered[name] != want {
				t.Errorf("depth %d: schema offers %q=%v, want %v", depth, name, offered[name], want)
			}

			// A fresh single-turn fake per attempt: connectortest.Text
			// replays its one canned answer exactly once, and this loop
			// dispatches a real, successful child (when the depth gate
			// allows it) more than once across depths/names.
			ctx := connector.WithModelClient(tools.WithDepth(context.Background(), depth), connectortest.Text("ok"))

			var success bool
			var errMsg string
			if depth == 0 {
				res := tools.RunTool(ctx, name, map[string]any{"task": "hi"})
				success, errMsg = res.Success, res.Error
			} else {
				r := &subagentToolRunner{}
				_, err := r.Run(ctx, name, map[string]any{"task": "hi"})
				success = err == nil
				if err != nil {
					errMsg = err.Error()
				}
			}
			if success != want {
				t.Errorf("depth %d: runtime gate allowed %q=%v, want %v (err=%q)", depth, name, success, want, errMsg)
			}
		}
	}
}

// TestScoutWhitelist_AllowsFind is the positive half of scout's tool
// profile: "find" (this codebase's glob/grep) must go through.
func TestScoutWhitelist_AllowsFind(t *testing.T) {
	withTestWiring(t)

	fake := &connectortest.Fake{ProviderName: "scout-find-test"}
	fake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.ToolCall{ID: "tc1", Name: "find", Arguments: `{"method":"glob","pattern":"*.go","output":"count"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		return []stream.Event{
			stream.TextDelta{Text: "done: " + lastToolResultText(req)},
			stream.Finish{Reason: "stop"},
		}
	}

	ctx := connector.WithModelClient(tools.WithDepth(context.Background(), 1), fake)
	res := tools.RunTool(ctx, "scout", map[string]any{"task": "count go files"})
	if !res.Success {
		t.Fatalf("expected the scout task to succeed, got error: %s", res.Error)
	}
	if strings.Contains(res.Content, "not available to a scout") {
		t.Errorf("expected \"find\" to be allowed inside a scout, got a denial in the transcript: %q", res.Content)
	}
}

// TestScoutWhitelist_DeniesWrite is the negative half: scout's profile has
// no write-capable tool at all.
func TestScoutWhitelist_DeniesWrite(t *testing.T) {
	withTestWiring(t)

	fake := &connectortest.Fake{ProviderName: "scout-write-test"}
	fake.Script = func(turn int, req connector.Request) []stream.Event {
		if turn == 0 {
			return []stream.Event{
				stream.ToolCall{ID: "tc1", Name: "write", Arguments: `{"path":"/tmp/scout-should-not-write","content":"x"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		return []stream.Event{
			stream.TextDelta{Text: "result: " + lastToolResultText(req)},
			stream.Finish{Reason: "stop"},
		}
	}

	ctx := connector.WithModelClient(tools.WithDepth(context.Background(), 1), fake)
	res := tools.RunTool(ctx, "scout", map[string]any{"task": "try to write a file"})
	if !res.Success {
		t.Fatalf("expected the scout call itself to succeed (the denial is inside its own transcript), got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "not available to a scout") {
		t.Errorf("expected the transcript to show \"write\" denied, got: %q", res.Content)
	}
}

// TestScout_HittingMaxIterationsCapReportsRealNumber pins a regression this
// item's own change could have introduced: agentRunner.run's cutoff
// message used to hard-code tools.DefaultSubagentMaxIterations (-1) for
// EVERY truncated child, which was harmless while MaxIterations really was
// always unlimited (agent.ErrMaxIterations could never fire). Now that
// scout sets a real cap (15), a truncated scout must report THAT number,
// not the -1 sentinel.
func TestScout_HittingMaxIterationsCapReportsRealNumber(t *testing.T) {
	withTestWiring(t)

	fake := &connectortest.Fake{ProviderName: "scout-cap-test"}
	fake.Script = func(turn int, req connector.Request) []stream.Event {
		// Every turn calls an allowed, harmless tool and asks for another
		// tool_calls round — never "stop" — so the loop only ends by
		// hitting scout's own 15-iteration cap.
		return []stream.Event{
			stream.ToolCall{ID: "tc", Name: "find", Arguments: `{"method":"glob","pattern":"*.go","output":"count"}`},
			stream.Finish{Reason: "tool_calls"},
		}
	}

	ctx := connector.WithModelClient(tools.WithDepth(context.Background(), 1), fake)
	res := tools.RunTool(ctx, "scout", map[string]any{"task": "loop forever"})
	// agent.Run's own iteration-cap warning ("Agent executed N tool-call
	// iterations...") is text, so this surfaces as a partial success
	// (Truncated=true), not a bare failure — same as an ordinary subagent
	// hitting a cutoff with something to show.
	if !res.Success || !res.Truncated {
		t.Fatalf("expected a truncated partial success, got success=%v truncated=%v content=%q error=%q", res.Success, res.Truncated, res.Content, res.Error)
	}
	if !strings.Contains(res.Content, "15-iteration") {
		t.Errorf("expected the cutoff note to name scout's real cap (15), got: %q", res.Content)
	}
	if strings.Contains(res.Content, "-1-iteration") {
		t.Errorf("cutoff note regressed to the unlimited-subagent sentinel: %q", res.Content)
	}
}

// TestJobResumerAdapter_RestoresDepthForScoutSchemaGateAgreement is the
// regression test for review finding 3: jobResumerAdapter.Resume (btw.go)
// used to replay a stashed resumableEntry's cfg (built once at the
// ORIGINAL run's depth, e.g. depth 1 for an ordinary subagent — see
// resumableEntry.depth's doc comment, main.go) under a runCtx derived from
// the "resume" tool call's own ctx, without ever calling tools.WithDepth.
// A resumed depth-1 conversation's stashed cfg.Schema still offered
// "scout" (correct for depth 1), but the runtime gate saw depth 0 (ctx
// never re-stamped) and refused it. The fix restores entry.depth via
// tools.WithDepth before agent.Run — this test stashes an entry AT DEPTH 1
// directly (bypassing a full async-spawn dance, which is exercised
// elsewhere) and drives a real Resume through withTestWiring's registry,
// confirming both halves agree: the resumed conversation's schema offered
// "scout", and calling it actually ran instead of being refused with a
// nesting-depth error.
func TestJobResumerAdapter_RestoresDepthForScoutSchemaGateAgreement(t *testing.T) {
	reg, _ := withTestWiring(t)
	resetResumableForTest(t)

	fake := &connectortest.Fake{
		ProviderName: "resume-scout-depth-test",
		Script: func(turn int, req connector.Request) []stream.Event {
			switch turn {
			case 0:
				// The resumed conversation's own first turn: call scout.
				return []stream.Event{
					stream.ToolCall{ID: "tc-scout", Name: "scout", Arguments: `{"task":"look around"}`},
					stream.Finish{Reason: "tool_calls"},
				}
			case 1:
				// Scout's own nested turn (same fake — scout has no model
				// override, so it inherits whatever ctx's ambient model
				// client is; the test's ctx below uses this same fake).
				return []stream.Event{
					stream.TextDelta{Text: "scouted"},
					stream.Finish{Reason: "stop"},
				}
			default:
				return []stream.Event{
					stream.TextDelta{Text: "done: " + lastToolResultText(req)},
					stream.Finish{Reason: "stop"},
				}
			}
		},
	}

	const origJobID = "resume-scout-depth-orig"
	stashResumable(origJobID, resumableEntry{
		msgs: []connector.Message{
			{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}},
		},
		mc: fake,
		cfg: agent.Config{
			MaxRetries: 1,
			Tools:      &subagentToolRunner{},
			// Built once at the original (depth-1) spawn, exactly like
			// agentRunner.run does — offers "scout" because depth 1 permits
			// it.
			Schema: tools.GetSubagentToolsSchemaJSONForAtDepth(nil, 1),
		},
		todoAgentID: "resume-scout-depth-orig",
		depth:       1,
	})

	ctx := connector.WithModelClient(context.Background(), fake)
	handle, err := (jobResumerAdapter{reg: reg}).Resume(ctx, origJobID, "now look around with scout")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	job, ok := reg.Wait(context.Background(), handle.ID(), 5*time.Second)
	if !ok {
		t.Fatal("resumed job never finished")
	}

	requests := fake.Requests()
	if len(requests) < 2 {
		t.Fatalf("expected at least 2 model requests (resumed turn + scout's own), got %d", len(requests))
	}
	// requests[0] is the ORIGINAL run's own initial request (never actually
	// streamed past turn 0 above, since stashing bypassed running it) —
	// the resumed conversation's own first request is requests[1] here
	// because Script is shared and call-count-indexed, so re-check by
	// content instead of a fixed index: whichever request the resumed job
	// actually sent must offer "scout".
	sawScoutOffered := false
	for _, req := range requests {
		if names := schemaToolNames(t, req.Tools); names["scout"] {
			sawScoutOffered = true
			break
		}
	}
	if !sawScoutOffered {
		t.Fatalf("expected the resumed conversation's schema to offer \"scout\" (depth 1), got requests: %+v", requests)
	}
	if strings.Contains(job.Result, "not available at nesting depth") {
		t.Fatalf("schema offered \"scout\" but the resumed runtime gate refused it: %q", job.Result)
	}
	if !strings.Contains(job.Result, "done: scouted") {
		t.Fatalf("expected the scout call to actually run and its result to reach the final answer, got %q", job.Result)
	}
}

// TestScoutWhitelist_DeniesSubagentAndAgents pins that a scout cannot reach
// a full subagent or the "agents" discovery tool — only "scout" itself
// (governed purely by the depth gate, not this whitelist) may nest.
func TestScoutWhitelist_DeniesSubagentAndAgents(t *testing.T) {
	withTestWiring(t)

	for _, blocked := range []string{"subagent", "agents"} {
		fake := &connectortest.Fake{ProviderName: "scout-deny-" + blocked}
		args := `{"task":"x"}`
		if blocked == "agents" {
			args = `{}`
		}
		fake.Script = func(turn int, req connector.Request) []stream.Event {
			if turn == 0 {
				return []stream.Event{
					stream.ToolCall{ID: "tc1", Name: blocked, Arguments: args},
					stream.Finish{Reason: "tool_calls"},
				}
			}
			return []stream.Event{
				stream.TextDelta{Text: "result: " + lastToolResultText(req)},
				stream.Finish{Reason: "stop"},
			}
		}

		ctx := connector.WithModelClient(tools.WithDepth(context.Background(), 1), fake)
		res := tools.RunTool(ctx, "scout", map[string]any{"task": "try " + blocked})
		if !res.Success {
			t.Fatalf("%s: expected the scout call itself to succeed, got error: %s", blocked, res.Error)
		}
		if !strings.Contains(res.Content, "not available") {
			t.Errorf("%s: expected the transcript to show it denied, got: %q", blocked, res.Content)
		}
	}
}
