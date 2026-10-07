package connector

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/crazy-goat/tyci-agent/stream"
)

func reasoningBody(t *testing.T, newC func(Endpoint) (Connector, error), effort string) map[string]any {
	t.Helper()
	doer := &responsesRequestDoer{}
	c, err := newC(Endpoint{BaseURL: "https://x.invalid", Path: "/p", HTTP: doer,
		Options: map[string]string{OptReasoningEffort: effort}})
	if err != nil {
		t.Fatal(err)
	}
	temp := 0.5
	_ = c.Stream(context.Background(), Request{Model: "m", MaxTokens: 100, Temperature: &temp,
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}}},
		func(stream.Event) error { return nil })
	raw, _ := io.ReadAll(doer.request.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestThinkingBudget(t *testing.T) {
	for in, want := range map[string]int{"low": 1024, "medium": 4096, "high": 16384, "2000": 2000, "xhigh": 0, "": 0} {
		if got := thinkingBudget(in); got != want {
			t.Errorf("thinkingBudget(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestOpenAIReasoningEffort(t *testing.T) {
	if got := reasoningBody(t, NewOpenAI, "high")["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort = %v", got)
	}
	if _, ok := reasoningBody(t, NewOpenAI, "2000")["reasoning_effort"]; ok {
		t.Fatal("numeric effort must not be sent as reasoning_effort")
	}
	if _, ok := reasoningBody(t, NewOpenAI, "")["reasoning_effort"]; ok {
		t.Fatal("reasoning_effort sent without option")
	}
}

func TestEndpointEffort_OptionWinsOverRequest(t *testing.T) {
	ep := Endpoint{Options: map[string]string{OptReasoningEffort: "low"}}
	if got := ep.effort(Request{Effort: "high"}); got != "low" {
		t.Fatalf("got %q", got)
	}
	if got := ep.effort(Request{}); got != "low" {
		t.Fatalf("got %q", got)
	}
}

func TestRequestEffortReachesBody(t *testing.T) {
	for name, tc := range map[string]struct {
		newC func(Endpoint) (Connector, error)
		get  func(map[string]any) any
	}{
		"openai":    {NewOpenAI, func(m map[string]any) any { return m["reasoning_effort"] }},
		"responses": {NewResponses, func(m map[string]any) any { r, _ := m["reasoning"].(map[string]any); return r["effort"] }},
	} {
		doer := &responsesRequestDoer{}
		c, err := tc.newC(Endpoint{BaseURL: "https://x.invalid", Path: "/p", HTTP: doer})
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Stream(context.Background(), Request{Model: "m", MaxTokens: 100, Effort: "high",
			Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}}},
			func(stream.Event) error { return nil })
		raw, _ := io.ReadAll(doer.request.Body)
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if got := tc.get(out); got != "high" {
			t.Errorf("%s: effort = %v", name, got)
		}
	}
}
