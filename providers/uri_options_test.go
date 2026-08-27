package providers

import (
	"testing"

	"github.com/decodo/tyci/connector"
)

func TestURIOptionsResponsesReasoningEffort(t *testing.T) {
	got := uriOptions("openai://GPT 5.6 Luna@@api.nexos.ai/v1?api=responses&reasoning=xhigh")
	if got[connector.OptReasoningEffort] != "xhigh" {
		t.Fatalf("reasoning effort = %q, want xhigh", got[connector.OptReasoningEffort])
	}
	if _, ok := got[connector.OptReasoning]; ok {
		t.Fatalf("chat reasoning option unexpectedly present: %#v", got)
	}
}

// TestURIOptionsFallbacksOptOut covers Nexos' fallback opt-out combined with
// the Responses API selector and a reasoning effort: all three URI options
// must reach the connector Options map, none dropped or duplicated.
func TestURIOptionsFallbacksOptOut(t *testing.T) {
	got := uriOptions("openai://GPT 5.6 Luna@@api.nexos.ai/v1?api=responses&reasoning=xhigh&fallbacks=false")
	if got[connector.OptFallbacks] != "false" {
		t.Fatalf("fallbacks = %q, want false", got[connector.OptFallbacks])
	}
	if got[connector.OptReasoningEffort] != "xhigh" {
		t.Fatalf("reasoning effort = %q, want xhigh", got[connector.OptReasoningEffort])
	}
	if len(got) != 2 {
		t.Fatalf("got %d options, want 2: %#v", len(got), got)
	}
}

// TestURIOptionsFallbacksAlone covers ?fallbacks=false with no other option
// present, and confirms its absence yields a nil options map (as reasoning
// alone already did before this option existed).
func TestURIOptionsFallbacksAlone(t *testing.T) {
	got := uriOptions("openai://GPT 5.6 Luna@@api.nexos.ai/v1?fallbacks=false")
	if got[connector.OptFallbacks] != "false" {
		t.Fatalf("fallbacks = %q, want false", got[connector.OptFallbacks])
	}
	if len(got) != 1 {
		t.Fatalf("got %d options, want 1: %#v", len(got), got)
	}

	if got := uriOptions("openai://GPT 5.6 Luna@@api.nexos.ai/v1"); got != nil {
		t.Fatalf("options = %#v, want nil when no query options are present", got)
	}
}
