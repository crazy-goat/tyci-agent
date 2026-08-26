package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/connector"
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

// ─── Open / Fresh Session ────────────────────────────────────────────────

func TestOpen_CreatesNewSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	s, err := Open(path, "/tmp", "gpt-4", "openai")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = s.Close() }()

	if s.ID() == "" {
		t.Error("session ID should not be empty")
	}
	if s.IsResume() {
		t.Error("fresh session should not be resume")
	}
	if msgs := s.Messages(); msgs != nil {
		t.Errorf("fresh session should have nil messages, got %d lines", len(msgs))
	}

	// Verify file was created with header
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	var header Header
	if err := json.Unmarshal(bytes.Split(data, []byte("\n"))[0], &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header.Type != TypeSession {
		t.Errorf("header type = %q, want %q", header.Type, TypeSession)
	}
	if header.Version != 1 {
		t.Errorf("header version = %d, want 1", header.Version)
	}
	if header.Model != "gpt-4" {
		t.Errorf("header model = %q, want %q", header.Model, "gpt-4")
	}
	if header.Provider != "openai" {
		t.Errorf("header provider = %q, want %q", header.Provider, "openai")
	}
}

func TestOpen_CreatesDirIfNotExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "nested", "test.jsonl")

	s, err := Open(path, "/tmp", "test-model", "test-provider")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	_ = s.Close()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("session file was not created")
	}
}

// ─── Open / Resume ──────────────────────────────────────────────────────

func TestOpen_ResumesExistingSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume-test.jsonl")

	// Create initial session with one message
	s1, err := Open(path, "/tmp", "gpt-4", "openai")
	if err != nil {
		t.Fatalf("first Open() error: %v", err)
	}
	err = s1.WriteMessage("user", []ContentBlock{{Type: "text", Text: "hello"}}, nil)
	if err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}
	err = s1.WriteSessionEnd("success", 0, nil)
	if err != nil {
		t.Fatalf("WriteSessionEnd() error: %v", err)
	}
	_ = s1.Close()

	// Resume the session
	s2, err := Open(path, "/tmp", "gpt-4", "openai")
	if err != nil {
		t.Fatalf("second Open() error: %v", err)
	}
	defer func() { _ = s2.Close() }()

	if !s2.IsResume() {
		t.Error("resumed session should have IsResume() = true")
	}
	msgs := s2.Messages()
	if msgs == nil {
		t.Fatal("resumed session should have messages")
	}
	// Header + 1 message + session_end = 3 lines
	if len(msgs) != 3 {
		t.Fatalf("expected 3 parsed lines, got %d", len(msgs))
	}
	if msgs[0].MsgType != "header" {
		t.Errorf("first line should be header, got %q", msgs[0].MsgType)
	}
	if msgs[1].MsgType != "user" {
		t.Errorf("second line should be 'user', got %q", msgs[1].MsgType)
	}
	if msgs[2].MsgType != "session_end" {
		t.Errorf("third line should be 'session_end', got %q", msgs[2].MsgType)
	}
}

func TestOpen_ResumeAppendsToExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "append-test.jsonl")

	s1, err := Open(path, "/tmp", "gpt-4", "openai")
	if err != nil {
		t.Fatalf("first Open() error: %v", err)
	}
	_ = s1.Close()

	// Resume and add a message
	s2, err := Open(path, "/tmp", "gpt-4", "openai")
	if err != nil {
		t.Fatalf("second Open() error: %v", err)
	}
	err = s2.WriteMessage("user", []ContentBlock{{Type: "text", Text: "appended"}}, nil)
	if err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}
	_ = s2.Close()

	// Read file and count lines
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 { // header + 1 message
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
}

// ─── WriteMessage ───────────────────────────────────────────────────────

func TestWriteMessage_UserMessage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	s, err := Open(path, "/tmp", "test", "test")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = s.Close() }()

	err = s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "Hello, world!"}}, nil)
	if err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}

	// Verify content
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Hello, world!") {
		t.Errorf("expected 'Hello, world!' in output, got %q", content)
	}
	if !strings.Contains(content, `"role":"user"`) {
		t.Errorf("expected role 'user' in output, got %q", content)
	}
}

