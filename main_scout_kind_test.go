package main

import (
	"context"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

// TestAgentRunnerRun_ScoutModeRecordsUnderScoutKind pins item 21 round B: a
// scout's usage must be recorded under ledger.Scout, not ledger.Subagent, so
// its cost is countable on its own (see ledger.Kind's doc comment). An
// ordinary subagent (ScoutMode false) must keep recording as ledger.Subagent
// — this is the one branch point between the two.
func TestAgentRunnerRun_ScoutModeRecordsUnderScoutKind(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scoutMode bool
		want      ledger.Kind
	}{
		{"scout", true, ledger.Scout},
		{"ordinary subagent", false, ledger.Subagent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger.Reset()
			t.Cleanup(ledger.Reset)

			fake := &connectortest.Fake{Turns: [][]stream.Event{{
				stream.TextDelta{Text: "child answer"},
				stream.Finish{Reason: "stop", Usage: stream.Usage{Input: 10, Output: 5}},
			}}}
			ctx := connector.WithModelClient(context.Background(), fake)
			_, err := (&agentRunner{}).run(ctx, "do the thing", "", "", tools.SubagentOptions{ScoutMode: tc.scoutMode})
			if err != nil {
				t.Fatalf("run() error: %v", err)
			}

			snap := ledger.Get()
			var gotKind ledger.Kind
			found := false
			for _, r := range snap.Rows {
				if r.Kind == ledger.Main {
					continue
				}
				gotKind = r.Kind
				found = true
			}
			if !found {
				t.Fatalf("no non-Main row recorded, want one of kind %v: %+v", tc.want, snap.Rows)
			}
			if gotKind != tc.want {
				t.Fatalf("recorded Kind = %v, want %v", gotKind, tc.want)
			}
		})
	}
}
