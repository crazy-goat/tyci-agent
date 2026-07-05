// Package session implements append-only JSONL session files.
// Each run produces a JSONL file with header, message events, and a session_end event.
// Pass --session <path> to resume from an existing file.
package session

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/decodo/tyci/providers"
)

// ─── Types ────────────────────────────────────────────────────────────────

type EventType string

const (
	TypeSession    EventType = "session"
	TypeMessage    EventType = "message"
	TypeCompaction EventType = "compaction"
	TypeSessionEnd EventType = "session_end"
)

// Usage mirrors stream.Usage but is JSON-serializable without coupling.
type Usage struct {
	Input       int     `json:"input"`
	Output      int     `json:"output"`
	Reasoning   int     `json:"reasoning,omitempty"`
	TotalTokens int     `json:"totalTokens"`
	TotalCost   float64 `json:"total_cost,omitempty"`
	CacheRead   int     `json:"cacheRead,omitempty"`
	CacheWrite  int     `json:"cacheWrite,omitempty"`
}

// Header is the first line of every session file.
type Header struct {
	Type      EventType `json:"type"`
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Timestamp string    `json:"timestamp"`
	CWD       string    `json:"cwd"`
	Model     string    `json:"model"`
	Provider  string    `json:"provider"`
}

// ContentBlock is a typed content block (text, thinking, toolCall, toolResult).
type ContentBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Thinking   string          `json:"thinking,omitempty"`
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
}

