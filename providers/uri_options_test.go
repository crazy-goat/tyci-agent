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
