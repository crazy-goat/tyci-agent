package session

import (
	"path/filepath"
	"testing"

	"github.com/decodo/tyci/connector"
)

// ─── dropTrailingUnansweredToolCalls: the fork-only half of the repair ─────
//
// This is deliberately NOT exercised through SanitizeMessageSequence (see
// its doc comment): that shared function backs ordinary /resume too, where a
// trailing unanswered tool call is a legitimate crash-recovery state, not
// something to discard. Only the fork paths (ForkAtIndex/ForkAtEventID,
// exercised further down) apply this extra repair.

func TestDropTrailingUnansweredToolCalls_StripsCallKeepsText(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "list files"}}},
		{Role: "assistant", Content: []connector.ContentBlock{
			{Type: "text", Text: "running it"},
			{Type: "toolCall", ID: "call-1", Name: "bash", Arguments: []byte(`{"cmd":"ls"}`)},
		}},
	}

	got := dropTrailingUnansweredToolCalls(msgs)

	if len(got) != 2 {
		t.Fatalf("expected the assistant message to survive with its toolCall stripped, got %d messages: %#v", len(got), got)
	}
	last := got[1]
	if last.Role != "assistant" {
		t.Fatalf("expected last message to remain assistant, got %q", last.Role)
	}
	for _, b := range last.Content {
		if b.Type == "toolCall" {
			t.Fatalf("expected the dangling toolCall block to be stripped, got %#v", last.Content)
		}
	}
	if len(last.Content) != 1 || last.Content[0].Text != "running it" {
		t.Fatalf("expected the text block to survive untouched, got %#v", last.Content)
	}
}

// TestDropTrailingUnansweredToolCalls_DropsWholeMessageWhenOnlyToolCalls
// covers the case where the trailing assistant message is NOTHING but tool
// calls: once they are stripped there is nothing left, so the whole message
// must go rather than leaving an empty assistant turn dangling at the end.
func TestDropTrailingUnansweredToolCalls_DropsWholeMessageWhenOnlyToolCalls(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "list files"}}},
		{Role: "assistant", Content: []connector.ContentBlock{
			{Type: "toolCall", ID: "call-1", Name: "bash", Arguments: []byte(`{"cmd":"ls"}`)},
		}},
	}

	got := dropTrailingUnansweredToolCalls(msgs)

	if len(got) != 1 {
		t.Fatalf("expected the tool-calls-only assistant message to be dropped entirely, got %d messages: %#v", len(got), got)
	}
	if got[0].Role != "user" {
		t.Fatalf("expected only the user message to survive, got %#v", got)
	}
}

// TestSanitizeMessageSequence_AnsweredToolCallSurvivesIntact is the control:
// a toolCall that DOES have its result in the sequence must not be touched.
func TestSanitizeMessageSequence_AnsweredToolCallSurvivesIntact(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "list files"}}},
		{Role: "assistant", Content: []connector.ContentBlock{
			{Type: "toolCall", ID: "call-1", Name: "bash", Arguments: []byte(`{"cmd":"ls"}`)},
		}},
		{Role: "toolResult", Content: []connector.ContentBlock{
			{Type: "text", Text: "file1\nfile2", ToolCallID: "call-1", ToolName: "bash"},
		}},
	}

	got := SanitizeMessageSequence(msgs)

	if len(got) != 3 {
		t.Fatalf("expected all 3 messages to survive, got %d: %#v", len(got), got)
	}
	foundCall := false
	for _, b := range got[1].Content {
		if b.Type == "toolCall" && b.ID == "call-1" {
			foundCall = true
		}
	}
	if !foundCall {
		t.Fatalf("expected the answered toolCall to survive, got %#v", got[1])
	}
}

// ─── ForkMessages / ForkMessagesWithTurn ────────────────────────────────────

func TestForkMessages_DoesNotAliasOriginal(t *testing.T) {
	orig := make([]connector.Message, 0, 4)
	orig = append(orig, connector.Message{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}})

	forked := ForkMessages(orig)
	forked = append(forked, connector.Message{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "hello"}}})

	if len(orig) != 1 {
		t.Fatalf("appending to the fork must not grow the original, got len %d", len(orig))
	}
}

func TestForkMessagesWithTurn_AppendsUserTurn(t *testing.T) {
	orig := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}},
	}
	got := ForkMessagesWithTurn(orig, "follow-up question")
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[1].Role != "user" || got[1].Content[0].Text != "follow-up question" {
		t.Fatalf("unexpected appended turn: %#v", got[1])
	}
}

// ─── ForkAtIndex: live, in-memory transcript-index addressing ─────────────

func TestForkAtIndex_CleanCut(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "q1"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "a1"}}},
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "q2"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "a2"}}},
	}

	got, err := ForkAtIndex(msgs, 2)
	if err != nil {
		t.Fatalf("ForkAtIndex() error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(got), got)
	}
	if got[0].Content[0].Text != "q1" || got[1].Content[0].Text != "a1" {
		t.Fatalf("unexpected fork content: %#v", got)
	}

	// Independent MESSAGE slice: appending to the original past the cut
	// must not leak into the fork (Content blocks are still shared per
	// message — see ForkMessages' doc comment — so this checks slice
	// independence, not a deep copy of every block).
	msgs = append(msgs, connector.Message{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "q3"}}})
	if len(got) != 2 {
		t.Fatalf("ForkAtIndex must not alias the original message slice, got len %d after appending to the original", len(got))
	}
}