func TestWriteMessage_AssistantWithOptions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	s, err := Open(path, "/tmp", "test", "test")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = s.Close() }()

	opts := &MessageOptions{
		API:        "openai",
		Provider:   "openai",
		Model:      "gpt-4",
		StopReason: "stop",
		ResponseID: "resp-123",
		Usage: &Usage{
			Input:      50,
			Output:     100,
			Reasoning:  0,
			CacheRead:  10,
			CacheWrite: 5,
		},
	}

	err = s.WriteMessage("assistant", []ContentBlock{
		{Type: "text", Text: "I am an AI."},
		{Type: "toolCall", ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)},
	}, opts)
	if err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}

	// Read and verify
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `"role":"assistant"`) {
		t.Errorf("expected assistant role, got %q", content)
	}
	if !strings.Contains(content, `"api":"openai"`) {
		t.Errorf("expected api field, got %q", content)
	}
	if !strings.Contains(content, `"input":50`) {
		t.Errorf("expected input usage, got %q", content)
	}
	if !strings.Contains(content, `"output":100`) {
		t.Errorf("expected output usage, got %q", content)
	}
	if !strings.Contains(content, `"cacheRead":10`) {
		t.Errorf("expected cacheRead, got %q", content)
	}
	if !strings.Contains(content, `"cacheWrite":5`) {
		t.Errorf("expected cacheWrite, got %q", content)
	}
	if !strings.Contains(content, `"stopReason":"stop"`) {
		t.Errorf("expected stopReason, got %q", content)
	}
	if !strings.Contains(content, `"responseId":"resp-123"`) {
		t.Errorf("expected responseId, got %q", content)
	}
	if !strings.Contains(content, `"name":"bash"`) {
		t.Errorf("expected tool name 'bash', got %q", content)
	}
}

func TestWriteMessage_ToolResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	s, err := Open(path, "/tmp", "test", "test")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = s.Close() }()

	err = s.WriteMessage("toolResult", []ContentBlock{
		{Type: "toolResult", ToolCallID: "call-1", ToolName: "bash", Text: "file1.txt\nfile2.txt", IsError: false},
	}, nil)
	if err != nil {
		t.Fatalf("WriteMessage() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `"role":"toolResult"`) {
		t.Errorf("expected toolResult role, got %q", content)
	}
	if !strings.Contains(content, `"toolCallId":"call-1"`) {
		t.Errorf("expected toolCallId, got %q", content)
	}
}

func TestWriteMessage_AfterClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	s, err := Open(path, "/tmp", "test", "test")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	_ = s.Close()

	err = s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "should fail"}}, nil)
	if err == nil {
		t.Error("WriteMessage() after close should return error")
	}
}

// ─── WriteSessionEnd ────────────────────────────────────────────────────

func TestWriteSessionEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	s, err := Open(path, "/tmp", "test", "test")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = s.Close() }()

	usage := &Usage{Input: 10, Output: 20, TotalTokens: 30}
	err = s.WriteSessionEnd("success", 0, usage)
	if err != nil {
		t.Fatalf("WriteSessionEnd() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `"type":"session_end"`) {
		t.Errorf("expected session_end type, got %q", content)
	}
	if !strings.Contains(content, `"status":"success"`) {
		t.Errorf("expected status, got %q", content)
	}
	if !strings.Contains(content, `"exit_code":0`) {
		t.Errorf("expected exit_code, got %q", content)
	}
	if !strings.Contains(content, `"input":10`) {
		t.Errorf("expected usage input, got %q", content)
	}
}

func TestWriteSessionEnd_AfterClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	s, err := Open(path, "/tmp", "test", "test")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	_ = s.Close()

	err = s.WriteSessionEnd("failed", 1, nil)
	if err == nil {
		t.Error("WriteSessionEnd() after close should return error")
	}
}

// ─── Close ──────────────────────────────────────────────────────────────

func TestClose_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	s, err := Open(path, "/tmp", "test", "test")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("first Close() error: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close() should not error: %v", err)
	}
}

// ─── DefaultPath ────────────────────────────────────────────────────────

func TestDefaultPath_Format(t *testing.T) {
	path, err := DefaultPath("/home/user/projects/my-app")
	if err != nil {
		t.Fatalf("DefaultPath() error: %v", err)
	}

	// Should be under ~/.tyci/sessions/
	if !strings.Contains(path, ".tyci/sessions/") {
		t.Errorf("expected .tyci/sessions/ in path, got %q", path)
	}

	// Should contain encoded CWD
	if !strings.Contains(path, "--home--user--projects--my-app") {
		t.Errorf("expected encoded CWD, got %q", path)
	}

	// Should end with .jsonl
	if !strings.HasSuffix(path, ".jsonl") {
		t.Errorf("expected .jsonl suffix, got %q", path)
	}

	// Should contain timestamp and UUID
	parts := strings.Split(path, "/")
	filename := parts[len(parts)-1]
	// Format: <ts>_<uuid>.jsonl
	tsPart := strings.Split(filename, "_")
	if len(tsPart) != 2 {
		t.Errorf("expected filename format <ts>_<uuid>.jsonl, got %q", filename)
	}
}