// MessagePayload is the message portion of a message event.
type MessagePayload struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// MessageEvent represents a user message, assistant message, or tool result.
type MessageEvent struct {
	Type      EventType      `json:"type"`
	ID        string         `json:"id"`
	Timestamp string         `json:"timestamp"`
	Message   MessagePayload `json:"message"`

	// Assistant-only metadata
	API        string `json:"api,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Usage      *Usage `json:"usage,omitempty"`
	StopReason string `json:"stopReason,omitempty"`
	ResponseID string `json:"responseId,omitempty"`
}

// SessionEnd is the final event in every session file.
type SessionEnd struct {
	Type       EventType `json:"type"`
	ID         string    `json:"id"`
	Timestamp  string    `json:"timestamp"`
	Status     string    `json:"status"`
	ExitCode   int       `json:"exit_code"`
	TotalUsage *Usage    `json:"total_usage,omitempty"`
}

type CompactionEvent struct {
	Type          EventType        `json:"type"`
	ID            string           `json:"id"`
	Timestamp     string           `json:"timestamp"`
	Summary       MessagePayload   `json:"summary"`
	TailStartID   string           `json:"tail_start_id,omitempty"`
	TailMessages  []MessagePayload `json:"tail_messages,omitempty"`
	DroppedEvents int              `json:"dropped_events,omitempty"`
}

// ─── Session ──────────────────────────────────────────────────────────────

// Session manages the append-only JSONL file.
type Session struct {
	id      string
	file    *os.File
	mu      sync.Mutex
	closed  bool
	encoder *json.Encoder
	path    string

	// Resume state
	isResume bool
}

// ParsedLine holds a raw line and its parsed event type for resume.
type ParsedLine struct {
	Raw     string
	MsgType string // "user", "assistant", "toolResult", "compaction", "session_end"
	Payload json.RawMessage
}

// ─── Open ─────────────────────────────────────────────────────────────────

// Open creates a new session file or resumes an existing one.
// If file exists, it resumes (parses history).
// If file doesn't exist, it creates a new session with header.
func Open(path, cwd, model, provider string) (*Session, error) {
	// Generate session ID
	id, err := newID()
	if err != nil {
		return nil, fmt.Errorf("session id: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("session dir: %w", err)
	}

	s := &Session{
		id:   id,
		path: path,
	}

	// Check if file exists → resume
	_, statErr := os.Stat(path)
	exists := statErr == nil

	if exists {
		// Open for append
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("open session for append: %w", err)
		}
		s.file = f
		s.encoder = json.NewEncoder(f)
		s.isResume = true

		if _, err := parseSessionFile(path); err != nil {
			f.Close()
			return nil, fmt.Errorf("parse session for resume: %w", err)
		}
	} else {
		// Create new file
		f, err := os.Create(path)
		if err != nil {
			return nil, fmt.Errorf("create session: %w", err)
		}
		s.file = f
		s.encoder = json.NewEncoder(f)

		// Write header
		h := Header{
			Type:      TypeSession,
			Version:   1,
			ID:        id,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			CWD:       cwd,
			Model:     model,
			Provider:  provider,
		}
		if err := s.encoder.Encode(h); err != nil {
			f.Close()
			os.Remove(path)
			return nil, fmt.Errorf("write header: %w", err)
		}
	}

	return s, nil
}

// ─── ID generation ────────────────────────────────────────────────────────

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ─── Write events ─────────────────────────────────────────────────────────

// WriteMessage writes a message event (user, assistant, or tool result).
func (s *Session) WriteMessage(role string, blocks []ContentBlock, opts *MessageOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("session closed")
	}

	id, err := newID()
	if err != nil {
		return err
	}

	ev := MessageEvent{
		Type:      TypeMessage,
		ID:        id,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Message: MessagePayload{
			Role:    role,
			Content: blocks,
		},
	}

	if opts != nil {
		ev.API = opts.API
		ev.Provider = opts.Provider
		ev.Model = opts.Model
		ev.StopReason = opts.StopReason
		ev.ResponseID = opts.ResponseID
		if opts.Usage != nil {
			ev.Usage = &Usage{
				Input:       opts.Usage.Input,
				Output:      opts.Usage.Output,
				Reasoning:   opts.Usage.Reasoning,
				CacheRead:   opts.Usage.CacheRead,
				CacheWrite:  opts.Usage.CacheWrite,
				TotalTokens: opts.Usage.Input + opts.Usage.Output + opts.Usage.Reasoning,
			}
		}
	}

	return s.encoder.Encode(ev)
}

// MessageOptions holds optional metadata for assistant messages.
type MessageOptions struct {
	API        string
	Provider   string
	Model      string
	Usage      *Usage
	StopReason string
	ResponseID string
}

// WriteSessionEnd writes the final session_end event.
func (s *Session) WriteCompaction(summary string, tailStartID string, tailMessages []MessagePayload, droppedEvents int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("session closed")
	}
	id, err := newID()
	if err != nil {
		return err
	}
	ev := CompactionEvent{
		Type:      TypeCompaction,
		ID:        id,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Summary: MessagePayload{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: summary}},
		},
		TailStartID:   tailStartID,
		TailMessages:  tailMessages,
		DroppedEvents: droppedEvents,
	}
	return s.encoder.Encode(ev)
}

func (s *Session) WriteSessionEnd(status string, exitCode int, totalUsage *Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("session closed")
	}

	ev := SessionEnd{
		Type:       TypeSessionEnd,
		ID:         s.id,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		Status:     status,
		ExitCode:   exitCode,
		TotalUsage: totalUsage,
	}

	return s.encoder.Encode(ev)
}

// ─── Close ────────────────────────────────────────────────────────────────

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.file.Close()
}

// ─── Accessors ────────────────────────────────────────────────────────────

func (s *Session) ID() string     { return s.id }
func (s *Session) IsResume() bool { return s.isResume }
func (s *Session) Path() string   { return s.path }

// Messages returns the parsed messages from a resumed session.
// Returns nil for a fresh session.
func (s *Session) Messages() []ParsedLine {
	if !s.isResume || s.path == "" {
		return nil
	}
	msgs, err := parseSessionFile(s.path)
	if err != nil {
		return nil
	}
	return msgs
}

// ─── Resume parsing ───────────────────────────────────────────────────────

// parseSessionFile reads a JSONL session file and returns parsed lines.
func LastNMessageMetadata(path string, n int) (string, int) {
	if n <= 0 {
		return "", 0
	}
	lines, err := parseSessionFile(path)
	if err != nil || len(lines) == 0 {
		return "", 0
	}
	count := 0
	startIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].MsgType == "user" || lines[i].MsgType == "assistant" || lines[i].MsgType == "toolResult" {
			count++
			startIdx = i
			if count == n {
				break
			}
		}
	}
	if startIdx == -1 {
		return "", 0
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(lines[startIdx].Raw), &raw); err != nil {
		return "", count
	}
	id, _ := raw["id"].(string)
	dropped := 0
	for _, line := range lines[:startIdx] {
		if line.MsgType == "user" || line.MsgType == "assistant" || line.MsgType == "toolResult" {
			dropped++
		}
	}
	return id, dropped
}

func parseSessionFile(path string) ([]ParsedLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []ParsedLine
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		raw := scanner.Text()
		if raw == "" {
			continue
		}

		// Determine event type from "type" field
		var typeHolder struct {
			Type EventType `json:"type"`
		}
		if err := json.Unmarshal([]byte(raw), &typeHolder); err != nil {
			continue // skip unparseable lines
		}

		pl := ParsedLine{Raw: raw}

		switch typeHolder.Type {
		case TypeSession:
			pl.MsgType = "header"
		case TypeMessage:
			var msgEv struct {
				Message struct {
					Role string `json:"role"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(raw), &msgEv); err == nil {
				pl.MsgType = msgEv.Message.Role
			} else {
				pl.MsgType = "unknown"
			}
		case TypeCompaction:
			pl.MsgType = "compaction"
		case TypeSessionEnd:
			pl.MsgType = "session_end"
		}

		lines = append(lines, pl)
	}

	return lines, scanner.Err()
}

