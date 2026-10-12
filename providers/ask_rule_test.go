package providers

import (
	"strings"
	"testing"
)

// TestPromptsHaveAskRule checks that a child prompt with ask_parent tells the
// model that ask_parent does not block, and that wait is for the answer only
// when the model cannot continue without it.
func TestPromptsHaveAskRule(t *testing.T) {
	const rule = "ask_parent does not block. `wait` for the answer only when you cannot continue without it."
	if !strings.Contains(buildSubagentContractNote(true), rule) {
		t.Fatalf("child contract note lacks the ask rule %q", rule)
	}
	if strings.Contains(buildSubagentContractNote(false), "ask_parent does not block") {
		t.Fatal("contract note without ask_parent must not state the ask rule")
	}
}