func TestDefaultPath_RootCWD(t *testing.T) {
	path, err := DefaultPath("/")
	if err != nil {
		t.Fatalf("DefaultPath() error: %v", err)
	}

	// "/" becomes "--" after replacing slashes
	if !strings.Contains(path, "--") {
		t.Errorf("root CWD should contain '--', got %q", path)
	}
}

// An empty cwd string isn't a real call site (every caller passes
// os.Getwd()'s result), but it must not error: filepath.Abs("") resolves it
// to the process's actual working directory, same as any other relative
// path, and ProjectKey/DefaultPath key off of that like normal.
func TestDefaultPath_EmptyCWD(t *testing.T) {
	path, err := DefaultPath("")
	if err != nil {
		t.Fatalf("DefaultPath() error: %v", err)
	}
	if !strings.Contains(path, ".tyci/sessions/") {
		t.Errorf("expected .tyci/sessions/ in path, got %q", path)
	}
	if !strings.HasSuffix(path, ".jsonl") {
		t.Errorf("expected .jsonl suffix, got %q", path)
	}
}

// ─── RebuildMessages ────────────────────────────────────────────────────

func TestRebuildMessages_SingleUserMessage(t *testing.T) {
	lines := []ParsedLine{
		{Raw: `{"type":"session","version":1,"id":"s1","timestamp":"2026-01-01T00:00:00Z"}`, MsgType: "header"},
		{Raw: `{"type":"message","id":"m1","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`, MsgType: "user"},
		{Raw: `{"type":"session_end","id":"s1","timestamp":"2026-01-01T00:00:02Z","status":"success","exit_code":0}`, MsgType: "session_end"},
	}

	msgs, err := RebuildMessages(lines)
	if err != nil {
		t.Fatalf("RebuildMessages() error: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", msgs[0].Role)
	}
	if len(msgs[0].Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msgs[0].Content))
	}
	if msgs[0].Content[0].Type != "text" {
		t.Errorf("expected text block, got %q", msgs[0].Content[0].Type)
	}
	if msgs[0].Content[0].Text != "hello" {
		t.Errorf("expected text 'hello', got %q", msgs[0].Content[0].Text)
	}
}

func TestRebuildMessages_AssistantWithToolCalls(t *testing.T) {
	lines := []ParsedLine{
		{Raw: `{"type":"session","version":1}`, MsgType: "header"},
		{Raw: `{"type":"message","id":"m1","message":{"role":"assistant","content":[{"type":"text","text":"Let me check"},{"type":"toolCall","id":"call-1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}]}}`, MsgType: "assistant"},
	}

	msgs, err := RebuildMessages(lines)
	if err != nil {
		t.Fatalf("RebuildMessages() error: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", msgs[0].Role)
	}
	if len(msgs[0].Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(msgs[0].Content))
	}

	// Check text block
	if msgs[0].Content[0].Type != "text" || msgs[0].Content[0].Text != "Let me check" {
		t.Errorf("expected text block 'Let me check', got %+v", msgs[0].Content[0])
	}

	// Check tool call block
	if msgs[0].Content[1].Type != "toolCall" {
		t.Errorf("expected toolCall block, got %q", msgs[0].Content[1].Type)
	}
	if msgs[0].Content[1].ID != "call-1" {
		t.Errorf("expected toolCall ID 'call-1', got %q", msgs[0].Content[1].ID)
	}
	if msgs[0].Content[1].Name != "bash" {
		t.Errorf("expected tool name 'bash', got %q", msgs[0].Content[1].Name)
	}
}

func TestRebuildMessages_MixedConversation(t *testing.T) {
	lines := []ParsedLine{
		{Raw: `{"type":"session","version":1}`, MsgType: "header"},
		{Raw: `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"What time is it?"}]}}`, MsgType: "user"},
		{Raw: `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"It's noon."}]}}`, MsgType: "assistant"},
		{Raw: `{"type":"session_end","status":"success","exit_code":0}`, MsgType: "session_end"},
	}

	msgs, err := RebuildMessages(lines)
	if err != nil {
		t.Fatalf("RebuildMessages() error: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Errorf("expected roles user->assistant, got %q -> %q", msgs[0].Role, msgs[1].Role)
	}
}

func TestRebuildMessages_NestedToolResult(t *testing.T) {
	json := `{"type":"message","message":{"role":"toolResult","content":[{"type":"toolResult","toolCallId":"call-1","toolName":"bash","text":"output","isError":false}]}}`
	lines := []ParsedLine{
		{Raw: `{"type":"session","version":1}`, MsgType: "header"},
		{Raw: json, MsgType: "toolResult"},
	}

	msgs, err := RebuildMessages(lines)
	if err != nil {
		t.Fatalf("RebuildMessages() error: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "toolResult" {
		t.Errorf("expected role 'toolResult', got %q", msgs[0].Role)
	}
	if len(msgs[0].Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msgs[0].Content))
	}
	cb := msgs[0].Content[0]
	if cb.Type != "toolResult" || cb.ToolCallID != "call-1" || cb.ToolName != "bash" || cb.Text != "output" {
		t.Errorf("unexpected toolResult block: %+v", cb)
	}
}

