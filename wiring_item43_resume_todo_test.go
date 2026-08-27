package main

// Item 43: a resumed job's todo list used to start empty even though the
// forked transcript's own past todo(...) calls/results still named ids from
// the ORIGINAL job's list — a resumed agent reading its own history would
// see those ids silently resolve to nothing.
//
// The first fix attempt used the job id itself as the source list to copy
// forward. That is wrong for an ordinary subagent: subagent.go's
// runSingleTask gives every subagent call its OWN TodoAgentCtxKey id,
// distinct from (and taking priority over) its job id — see
// todoAgentIDFromCtx and resumableEntry.todoAgentID's doc comments. This
// test drives the real production wiring (withTestWiring, same shape as
// wiring_b1_resume_mailbox_test.go's equivalent mailbox fix) through an
// ordinary async `subagent` spawn, so it actually exercises that mismatch
// rather than the lower-level tools.CopyTodoListForResume tests, which use
// hand-picked ids and would pass either way.

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

func TestWiring_Item43_ResumedJobCarriesForwardTodoList(t *testing.T) {
	reg, _ := withTestWiring(t)

	childFake := &connectortest.Fake{ProviderName: "i43-child"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		switch turn {
		case 0:
			// The original spawn plans one item under its own TodoAgentCtxKey
			// id — NOT the job id.
			return []stream.Event{
				stream.ToolCall{ID: "add-1", Name: "todo", Arguments: `{"action":"add","content":"alpha"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		case 1:
			// The original spawn's own finishing turn.
			return []stream.Event{
				stream.TextDelta{Text: "first done"},
				stream.Finish{Reason: "stop"},
			}
		default:
			// The resumed run's only turn: finishes cleanly, no need to
			// touch todo itself — the test checks the list directly.
			return []stream.Event{
				stream.TextDelta{Text: "second done"},
				stream.Finish{Reason: "stop"},
			}
		}
	}
	providers.Register(&fixedClientProvider{name: "i43-child-prov", client: childFake})

	ctx := connector.WithModelClient(context.Background(), connectortest.Text("n/a"))
	spawnRes := tools.RunTool(ctx, "subagent", map[string]any{
		"task": "plan something", "async": true, "model": "i43-child-prov/m",
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
	if newJobID == origJobID {
		t.Fatalf("expected a new, distinct job id from resume, got the original back")
	}

	final, ok := reg.Wait(context.Background(), newJobID, 2*time.Second)
	if !ok || final.Status != jobs.StatusDone {
		t.Fatalf("resumed job did not finish done: %+v", final)
	}

	// Query the resumed job's own todo list exactly as its own first tool
	// call would have resolved it: JobIDCtxKey set to newJobID, no
	// TodoAgentCtxKey (the resumed run never gets its own — see
	// jobResumerAdapter.Resume).
	listCtx := context.WithValue(context.Background(), tools.JobIDCtxKey{}, newJobID)
	listRes := tools.RunTool(listCtx, "todo", map[string]any{"action": "list"})
	if !listRes.Success {
		t.Fatalf("list on resumed job: %s", listRes.Error)
	}
	if !strings.Contains(listRes.Content, "alpha") {
		t.Fatalf("resumed job's todo list should carry the original subagent's item, got: %q", listRes.Content)
	}
}