// ─── Default path ─────────────────────────────────────────────────────────

// DefaultPath returns the default session file path:
//
//	~/.tyci/sessions/<encoded-cwd>/<ts>_<uuid>.jsonl
func DefaultPath(cwd string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	// Encode CWD by replacing / with --
	encoded := strings.ReplaceAll(cwd, "/", "--")
	if encoded == "" {
		encoded = "root"
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	id, err := newID()
	if err != nil {
		return "", err
	}

	filename := fmt.Sprintf("%s_%s.jsonl", ts, id)
	dir := filepath.Join(home, ".tyci", "sessions", encoded)
	return filepath.Join(dir, filename), nil
}

// ─── Resume: rebuild conversation ─────────────────────────────────────────

// RebuildMessages reconstructs []providers.RichMessage from parsed session lines.
// It walks forward from the last header, applying any compaction event and then
// collecting the effective tail message history.
func RebuildMessages(lines []ParsedLine) ([]providers.RichMessage, error) {
	var msgs []providers.RichMessage

	startIdx := 0
	for i, l := range lines {
		if l.MsgType == "header" {
			startIdx = i + 1
			break
		}
	}

	for _, l := range lines[startIdx:] {
		if l.MsgType == "session_end" {
			continue
		}
		if l.MsgType == "compaction" {
			compacted, ok := rebuildCompactionLine(l.Raw)
			if ok {
				msgs = compacted
			}
			continue
		}
		msg, ok := rebuildMessageLine(l.Raw)
		if !ok {
			continue
		}
		msgs = append(msgs, msg)
	}

	return msgs, nil
}

func rebuildCompactionLine(raw string) ([]providers.RichMessage, bool) {
	var ev struct {
		Summary struct {
			Role    string         `json:"role"`
			Content []ContentBlock `json:"content"`
		} `json:"summary"`
		TailMessages []struct {
			Role    string         `json:"role"`
			Content []ContentBlock `json:"content"`
		} `json:"tail_messages"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return nil, false
	}
	var msgs []providers.RichMessage
	if summary := contentBlocksToRichMessage(ev.Summary.Role, ev.Summary.Content); summary != nil {
		msgs = append(msgs, *summary)
	}
	for _, tail := range ev.TailMessages {
		if msg := contentBlocksToRichMessage(tail.Role, tail.Content); msg != nil {
			msgs = append(msgs, *msg)
		}
	}
	return msgs, true
}

func rebuildMessageLine(raw string) (providers.RichMessage, bool) {
	var ev struct {
		Message struct {
			Role    string         `json:"role"`
			Content []ContentBlock `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return providers.RichMessage{}, false
	}
	msg := contentBlocksToRichMessage(ev.Message.Role, ev.Message.Content)
	if msg == nil {
		return providers.RichMessage{}, false
	}
	return *msg, true
}

func contentBlocksToRichMessage(role string, blocks []ContentBlock) *providers.RichMessage {
	content := make([]providers.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		cb := providers.ContentBlock{
			Type:       block.Type,
			Text:       block.Text,
			Thinking:   block.Thinking,
			ID:         block.ID,
			Name:       block.Name,
			Arguments:  block.Arguments,
			IsError:    block.IsError,
			ToolCallID: block.ToolCallID,
			ToolName:   block.ToolName,
		}
		content = append(content, cb)
	}
	return &providers.RichMessage{Role: role, Content: content}
}

// ─── ReadCloser support ───────────────────────────────────────────────────

// ReadAllMessages reads a session file and returns all messages as raw JSON maps.
func ReadAllMessages(r io.Reader) ([]map[string]any, error) {
	var msgs []map[string]any
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		msgs = append(msgs, raw)
	}
	return msgs, scanner.Err()
}