func TestRebuildMessages_SkipsThinking(t *testing.T) {
	lines := []ParsedLine{
		{Raw: `{"type":"session","version":1}`, MsgType: "header"},
		{Raw: `{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Hmm, let me think..."},{"type":"text","text":"Here's the answer."}]}}`, MsgType: "assistant"},
	}

	msgs, err := RebuildMessages(lines)
	if err != nil {
		t.Fatalf("RebuildMessages() error: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs[0].Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(msgs[0].Content))
	}
	if msgs[0].Content[0].Type != "thinking" {
		t.Errorf("expected thinking block, got %q", msgs[0].Content[0].Type)
	}
	if msgs[0].Content[0].Thinking != "Hmm, let me think..." {
		t.Errorf("expected thinking text, got %q", msgs[0].Content[0].Thinking)
	}
}

func TestRebuildMessages_SkipsMalformedLines(t *testing.T) {
	lines := []ParsedLine{
		{Raw: `{"type":"session","version":1}`, MsgType: "header"},
		{Raw: `NOT JSON`, MsgType: "unknown"},
		{Raw: `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`, MsgType: "user"},
	}

	msgs, err := RebuildMessages(lines)
	if err != nil {
		t.Fatalf("RebuildMessages() error: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 valid message, got %d", len(msgs))
	}
}

func TestRebuildMessages_UsesLatestCompactionTail(t *testing.T) {
	lines := []ParsedLine{
		{Raw: `{"type":"session","version":1}`, MsgType: "header"},
		{Raw: `{"type":"message","id":"m1","message":{"role":"user","content":[{"type":"text","text":"old user"}]}}`, MsgType: "user"},
		{Raw: `{"type":"compaction","id":"c1","summary":{"role":"user","content":[{"type":"text","text":"summary text"}]},"tail_start_id":"m2","tail_messages":[{"role":"assistant","content":[{"type":"text","text":"recent assistant"}]},{"role":"user","content":[{"type":"text","text":"recent user"}]}]}`, MsgType: "compaction"},
		{Raw: `{"type":"message","id":"m3","message":{"role":"assistant","content":[{"type":"text","text":"new answer"}]}}`, MsgType: "assistant"},
	}

	msgs, err := RebuildMessages(lines)
	if err != nil {
		t.Fatalf("RebuildMessages() error: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[0].Content[0].Text != "summary text" {
		t.Fatalf("expected compaction summary first, got %#v", msgs[0])
	}
	if msgs[1].Content[0].Text != "recent assistant" {
		t.Fatalf("expected compacted tail assistant, got %#v", msgs[1])
	}
	if msgs[2].Content[0].Text != "recent user" {
		t.Fatalf("expected compacted tail user, got %#v", msgs[2])
	}
	if msgs[3].Content[0].Text != "new answer" {
		t.Fatalf("expected post-compaction message, got %#v", msgs[3])
	}
}

func TestRebuildMessages_DropsOrphanToolResults(t *testing.T) {
	lines := []ParsedLine{
		{Raw: `{"type":"session","version":1}`, MsgType: "header"},
		{Raw: `{"type":"message","id":"t1","message":{"role":"toolResult","content":[{"type":"text","text":"orphan result","toolCallId":"call-orphan","toolName":"bash"}]}}`, MsgType: "toolResult"},
		{Raw: `{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"text","text":"running tool"},{"type":"toolCall","id":"call-1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}]}}`, MsgType: "assistant"},
		{Raw: `{"type":"message","id":"t2","message":{"role":"toolResult","content":[{"type":"text","text":"matched result","toolCallId":"call-1","toolName":"bash"}]}}`, MsgType: "toolResult"},
	}
	msgs, err := RebuildMessages(lines)
	if err != nil {
		t.Fatalf("RebuildMessages() error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after dropping orphan tool result, got %d", len(msgs))
	}
	if msgs[0].Role != "assistant" || msgs[1].Role != "toolResult" {
		t.Fatalf("unexpected roles after sanitize: %#v", msgs)
	}
	if msgs[1].Content[0].ToolCallID != "call-1" {
		t.Fatalf("expected matched tool result to survive, got %#v", msgs[1])
	}
}

// F8 (item 10 inbox): LoadForReplay is what feeds the model on /resume
// (tui_mode.go, interactive.go), but unlike RebuildMessages it never called
// SanitizeMessageSequence. A JSONL truncated mid tool-call/result pair —
// here, a header directly followed by an orphan toolResult whose matching
// assistant toolCall was never written (e.g. the process died right after
// the result but the call line got lost, or a compaction tail starts
// mid-call) — must not leave that orphan toolResult in the replayed
// history: providers reject an orphan tool result with a 400 the moment
// /resume tries to send it.
func TestLoadForReplay_DropsOrphanToolResultFromTruncatedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "truncated.jsonl")
	lines := []string{
		`{"type":"session","version":1,"id":"s1","model":"m","provider":"p"}`,
		`{"type":"message","id":"t1","message":{"role":"toolResult","content":[{"type":"text","text":"orphan result","toolCallId":"call-orphan","toolName":"bash"}]}}`,
		`{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"text","text":"running tool"},{"type":"toolCall","id":"call-1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}]}}`,
		`{"type":"message","id":"t2","message":{"role":"toolResult","content":[{"type":"text","text":"matched result","toolCallId":"call-1","toolName":"bash"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, msgs, _, corrupt, err := LoadForReplay(path)
	if err != nil {
		t.Fatalf("LoadForReplay() error: %v", err)
	}
	if len(corrupt) != 0 {
		t.Fatalf("expected 0 corrupt lines (the orphan result parses fine, it's just unmatched), got %d: %v", len(corrupt), corrupt)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after dropping the orphan tool result, got %d: %#v", len(msgs), msgs)
	}
	if msgs[0].Role != "assistant" || msgs[1].Role != "toolResult" {
		t.Fatalf("unexpected roles after sanitize: %#v", msgs)
	}
	if msgs[1].Content[0].ToolCallID != "call-1" {
		t.Fatalf("expected the matched tool result to survive, got %#v", msgs[1])
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.ToolCallID == "call-orphan" {
				t.Fatalf("orphan tool result (call-orphan) survived into replay: %#v", msgs)
			}
		}
	}
}

// ─── parseSessionFile edge cases ────────────────────────────────────────

func TestParseSessionFile_SkipsEmptyLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	header := Header{Type: TypeSession, Version: 1, ID: "s1"}
	hData, _ := json.Marshal(header)
	// Add empty lines between valid JSON lines
	content := string(hData) + "\n\n\n" + string(hData) + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	lines, err := parseSessionFile(path)
	if err != nil {
		t.Fatalf("parseSessionFile() error: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (skipping empty ones), got %d", len(lines))
	}
}

func TestParseSessionFile_SkipsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")

	content := `{"type":"session","version":1}
not json
{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	lines, err := parseSessionFile(path)
	if err != nil {
		t.Fatalf("parseSessionFile() error: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 valid lines (skipping invalid), got %d", len(lines))
	}
}

// ─── ReadAllMessages ────────────────────────────────────────────────────

func TestReadAllMessages_MultipleEvents(t *testing.T) {
	var buf bytes.Buffer
	events := []string{
		`{"type":"session","version":1}`,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"session_end","status":"success","exit_code":0}`,
	}
	for _, e := range events {
		buf.WriteString(e + "\n")
	}

	msgs, err := ReadAllMessages(&buf)
	if err != nil {
		t.Fatalf("ReadAllMessages() error: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 events, got %d", len(msgs))
	}
}

func TestReadAllMessages_SkipsNonJSON(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("not json\n")
	buf.WriteString(`{"valid":true}` + "\n")

	msgs, err := ReadAllMessages(&buf)
	if err != nil {
		t.Fatalf("ReadAllMessages() error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 valid message, got %d", len(msgs))
	}
}

// ─── Round-trip: Open → Write → Resume → Rebuild ───────────────────────

func TestFullRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roundtrip.jsonl")

	// Create session with user + assistant + toolResult messages
	s, err := Open(path, "/tmp", "gpt-4", "openai")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}

	err = s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "List files"}}, nil)
	if err != nil {
		t.Fatalf("WriteMessage(user) error: %v", err)
	}

	err = s.WriteMessage("assistant", []ContentBlock{
		{Type: "text", Text: "Running ls"},
		{Type: "toolCall", ID: "tc-1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)},
	}, &MessageOptions{Model: "gpt-4", Usage: &Usage{Input: 10, Output: 20}})
	if err != nil {
		t.Fatalf("WriteMessage(assistant) error: %v", err)
	}

	err = s.WriteMessage("toolResult", []ContentBlock{
		{Type: "toolResult", ToolCallID: "tc-1", ToolName: "bash", Text: "file1.txt\nfile2.txt"},
	}, nil)
	if err != nil {
		t.Fatalf("WriteMessage(toolResult) error: %v", err)
	}

	err = s.WriteSessionEnd("success", 0, &Usage{Input: 10, Output: 20, TotalTokens: 30})
	if err != nil {
		t.Fatalf("WriteSessionEnd() error: %v", err)
	}
	_ = s.Close()

	// Resume and rebuild
	s2, err := Open(path, "/tmp", "gpt-4", "openai")
	if err != nil {
		t.Fatalf("resume Open() error: %v", err)
	}
	defer func() { _ = s2.Close() }()

	if !s2.IsResume() {
		t.Error("should be resume")
	}

	msgs, err := RebuildMessages(s2.Messages())
	if err != nil {
		t.Fatalf("RebuildMessages() error: %v", err)
	}

	// Should have 3 messages (user, assistant, toolResult)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("msg[0] role = %q, want 'user'", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("msg[1] role = %q, want 'assistant'", msgs[1].Role)
	}
	if msgs[2].Role != "toolResult" {
		t.Errorf("msg[2] role = %q, want 'toolResult'", msgs[2].Role)
	}

	// Check content blocks
	if len(msgs[1].Content) != 2 {
		t.Fatalf("assistant should have 2 content blocks, got %d", len(msgs[1].Content))
	}
	if msgs[1].Content[0].Type != "text" || msgs[1].Content[0].Text != "Running ls" {
		t.Errorf("assistant text block wrong: %+v", msgs[1].Content[0])
	}
	if msgs[1].Content[1].Type != "toolCall" || msgs[1].Content[1].ID != "tc-1" {
		t.Errorf("assistant toolCall block wrong: %+v", msgs[1].Content[1])
	}
}

func TestCompactKeepsRawLogAndReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compact.jsonl")
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "old"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "answer"}}},
	}
	if _, err := s.Compact("preserved summary", "tail-1", msgs[1:], 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type":"compaction"`) || !strings.Contains(string(data), "old") {
		t.Fatalf("raw log was not retained: %s", data)
	}
	_, replay, _, _, err := LoadForReplay(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 2 || replay[0].Content[0].Text != "preserved summary" || replay[1].Content[0].Text != "answer" {
		t.Fatalf("replay = %#v", replay)
	}
	if _, err := os.Stat(strings.TrimSuffix(path, ".jsonl") + ".md"); err != nil {
		t.Fatalf("markdown dump missing: %v", err)
	}
}

func TestMarkdownDumpRefreshesAfterLaterMessage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "refresh.jsonl")
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Compact("summary", "", nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "new message"}}, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "new message") {
		t.Fatalf("dump was stale after WriteMessage: %s", data)
	}
	if err := s.WriteCompaction("second summary", "", nil, 0); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "second summary") {
		t.Fatalf("dump was stale after WriteCompaction: %s", data)
	}
}

