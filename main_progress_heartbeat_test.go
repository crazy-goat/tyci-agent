package main

// Item 15 review finding: cfg.ProgressHeartbeat used to be wired for EVERY
// subagent unconditionally, but report_progress is not in alwaysAllowedTools
// (tools/toolgate.go) — so a whitelisted agent whose tools: list omits it
// (e.g. builtin "reviewer": find, read, bash, or "locator": find, read)
// could never satisfy the nudge. LastProgressAt would never advance and the
// reminder would re-fire roughly every SubagentBackgroundAfterSec for the
// rest of the run, the exact "crowd out the real conversation" outcome item
// 15 exists to avoid.
//
// This test drives a real subagent job through the actual production
// composition (withTestWiring — same JobRegistry/wireTools as wiring_test.go)
// with a tools: whitelist of ["find", "read"] (report_progress omitted), and
// asserts the harness-authored heartbeat reminder never appears anywhere in
// the child's own transcript — even though the run spans several iterations,
// each one comfortably past a shrunk SubagentBackgroundAfterSec, which would
// have triggered at least one nudge had the gate not been fixed.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

func TestWiring_Item15_WhitelistedAgentWithoutReportProgress_NeverNagged(t *testing.T) {
	withTestWiring(t)

	restore := tools.SetSubagentBackgroundAfterSecForTests(20 * time.Millisecond)
	defer restore()

	var mu sync.Mutex
	var allText []string

	const totalTurns = 4
	childFake := &connectortest.Fake{ProviderName: "i15-child", ModelName: "i15-child-model"}
	childFake.Script = func(turn int, req connector.Request) []stream.Event {
		mu.Lock()
		for _, m := range req.Messages {
			for _, c := range m.Content {
				if c.Type == "text" {
					allText = append(allText, c.Text)
				}
			}
		}
		mu.Unlock()

		if turn < totalTurns {
			// Sleep past the shrunk SubagentBackgroundAfterSec so, by the next
			// iteration boundary, a heartbeat nudge would already be due if
			// cfg.ProgressHeartbeat were (wrongly) wired for this agent.
			time.Sleep(40 * time.Millisecond)
			return []stream.Event{
				stream.ToolCall{ID: "tc", Name: "read", Arguments: `{"path":"/does/not/exist"}`},
				stream.Finish{Reason: "tool_calls"},
			}
		}
		return []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Reason: "stop"},
		}
	}

	ctx := connector.WithModelClient(context.Background(), childFake)
	opts := tools.SubagentOptions{Tools: []string{"find", "read"}} // report_progress deliberately omitted

	done := make(chan struct{})
	JobRegistry.Start(ctx, "whitelisted child", jobs.KindSubagent, "", func(runCtx context.Context, jobID string) (string, bool, error) {
		defer close(done)
		runCtx = context.WithValue(runCtx, tools.JobIDCtxKey{}, jobID)
		res, err := (&agentRunner{}).RunTaskWithSystem(runCtx, "do the thing", childFake.ModelName, "you are a test agent", opts)
		return res, false, err
	})

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the whitelisted child to finish")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(allText) == 0 {
		t.Fatal("expected at least one text block to have been sent to the child model")
	}
	for _, text := range allText {
		if strings.Contains(text, "posting a status update") {
			t.Fatalf("progress-heartbeat reminder leaked into a whitelisted child that can never call report_progress: %q", text)
		}
	}
}