func TestForkAtIndex_LandsMidToolCallResultPair_SanitizesTrailingCall(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "run ls"}}},
		{Role: "assistant", Content: []connector.ContentBlock{
			{Type: "text", Text: "running it"},
			{Type: "toolCall", ID: "call-1", Name: "bash", Arguments: []byte(`{"cmd":"ls"}`)},
		}},
		{Role: "toolResult", Content: []connector.ContentBlock{
			{Type: "text", Text: "file1", ToolCallID: "call-1", ToolName: "bash"},
		}},
	}

	// Cut right after the assistant's tool call, before its result — index 2
	// keeps [user, assistant] but not the toolResult.
	got, err := ForkAtIndex(msgs, 2)
	if err != nil {
		t.Fatalf("ForkAtIndex() error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages after sanitization (assistant's text block survives), got %d: %#v", len(got), got)
	}
	for _, b := range got[1].Content {
		if b.Type == "toolCall" {
			t.Fatalf("expected the dangling toolCall to be sanitized away, got %#v", got[1])
		}
	}
}

func TestForkAtIndex_OutOfRange(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}},
	}
	if _, err := ForkAtIndex(msgs, -1); err == nil {
		t.Error("expected error for negative index")
	}
	if _, err := ForkAtIndex(msgs, 2); err == nil {
		t.Error("expected error for index beyond length")
	}
	if _, err := ForkAtIndex(msgs, 0); err != nil {
		t.Errorf("index 0 (empty prefix) should be valid, got error: %v", err)
	}
	if _, err := ForkAtIndex(msgs, len(msgs)); err != nil {
		t.Errorf("index == len(msgs) should be valid, got error: %v", err)
	}
}

// ─── ForkAtEventID: persisted-session event-id addressing ─────────────────

// buildTestSession writes a small session file with a user turn, an
// assistant turn with a tool call, and its tool result, returning the path
// and the three events' ids in write order.
func buildTestSession(t *testing.T) (path string, ids []string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "test.jsonl")

	s, err := Open(path, dir, "gpt-4", "openai")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "run ls"}}, nil); err != nil {
		t.Fatalf("WriteMessage(user) error: %v", err)
	}
	if err := s.WriteMessage("assistant", []ContentBlock{
		{Type: "text", Text: "running it"},
		{Type: "toolCall", ID: "call-1", Name: "bash", Arguments: []byte(`{"cmd":"ls"}`)},
	}, nil); err != nil {
		t.Fatalf("WriteMessage(assistant) error: %v", err)
	}
	if err := s.WriteMessage("toolResult", []ContentBlock{
		{Type: "text", Text: "file1", ToolCallID: "call-1", ToolName: "bash"},
	}, nil); err != nil {
		t.Fatalf("WriteMessage(toolResult) error: %v", err)
	}

	lines, err := parseSessionFile(path)
	if err != nil {
		t.Fatalf("parseSessionFile() error: %v", err)
	}
	for _, l := range lines {
		if l.MsgType == "header" {
			continue
		}
		id, ok := eventLineID(l.Raw)
		if !ok {
			t.Fatalf("could not extract event id from line: %s", l.Raw)
		}
		ids = append(ids, id)
	}
	return path, ids
}

func TestForkAtEventID_CleanCut(t *testing.T) {
	path, ids := buildTestSession(t)
	if len(ids) != 3 {
		t.Fatalf("expected 3 events, got %d", len(ids))
	}

	// Cut at the toolResult event: keeps the full, clean turn.
	got, err := ForkAtEventID(path, ids[2])
	if err != nil {
		t.Fatalf("ForkAtEventID() error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d: %#v", len(got), got)
	}
	if got[0].Role != "user" || got[1].Role != "assistant" || got[2].Role != "toolResult" {
		t.Fatalf("unexpected roles: %#v", got)
	}
}

func TestForkAtEventID_LandsMidToolCallResultPair_SanitizesTrailingCall(t *testing.T) {
	path, ids := buildTestSession(t)

	// Cut at the assistant event (id[1]): keeps the tool call but not its
	// result — must repair the dangling call.
	got, err := ForkAtEventID(path, ids[1])
	if err != nil {
		t.Fatalf("ForkAtEventID() error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages (user, assistant with toolCall stripped), got %d: %#v", len(got), got)
	}
	for _, b := range got[1].Content {
		if b.Type == "toolCall" {
			t.Fatalf("expected the dangling toolCall to be sanitized away, got %#v", got[1])
		}
	}
}

func TestForkAtEventID_UnknownID(t *testing.T) {
	path, _ := buildTestSession(t)
	if _, err := ForkAtEventID(path, "no-such-event"); err == nil {
		t.Error("expected an error for an unknown event id")
	}
}

func TestForkAtEventID_EmptyID(t *testing.T) {
	path, _ := buildTestSession(t)
	if _, err := ForkAtEventID(path, ""); err == nil {
		t.Error("expected an error for an empty event id")
	}
}

// ─── ContentBlocksFromConnector ─────────────────────────────────────────────

func TestContentBlocksFromConnector_PreservesFields(t *testing.T) {
	blocks := []connector.ContentBlock{
		{Type: "toolCall", ID: "call-1", Name: "bash", Arguments: []byte(`{"cmd":"ls"}`)},
		{Type: "text", Text: "hi", ToolCallID: "call-1", ToolName: "bash", IsError: true},
	}
	got := ContentBlocksFromConnector(blocks)
	if len(got) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(got))
	}
	if got[0].Type != "toolCall" || got[0].ID != "call-1" || got[0].Name != "bash" {
		t.Fatalf("toolCall block not preserved: %#v", got[0])
	}
	if got[1].Text != "hi" || got[1].ToolCallID != "call-1" || !got[1].IsError {
		t.Fatalf("text/result block not preserved: %#v", got[1])
	}
}