// F9 (item 10 inbox): refreshMarkdownDump used to re-parse and rewrite the
// ENTIRE derived dump on every single WriteMessage/WriteCompaction/
// WriteSessionEnd call — O(n) per write, O(n^2) over a session's length.
// recordDumpEvent replaces that with an O(1) append once the dump is known
// to exist; a plain WriteMessage on an already-initialized session must
// never trigger another full WriteMarkdownDump pass, however long the
// session already is.
func TestWriteMessage_DoesNotTriggerFullDumpRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hot-path.jsonl")
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// The very first write of a brand-new session is allowed one full
	// rewrite (there is no dump file yet to append to).
	if err := s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "seed"}}, nil); err != nil {
		t.Fatal(err)
	}
	ResetMarkdownFullRewritesForTesting()

	const n = 500
	for i := 0; i < n; i++ {
		if err := s.WriteMessage("user", []ContentBlock{{Type: "text", Text: fmt.Sprintf("message %d", i)}}, nil); err != nil {
			t.Fatalf("WriteMessage(%d): %v", i, err)
		}
	}
	if got := MarkdownFullRewritesForTesting(); got != 0 {
		t.Fatalf("full dump rewrites = %d after %d plain WriteMessage calls, want 0 (O(1) append should have handled every one)", got, n)
	}

	data, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "message 499") {
		t.Fatalf("dump is missing the last appended message: %s", data)
	}
	if !strings.Contains(string(data), "seed") {
		t.Fatalf("dump is missing the seed message written before the reset: %s", data)
	}
}

