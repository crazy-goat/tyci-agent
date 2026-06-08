package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseSessionFile_LargeLine verifies that parseSessionFile can handle
// lines larger than the default bufio.Scanner 64KB limit.
func TestParseSessionFile_LargeLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-large.jsonl")

	// Create a session file with a 300KB line (between 64KB and 256KB read tool limit).
	bigContent := strings.Repeat("x", 300*1024)

	// Build a valid JSONL message event with a large text block.
	msg := MessageEvent{
		Type:      TypeMessage,
		ID:        "test-id",
		Timestamp: "2026-01-01T00:00:00Z",
		Message: MessagePayload{
			Role: "user",
			Content: []ContentBlock{
				{Type: "text", Text: bigContent},
			},
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Write header + message + session_end
	header := Header{Type: TypeSession, Version: 1, ID: "sess-id", Timestamp: "2026-01-01T00:00:00Z", CWD: "/tmp", Model: "test", Provider: "test"}
	hData, _ := json.Marshal(header)
	end := SessionEnd{Type: TypeSessionEnd, ID: "sess-id", Timestamp: "2026-01-01T00:00:00Z", Status: "success", ExitCode: 0}
	eData, _ := json.Marshal(end)

	fileContent := string(hData) + "\n" + string(data) + "\n" + string(eData) + "\n"
	if err := os.WriteFile(path, []byte(fileContent), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	lines, err := parseSessionFile(path)
	if err != nil {
		t.Fatalf("parseSessionFile: %v", err)
	}

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}

	// Verify the large line is intact
	if len(lines[1].Raw) < 300*1024 {
		t.Fatalf("large line too short: got %d bytes, want >= %d", len(lines[1].Raw), 300*1024)
	}
}

// TestReadAllMessages_LargeLine verifies that ReadAllMessages can handle
// lines larger than the default bufio.Scanner 64KB limit.
func TestReadAllMessages_LargeLine(t *testing.T) {
	bigContent := strings.Repeat("y", 200*1024)

	msg := MessageEvent{
		Type:      TypeMessage,
		ID:        "test-id",
		Timestamp: "2026-01-01T00:00:00Z",
		Message: MessagePayload{
			Role: "assistant",
			Content: []ContentBlock{
				{Type: "text", Text: bigContent},
			},
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	r := bytes.NewReader(data)
	msgs, err := ReadAllMessages(r)
	if err != nil {
		t.Fatalf("ReadAllMessages: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
}

// TestParseSessionFile_NormalLine verifies normal-sized lines still work.
func TestParseSessionFile_NormalLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-normal.jsonl")

	header := Header{Type: TypeSession, Version: 1, ID: "sess-id", Timestamp: "2026-01-01T00:00:00Z", CWD: "/tmp", Model: "test", Provider: "test"}
	hData, _ := json.Marshal(header)
	msg := MessageEvent{
		Type:      TypeMessage,
		ID:        "test-id",
		Timestamp: "2026-01-01T00:00:00Z",
		Message: MessagePayload{
			Role: "user",
			Content: []ContentBlock{
				{Type: "text", Text: "hello world"},
			},
		},
	}
	mData, _ := json.Marshal(msg)
	end := SessionEnd{Type: TypeSessionEnd, ID: "sess-id", Timestamp: "2026-01-01T00:00:00Z", Status: "success", ExitCode: 0}
	eData, _ := json.Marshal(end)

	content := fmt.Sprintf("%s\n%s\n%s\n", hData, mData, eData)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	lines, err := parseSessionFile(path)
	if err != nil {
		t.Fatalf("parseSessionFile: %v", err)
	}

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[1].MsgType != "user" {
		t.Fatalf("expected msgType 'user', got %q", lines[1].MsgType)
	}
}

// TestReadAllMessages_EmptyInput verifies empty input doesn't crash.
func TestReadAllMessages_EmptyInput(t *testing.T) {
	r := bytes.NewReader([]byte{})
	msgs, err := ReadAllMessages(r)
	if err != nil {
		t.Fatalf("ReadAllMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(msgs))
	}
}
