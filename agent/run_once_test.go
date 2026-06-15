package agent

import (
	"testing"

	"github.com/decodo/tyci/providers"
)

func TestRoundInputLabel_FirstRound(t *testing.T) {
	msgs := []providers.RichMessage{
		{Role: "user", Content: []providers.ContentBlock{{Type: "text", Text: "hi"}}},
	}
	if got := roundInputLabel(msgs); got != "user prompt" {
		t.Errorf("first round: got %q, want %q", got, "user prompt")
	}
}

func TestRoundInputLabel_SubsequentRound(t *testing.T) {
	msgs := []providers.RichMessage{
		{Role: "user", Content: []providers.ContentBlock{{Type: "text", Text: "hi"}}},
		{Role: "assistant", Content: []providers.ContentBlock{{Type: "toolCall"}}},
		{Role: "toolResult", Content: []providers.ContentBlock{{Type: "text", Text: "out"}}},
	}
	if got := roundInputLabel(msgs); got != "return of tool" {
		t.Errorf("subsequent round: got %q, want %q", got, "return of tool")
	}
}

func TestRoundInputLabel_EmptyMessages(t *testing.T) {
	if got := roundInputLabel(nil); got != "request" {
		t.Errorf("empty msgs: got %q, want %q", got, "request")
	}
	if got := roundInputLabel([]providers.RichMessage{}); got != "request" {
		t.Errorf("empty msgs: got %q, want %q", got, "request")
	}
}

func TestRoundInputLabel_UnknownRole(t *testing.T) {
	msgs := []providers.RichMessage{{Role: "system"}}
	if got := roundInputLabel(msgs); got != "request" {
		t.Errorf("unknown role: got %q, want %q", got, "request")
	}
}