// Correctness companion to the above: the incrementally-appended dump must
// be byte-identical to what a full WriteMarkdownDump pass over the same
// JSONL produces, across a mix of messages, a compaction, and a session
// end — not just "has the right substrings".
func TestIncrementalDump_MatchesFullRewriteByteForByte(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.jsonl")
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "hello"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("assistant", []ContentBlock{{Type: "text", Text: "hi there"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteCompaction("summary text", "", nil, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "after compaction"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSessionEnd("success", 0, nil); err != nil {
		t.Fatal(err)
	}
	s.Close()

	incremental, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".md")
	if err != nil {
		t.Fatal(err)
	}
	fullPath, err := WriteMarkdownDump(path)
	if err != nil {
		t.Fatal(err)
	}
	full, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(incremental) != string(full) {
		t.Fatalf("incremental dump differs from a full rewrite of the same JSONL:\nincremental:\n%s\nfull:\n%s", incremental, full)
	}
}

// A resumed session whose dump file was deleted (or never existed, e.g. a
// session written before this feature) must get it backfilled by exactly
// one full rewrite on the next write, then go back to appending.
func TestRecordDumpEvent_BackfillsMissingDumpOnResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backfill.jsonl")
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "before restart"}}, nil); err != nil {
		t.Fatal(err)
	}
	s.Close()

	if err := os.Remove(strings.TrimSuffix(path, ".jsonl") + ".md"); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ResetMarkdownFullRewritesForTesting()
	if err := s2.WriteMessage("user", []ContentBlock{{Type: "text", Text: "after restart"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := MarkdownFullRewritesForTesting(); got != 1 {
		t.Fatalf("full dump rewrites = %d, want exactly 1 (the backfill)", got)
	}
	data, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "before restart") || !strings.Contains(string(data), "after restart") {
		t.Fatalf("backfilled dump is missing pre- or post-restart content: %s", data)
	}

	ResetMarkdownFullRewritesForTesting()
	if err := s2.WriteMessage("user", []ContentBlock{{Type: "text", Text: "third"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := MarkdownFullRewritesForTesting(); got != 0 {
		t.Fatalf("full dump rewrites = %d after the backfill, want 0 (should be back to appending)", got)
	}
}

// TestRecordDumpEvent_ResumeWithExistingDumpDoesNotDropFirstEvent guards the
// review-found F9 regression: a resumed process whose dump file already
// exists (the normal case — the previous run wrote it) must still fold in
// the first event written after resume, not silently drop it because the
// stat-based "is the dump current" check found the file already there.
func TestRecordDumpEvent_ResumeWithExistingDumpDoesNotDropFirstEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume.jsonl")
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "before restart"}}, nil); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// The dump file is left in place this time — unlike the backfill test
	// above, this is the normal resume case.
	if _, err := os.Stat(strings.TrimSuffix(path, ".jsonl") + ".md"); err != nil {
		t.Fatalf("precondition: dump file should exist before resume: %v", err)
	}

	s2, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.WriteMessage("user", []ContentBlock{{Type: "text", Text: "FIRST-AFTER-RESUME"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s2.WriteMessage("user", []ContentBlock{{Type: "text", Text: "second-after-resume"}}, nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "FIRST-AFTER-RESUME") {
		t.Fatalf("dump is missing the first event written after resume (dropped by the stale stat-based check): %s", data)
	}
	if !strings.Contains(string(data), "second-after-resume") {
		t.Fatalf("dump is missing the second event written after resume: %s", data)
	}

	fullPath, err := WriteMarkdownDump(path)
	if err != nil {
		t.Fatal(err)
	}
	full, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(full) {
		t.Fatalf("incremental dump after resume differs from a full rewrite of the same JSONL:\nincremental:\n%s\nfull:\n%s", data, full)
	}
}

func TestCompactionLiveBoundaryDoesNotInventEventID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "boundary.jsonl")
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.WriteCompaction("summary", "", nil, 1); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "live-message-") {
		t.Fatalf("synthetic live boundary id persisted: %s", data)
	}
}

