package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/stream"
)

// TestCanDrainLastIteration checks that a late result is not drained on the
// last allowed iteration. It stays queued for the next turn.
func TestCanDrainLastIteration(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	sub := b.Subscribe("test", bus.Filter{To: testSelf, Kinds: []bus.Kind{bus.KindToolResult}})
	defer sub.Close()
	d := newDispatcher(b, testSelf, sub)

	// "other_tool" waits 2.5s, then the call is backgrounded. It finishes
	// only after Run returns.
	release := make(chan struct{})
	runner := fakeRunner{fn: func(context.Context, string, map[string]any) (string, error) {
		<-release
		return "late text", nil
	}}
	p := &connectortest.Fake{ProviderName: "mock", ModelName: "mock-1", Turns: [][]stream.Event{{
		stream.ToolCallStart{ID: "t1", Name: "other_tool"},
		stream.ToolCall{ID: "t1", Name: "other_tool", Arguments: `{}`},
		stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
	}}}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}

	// The limit ends the run with an error. That error is expected here.
	_, _ = Run(context.Background(), p, newCaptureDisplay(), &msgs, Config{
		MaxRetries:    1,
		MaxIterations: 1,
		Tools:         runner,
		Dispatch:      d,
	})
	close(release)
	for _, m := range msgs {
		for _, c := range m.Content {
			if strings.Contains(c.Text, "tyci-notice") {
				t.Fatalf("result drained on the last iteration: %q", c.Text)
			}
		}
	}
	got := d.waitLate(context.Background())
	if len(got) != 1 || got[0].Content != "late text" {
		t.Errorf("late results = %+v, want the one result kept for the next turn", got)
	}
}
