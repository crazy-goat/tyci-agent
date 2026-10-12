package session

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestReplayAcceptsPlaceholderWithoutNotice checks that a session whose tool
// result is still the placeholder (the call was never finished) replays.
func TestReplayAcceptsPlaceholderWithoutNotice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	s, err := Open(path, "/tmp", "mock-1", "mock")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "go"}}, nil); err != nil {
		t.Fatalf("write user: %v", err)
	}
	if err := s.WriteMessage("assistant", []ContentBlock{{Type: "toolCall", ID: "t1", Name: "subagent", Arguments: []byte(`{}`)}}, nil); err != nil {
		t.Fatalf("write call: %v", err)
	}
	if err := s.WriteMessage("toolResult", []ContentBlock{{Type: "text", Text: "started, id=c1", ToolCallID: "t1", ToolName: "subagent"}}, nil); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, msgs, _, _, err := LoadForReplay(path)
	if err != nil {
		t.Fatalf("LoadForReplay: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	if got := msgs[2].Content[0].Text; !strings.Contains(got, "started, id=c1") {
		t.Errorf("placeholder not replayed: %q", got)
	}
}
