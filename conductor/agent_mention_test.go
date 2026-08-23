package conductor

import (
	"context"
	"strings"
	"testing"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/stream"
)

// ---------------------------------------------------------------------------
// extractAgentMentions
// ---------------------------------------------------------------------------

func TestExtractAgentMentions_None(t *testing.T) {
	if got := extractAgentMentions("read @file:main.go and summarize it"); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestExtractAgentMentions_Single(t *testing.T) {
	got := extractAgentMentions("continue with @agent:reviewer please")
	if want := []string{"reviewer"}; !equalStrings(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestExtractAgentMentions_StripsTrailingPunctuation(t *testing.T) {
	got := extractAgentMentions("use @agent:reviewer.")
	if want := []string{"reviewer"}; !equalStrings(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestExtractAgentMentions_MultipleDeduped(t *testing.T) {
	got := extractAgentMentions("@agent:locator then @agent:reviewer then @agent:locator again")
	if want := []string{"locator", "reviewer"}; !equalStrings(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// buildAgentMentionNote
// ---------------------------------------------------------------------------

func TestBuildAgentMentionNote_NamesTheAgentAndTheTool(t *testing.T) {
	note := buildAgentMentionNote([]string{"reviewer"})
	if !strings.Contains(note, "reviewer") {
		t.Fatalf("note does not name the agent: %q", note)
	}
	if !strings.Contains(note, "subagent") {
		t.Fatalf("note does not mention the subagent tool: %q", note)
	}
	if !strings.Contains(note, "automated") {
		t.Fatalf("note should be framed as automated, not the user: %q", note)
	}
}

// ---------------------------------------------------------------------------
// Wired into Submit
// ---------------------------------------------------------------------------

// TestSubmit_AgentMentionAppendsInstructionBlock is the integration point:
// a message containing "@agent:<name>" must reach the model with a second
// content block instructing it to continue in a subagent using that agent —
// the smallest correct mechanism for the missing UI half of item 2 (the
// engine-side subagent "agent" field already exists).
func TestSubmit_AgentMentionAppendsInstructionBlock(t *testing.T) {
	client := &connectortest.Fake{
		ProviderName: "p", ModelName: "m",
		Turns: [][]stream.Event{
			{stream.TextDelta{Text: "ok"}, stream.Finish{}},
		},
	}
	c := New(Options{Client: client, Sink: &recorder{}, Config: agent.Config{}})

	if _, err := c.Submit(context.Background(), "please continue with @agent:reviewer"); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	msgs := c.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages recorded")
	}
	user := msgs[0]
	if user.Role != "user" {
		t.Fatalf("first message role = %q, want user", user.Role)
	}
	if len(user.Content) != 2 {
		t.Fatalf("got %d content blocks, want 2 (prompt + note): %+v", len(user.Content), user.Content)
	}
	if user.Content[0].Text != "please continue with @agent:reviewer" {
		t.Fatalf("first block should be the untouched prompt, got %q", user.Content[0].Text)
	}
	if !strings.Contains(user.Content[1].Text, "reviewer") || !strings.Contains(user.Content[1].Text, "subagent") {
		t.Fatalf("second block should instruct the subagent call, got %q", user.Content[1].Text)
	}
}

// TestSubmit_NoAgentMentionIsUnaffected: the common case (no "@agent:" at
// all — including the ordinary "@file:" mention) must not grow a second
// content block. This is the regression guard for the file-completion path.
func TestSubmit_NoAgentMentionIsUnaffected(t *testing.T) {
	client := &connectortest.Fake{
		ProviderName: "p", ModelName: "m",
		Turns: [][]stream.Event{
			{stream.TextDelta{Text: "ok"}, stream.Finish{}},
		},
	}
	c := New(Options{Client: client, Sink: &recorder{}, Config: agent.Config{}})

	if _, err := c.Submit(context.Background(), "read @file:main.go and summarize it"); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	msgs := c.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages recorded")
	}
	if got := len(msgs[0].Content); got != 1 {
		t.Fatalf("got %d content blocks, want 1 (no agent mention, no note)", got)
	}
}