// ─── Markdown dump (item 10) ────────────────────────────────────────────

func TestWriteMarkdownDump_HeaderCarriesMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.jsonl")
	s, err := Open(path, dir, "gpt-5", "openai")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	dumpPath, err := WriteMarkdownDump(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	for _, want := range []string{"session id:", "openai/gpt-5", "project:", "date:"} {
		if !strings.Contains(out, want) {
			t.Errorf("dump header missing %q: %s", want, out)
		}
	}
}

// Every content-bearing line must be self-identifying with a stable
// "[N] [role-or-tool]" prefix and must never wrap: a wrapped line splits a
// path or identifier in half and grep stops finding it.
func TestWriteMarkdownDump_LinesAreSelfIdentifyingAndUnwrapped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.jsonl")
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "please read /etc/passwd\nand summarize"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("assistant", []ContentBlock{
		{Type: "toolCall", ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"cat /etc/passwd"}`)},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("toolResult", []ContentBlock{
		{Type: "text", Text: "Error: no such file", ToolCallID: "call-1", ToolName: "bash", IsError: true},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	dumpPath := strings.TrimSuffix(path, ".jsonl") + ".md"
	data, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	var toolCallLine, toolResultLine, userLine string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "call id=call-1"):
			toolCallLine = l
		case strings.Contains(l, "result id=call-1"):
			toolResultLine = l
		case strings.Contains(l, "[user]"):
			userLine = l
		}
	}
	if toolCallLine == "" {
		t.Fatalf("no tool call line found: %s", data)
	}
	if !strings.HasPrefix(toolCallLine, "[") || !strings.Contains(toolCallLine, "[bash]") {
		t.Errorf("tool call line must be prefixed with its tool name: %q", toolCallLine)
	}
	if !strings.Contains(toolCallLine, "cat /etc/passwd") {
		t.Errorf("tool call args not written verbatim: %q", toolCallLine)
	}
	if toolResultLine == "" {
		t.Fatalf("no tool result line found: %s", data)
	}
	if !strings.Contains(toolResultLine, "[bash]") || !strings.Contains(toolResultLine, "error=true") {
		t.Errorf("tool result line must name its tool and flag the error: %q", toolResultLine)
	}
	if !strings.Contains(toolResultLine, "Error: no such file") {
		t.Errorf("tool result text not written verbatim: %q", toolResultLine)
	}
	if userLine == "" {
		t.Fatalf("no user line found: %s", data)
	}
	if strings.Contains(userLine, "\n") {
		t.Errorf("user line contains a literal newline, breaking grep: %q", userLine)
	}
	if !strings.Contains(userLine, "please read /etc/passwd and summarize") {
		t.Errorf("embedded newline should collapse to a space, not disappear: %q", userLine)
	}
}

func TestWriteMarkdownDump_StripsANSIEscapes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ansi.jsonl")
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	coloredError := "\x1b[31mError: boom\x1b[0m"
	if err := s.WriteMessage("toolResult", []ContentBlock{
		{Type: "text", Text: coloredError, ToolCallID: "c1", ToolName: "bash", IsError: true},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".md")
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("ANSI escape survived into the dump: %q", out)
	}
	if !strings.Contains(out, "Error: boom") {
		t.Fatalf("error text lost along with its color codes: %q", out)
	}
}

func TestWriteMarkdownDump_CapsOversizedFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cap.jsonl")
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x", dumpFieldCap+500)
	if err := s.WriteMessage("toolResult", []ContentBlock{
		{Type: "text", Text: huge, ToolCallID: "c1", ToolName: "read", IsError: false},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".md")
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if strings.Contains(out, strings.Repeat("x", dumpFieldCap+1)) {
		t.Fatalf("field was not capped: dump contains %d consecutive x's or more", dumpFieldCap+1)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("truncation must be marked, not silent: %q", out)
	}
	// The raw JSONL must still hold the full, untruncated text — the dump
	// truncates, the source of truth never does.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), huge) {
		t.Fatalf("raw JSONL must retain the full field even though the dump caps it")
	}
}

// TestDumpPathFor_NeverCollidesWithSessionPath is the regression test for a
// session opened at a path that already ends in ".md" (e.g. --session
// notes.md): trimming the extension and re-appending ".md" would otherwise
// reproduce the session path itself, and os.Create on the dump would
// truncate the live JSONL out from under the running session.
func TestDumpPathFor_NeverCollidesWithSessionPath(t *testing.T) {
	got := DumpPathFor("/tmp/notes.md")
	if got == "/tmp/notes.md" {
		t.Fatalf("DumpPathFor collided with the session path: %q", got)
	}
	if got != "/tmp/notes.md.md" {
		t.Fatalf("DumpPathFor(.md session) = %q, want /tmp/notes.md.md", got)
	}

	// The ordinary .jsonl case is unaffected.
	if got := DumpPathFor("/tmp/session.jsonl"); got != "/tmp/session.md" {
		t.Fatalf("DumpPathFor(.jsonl session) = %q, want /tmp/session.md", got)
	}
}

// TestWriteMarkdownDump_MdSessionPathDoesNotTruncateItself exercises the
// guard end-to-end: opening a session at a path ending in .md must not have
// its own JSONL content clobbered when the derived dump is written.
func TestWriteMarkdownDump_MdSessionPathDoesNotTruncateItself(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	s, err := Open(path, dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteMessage("user", []ContentBlock{{Type: "text", Text: "hello"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "hello") {
		t.Fatalf("session JSONL at a .md path was truncated by its own dump: %s", raw)
	}

	dumpPath := DumpPathFor(path)
	if dumpPath == path {
		t.Fatalf("dump path collided with session path: %q", dumpPath)
	}
	if _, err := os.Stat(dumpPath); err != nil {
		t.Fatalf("dump not written to its distinct path: %v", err)
	}
}
