package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/agent"
	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/crazy-goat/tyci-agent/tools"
)

// TestJobResumerAdapter_ResumedSubagentStaysInOwnSubtree pins the subtree
// rule for a resumed subagent (#503). jobResumerAdapter.Resume must set
// tools.SubagentSinkCtxKey, as the promoted /btw path does, so that
// kill_job and wait treat the resumed run as a child. A job outside its
// subtree must then be refused, and must keep running.
func TestJobResumerAdapter_ResumedSubagentStaysInOwnSubtree(t *testing.T) {
	reg, _ := withTestWiring(t)
	resetResumableForTest(t)

	// A running job that belongs to nobody in the resumed run's subtree.
	outsideCtx, stopOutside := context.WithCancel(context.Background())
	outsideStopped := make(chan struct{})
	outside := reg.Start(outsideCtx, "outside", jobs.KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-ctx.Done()
		close(outsideStopped)
		return "", false, ctx.Err()
	})
	// Wait for the outside job to end before the wiring is reset, so no
	// goroutine of this test outlives it (it would race with the next test).
	t.Cleanup(func() {
		stopOutside()
		reg.Wait(context.Background(), outside.ID, 5*time.Second)
	})

	var killResult, waitResult string
	fake := &connectortest.Fake{
		ProviderName: "resume-subtree-test",
		Script: func(turn int, req connector.Request) []stream.Event {
			switch turn {
			case 0:
				return []stream.Event{
					stream.ToolCall{ID: "tc-kill", Name: "kill_job", Arguments: `{"job_id":"` + outside.ID + `"}`},
					stream.Finish{Reason: "tool_calls"},
				}
			case 1:
				killResult = lastToolResultText(req)
				return []stream.Event{
					stream.ToolCall{ID: "tc-wait", Name: "wait", Arguments: `{"job_id":"` + outside.ID + `","seconds":1}`},
					stream.Finish{Reason: "tool_calls"},
				}
			default:
				waitResult = lastToolResultText(req)
				return []stream.Event{
					stream.TextDelta{Text: "done"},
					stream.Finish{Reason: "stop"},
				}
			}
		},
	}

	const origJobID = "resume-subtree-orig"
	stashResumable(origJobID, resumableEntry{
		msgs: []connector.Message{
			{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}},
		},
		mc: fake,
		cfg: agent.Config{
			MaxRetries: 1,
			Tools:      &subagentToolRunner{},
			Schema:     tools.GetSubagentToolsSchemaJSONForAtDepth(nil, 1),
		},
		todoAgentID: origJobID,
		depth:       1,
	})

	ctx := connector.WithModelClient(context.Background(), fake)
	handle, err := (jobResumerAdapter{reg: reg}).Resume(ctx, origJobID, "try to stop the outside job")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if _, ok := reg.Wait(context.Background(), handle.ID(), 5*time.Second); !ok {
		t.Fatal("resumed job never finished")
	}

	const refusal = "not within your own subtree"
	if !strings.Contains(killResult, refusal) {
		t.Errorf("kill_job on a job outside the subtree must be refused, got %q", killResult)
	}
	if !strings.Contains(waitResult, refusal) {
		t.Errorf("wait on a job outside the subtree must be refused, got %q", waitResult)
	}
	select {
	case <-outsideStopped:
		t.Fatal("resumed subagent cancelled a job outside its subtree")
	default:
	}
}
