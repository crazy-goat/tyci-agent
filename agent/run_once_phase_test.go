package agent

import (
	"context"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/stream"
)

// phaseRecordingDisplay is silentDisplay plus PhaseSink, recording every
// Phase() call in order so a test can assert on both what was called and
// when relative to the other Sink methods.
type phaseRecordingDisplay struct {
	silentDisplay
	phases []string
}

func (d *phaseRecordingDisplay) Phase(name string) {
	d.phases = append(d.phases, name)
}

// TestRunOnce_PhaseSink_SendingBeforeStream is the one phase runOnce sets
// itself rather than sourcing from a stream event: "sending" has to be
// visible from the moment Request() fires, since nothing about the
// transport marks "the request is now being built" — WroteRequest only
// fires once the body is already written.
func TestRunOnce_PhaseSink_SendingBeforeStream(t *testing.T) {
	fake := connectortest.Text("hi")
	d := &phaseRecordingDisplay{}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}}
	var total stream.Usage

	if _, _, _, err := runOnce(context.Background(), fake, d, &msgs, Config{}, &total); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if len(d.phases) == 0 || d.phases[0] != "sending" {
		t.Fatalf("expected the first Phase call to be %q, got %v", "sending", d.phases)
	}
}

// TestRunOnce_PhaseSink_ForwardsTransportEvents is the forwarding contract
// item 55 asks for: stream.RequestSent and stream.ResponseStarted, emitted
// by the streamer's httptrace hooks (api/*.go), must reach a PhaseSink as
// "waiting" and "thinking" respectively, in order, before whatever content
// the round actually produces.
func TestRunOnce_PhaseSink_ForwardsTransportEvents(t *testing.T) {
	fake := &connectortest.Fake{
		Turns: [][]stream.Event{{
			stream.RequestSent{},
			stream.ResponseStarted{},
			stream.TextDelta{Text: "hi"},
			stream.Finish{Reason: "stop"},
		}},
	}
	d := &phaseRecordingDisplay{}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}}
	var total stream.Usage

	if _, _, _, err := runOnce(context.Background(), fake, d, &msgs, Config{}, &total); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	want := []string{"sending", "waiting", "thinking"}
	if len(d.phases) != len(want) {
		t.Fatalf("Phase calls = %v, want %v", d.phases, want)
	}
	for i, w := range want {
		if d.phases[i] != w {
			t.Errorf("Phase[%d] = %q, want %q (full sequence: %v)", i, d.phases[i], w, d.phases)
		}
	}
}

// TestRunOnce_PhaseSink_OptionalForOtherSinks pins the "asserted at the call
// site" design: a Sink that does not implement PhaseSink (silentDisplay,
// like Minimal/Terminal/BtwSink in production) must not be affected by the
// stream carrying RequestSent/ResponseStarted events — runOnce must not
// panic on a failed type assertion, and the round must complete normally.
func TestRunOnce_PhaseSink_OptionalForOtherSinks(t *testing.T) {
	fake := &connectortest.Fake{
		Turns: [][]stream.Event{{
			stream.RequestSent{},
			stream.ResponseStarted{},
			stream.TextDelta{Text: "hi"},
			stream.Finish{Reason: "stop"},
		}},
	}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}}
	var total stream.Usage

	if _, _, _, err := runOnce(context.Background(), fake, &silentDisplay{}, &msgs, Config{}, &total); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
}
