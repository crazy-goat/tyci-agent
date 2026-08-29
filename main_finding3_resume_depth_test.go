package main

// Review finding 3 (item 21): depth was lost on the resume path.
// jobResumerAdapter.Resume (btw.go) replays a stashed resumableEntry under
// a runCtx derived from the top-level "resume" tool call, which is depth 0
// — without restamping tools.WithDepth from the ORIGINAL run's own depth, a
// resumed depth-1+ subagent's runtime gate (subagentToolRunner.Run, which
// reads tools.DepthFromContext(ctx) fresh on every call) would silently see
// depth 0 instead. The fix stashes depth in resumableEntry (main.go) at
// every (re-)stash site and restores it via tools.WithDepth before the
// resumed run.
//
// "subagent" is not the right probe for this: main.go's subagentToolRunner.
// Run denies it TWICE independently — once via the depth check
// (tools.ToolAllowedAtDepth), and again, unconditionally regardless of
// depth, via the static tools.DenySubagentRecursion() gate it installs on
// every dispatch (subagentDeniedTools always includes "subagent"). Losing
// depth would not change that outcome, so it cannot tell the fix apart from
// the bug. "scout" can: it is NOT in subagentDeniedTools — item 21 governs
// it purely through the depth gate — so a depth-1 resumed child calling
// "scout" only succeeds if its depth genuinely still reads back as 1.
// Falling back to depth 0 (the bug) would refuse it with a nesting-depth
// error instead.
//
// This test drives the real production wiring (withTestWiring, the real
// agentRunner and jobResumerAdapter) exactly like wiring_b1_resume_mailbox_
// test.go: spawn a real depth-1 async subagent, let it finish, resume it,
// and have the RESUMED run itself call "scout" — which must actually run,
// not be refused as if it were back at depth 0.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// TestWiring_Finding3_ResumeRestampsCallerDepth pins the fix: a resumed
// depth-1 subagent that calls "scout" must still be able to (depth 1-3 is
// exactly where scout is available) — proving its depth survived the
// resume, rather than silently defaulting back to depth 0's "scout"-less
// gate.
func TestWiring_Finding3_ResumeRestampsCallerDepth(t *testing.T) {
	reg, _ := withTestWiring(t)

	childFake := &connectortest.Fake{ProviderName: "finding3-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		switch turn {
		case 0:
			// The original spawn's only turn — nothing interesting, just
			// enough for the job to finish and become resumable.
			return []stream.Event{
				stream.TextDelta{Text: "first answer"},
				stream.Finish{Reason: "stop"},
			}
		case 1:
			// The resumed run's first turn: call "scout". If depth was
			// lost (silently defaulting to 0 instead of the original
			// depth-1 child), this would be refused as a nesting-depth
			// violation; the fix must still let it through.
			return []stream.Event{
				stream.ToolCall{ID: "tc-scout", Name: "scout", Arguments: `{"task":"peek around"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		case 2:
			// scout's own nested turn, driven by this same fake client
			// (scout inherits its caller's model client) — answer
			// immediately with no further tool calls.
			return []stream.Event{
				stream.TextDelta{Text: "scouted"},
				stream.Finish{Reason: "stop"},
			}
		default:
			// The resumed run's second turn, after the scout tool result.
			return []stream.Event{
				stream.TextDelta{Text: "saw: " + lastToolResultText(req)},
				stream.Finish{Reason: "stop"},
			}
		}
	}
	providers.Register(&fixedClientProvider{name: "finding3-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	spawnRes := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "tell me something", "async": true, "model": "finding3-child-prov/m",
	})
	if !spawnRes.Success {
		t.Fatalf("spawn subagent: %s", spawnRes.Error)
	}
	origJobID := jobIDPattern.FindStringSubmatch(spawnRes.Content)[1]

	orig, ok := reg.Wait(context.Background(), origJobID, 2*time.Second)
	if !ok || orig.Status != jobs.StatusDone {
		t.Fatalf("original job did not finish done: %+v", orig)
	}

	resumeRes := tools.RunTool(context.Background(), "resume", map[string]any{
		"job_id": origJobID, "task": "continue please",
	})
	if !resumeRes.Success {
		t.Fatalf("resume: %s", resumeRes.Error)
	}
	newJobID := resumeJobID(t, resumeRes.Content)

	final, ok := reg.Wait(context.Background(), newJobID, 2*time.Second)
	if !ok || final.Status != jobs.StatusDone {
		t.Fatalf("resumed job did not finish done: ok=%v final=%+v", ok, final)
	}

	if strings.Contains(final.Result, "not available at nesting depth") {
		t.Fatalf("resumed child's \"scout\" call was refused as if depth had reset to 0: %q", final.Result)
	}
	if !strings.Contains(final.Result, "saw: scouted") {
		t.Fatalf("expected the scout call to actually run and its result to reach the resumed run's final answer, got %q", final.Result)
	}
}
