package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/stream"
)

func TestRoundInputLabel_FirstRound(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}},
	}
	if got := roundInputLabel(msgs); got != "user prompt" {
		t.Errorf("first round: got %q, want %q", got, "user prompt")
	}
}

func TestRoundInputLabel_SubsequentRound(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "toolCall"}}},
		{Role: "toolResult", Content: []connector.ContentBlock{{Type: "text", Text: "out"}}},
	}
	if got := roundInputLabel(msgs); got != "return of tool" {
		t.Errorf("subsequent round: got %q, want %q", got, "return of tool")
	}
}

func TestRoundInputLabel_EmptyMessages(t *testing.T) {
	if got := roundInputLabel(nil); got != "request" {
		t.Errorf("empty msgs: got %q, want %q", got, "request")
	}
	if got := roundInputLabel([]connector.Message{}); got != "request" {
		t.Errorf("empty msgs: got %q, want %q", got, "request")
	}
}

func TestRoundInputLabel_UnknownRole(t *testing.T) {
	msgs := []connector.Message{{Role: "system"}}
	if got := roundInputLabel(msgs); got != "request" {
		t.Errorf("unknown role: got %q, want %q", got, "request")
	}
}

// --- cfg.Temperature reaching connector.Request -----------------------------
//
// These pin the one hop that turns a Config field into wire data:
// runOnce builds the connector.Request handed to mc.Stream. connectortest.Fake
// records every request it is given (Requests()), so we can assert on the
// exact pointer value that crossed the boundary without needing a real
// provider or HTTP.

func TestRunOnce_TemperatureSetReachesRequest(t *testing.T) {
	fake := connectortest.Text("hi")
	temp := 0.7
	cfg := Config{Temperature: &temp}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}}
	var total stream.Usage

	_, _, _, err := runOnce(context.Background(), fake, &silentDisplay{}, &msgs, cfg, &total)
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].Temperature == nil || *reqs[0].Temperature != 0.7 {
		t.Fatalf("Request.Temperature = %v, want pointer to 0.7", reqs[0].Temperature)
	}
}

// Temperature 0.0 is a legal, meaningful value ("deterministic") and must
// still cross as a non-nil pointer — the whole point of the field being a
// *float64 instead of a plain float64.
func TestRunOnce_TemperatureZeroReachesRequestAsNonNil(t *testing.T) {
	fake := connectortest.Text("hi")
	temp := 0.0
	cfg := Config{Temperature: &temp}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}}
	var total stream.Usage

	_, _, _, err := runOnce(context.Background(), fake, &silentDisplay{}, &msgs, cfg, &total)
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].Temperature == nil {
		t.Fatal("Request.Temperature = nil, want pointer to 0.0 (0 is meaningful, not \"unset\")")
	}
	if *reqs[0].Temperature != 0.0 {
		t.Errorf("Request.Temperature = %v, want 0.0", *reqs[0].Temperature)
	}
}

func TestRunOnce_TemperatureUnsetStaysNilInRequest(t *testing.T) {
	fake := connectortest.Text("hi")
	cfg := Config{} // Temperature not set
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}}
	var total stream.Usage

	_, _, _, err := runOnce(context.Background(), fake, &silentDisplay{}, &msgs, cfg, &total)
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].Temperature != nil {
		t.Errorf("Request.Temperature = %v, want nil when cfg.Temperature is unset", *reqs[0].Temperature)
	}
}

// TestRun_TemperatureCarriesOverToFallback is the acceptance test for the
// fallback.go doc comment added alongside this: tryFallback calls runOnce
// with the SAME cfg the primary used, so cfg.Temperature must reach the
// fallback model's request too, without any extra plumbing.
func TestRun_TemperatureCarriesOverToFallback(t *testing.T) {
	primary := &connectortest.Fake{ProviderName: "temp-primary", ModelName: "p-1", StreamErr: errors.New("primary down")}
	fallback := connectortest.Text("fallback answer")

	temp := 1.3
	cfg := Config{
		MaxRetries:  1,
		Fallbacks:   []connector.ModelClient{fallback},
		Temperature: &temp,
	}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}}

	_, err := Run(context.Background(), primary, &silentDisplay{}, &msgs, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	reqs := fallback.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request to the fallback, got %d", len(reqs))
	}
	if reqs[0].Temperature == nil || *reqs[0].Temperature != 1.3 {
		t.Fatalf("fallback Request.Temperature = %v, want pointer to 1.3", reqs[0].Temperature)
	}
}
