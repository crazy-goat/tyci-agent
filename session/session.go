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
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/internal/gitinfo"
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

	// ProjectRoot is the key this session is actually filed under: the git
	// toplevel for CWD (worktrees resolved to their main repo — see
	// gitinfo.ProjectRoot), or CWD itself outside a git repo. Recorded so
	// the derivation is inspectable/debuggable without recomputing it, and
	// empty on sessions written before this field existed (see ProjectKey
	// and migrateLegacyDirs for how those are still found).
	ProjectRoot string `json:"projectRoot,omitempty"`
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
		projectRoot, _ := ProjectKey(cwd)
		h := Header{
			Type:        TypeSession,
			Version:     1,
			ID:          id,
			Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
			CWD:         cwd,
			Model:       model,
			Provider:    provider,
			ProjectRoot: projectRoot,
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
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session closed")
	}

	id, err := newID()
	if err != nil {
		s.mu.Unlock()
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

	err = s.encoder.Encode(ev)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.refreshMarkdownDump()
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
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session closed")
	}
	id, err := newID()
	if err != nil {
		s.mu.Unlock()
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
	err = s.encoder.Encode(ev)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.refreshMarkdownDump()
}

// Compact appends a compaction event and regenerates the derived markdown
// dump. The JSONL remains the source of truth and is never deleted.
func (s *Session) Compact(summary, tailStartID string, tail []connector.Message, droppedEvents int) (string, error) {
	payload := make([]MessagePayload, len(tail))
	for i, msg := range tail {
		payload[i] = MessagePayload{Role: msg.Role, Content: ContentBlocksFromConnector(msg.Content)}
	}
	if err := s.WriteCompaction(summary, tailStartID, payload, droppedEvents); err != nil {
		return "", err
	}
	s.mu.Lock()
	if !s.closed {
		if err := s.file.Sync(); err != nil {
			s.mu.Unlock()
			return "", err
		}
	}
	s.mu.Unlock()
	return WriteMarkdownDump(s.path)
}

// dumpFieldCap bounds how much of any one field (message text, thinking, tool
// arguments, tool result) the markdown dump keeps inline. The dump exists to
// be grepped, and "what was that error" or "which file did I edit" almost
// always lives in the first few hundred characters — the raw JSONL (never
// deleted, see WriteCompaction's doc comment) still has the rest for the rare
// case that matters.
const dumpFieldCap = 4000

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[[:alpha:]]`)

// DumpPathFor returns the markdown dump path for a session JSONL file:
// deterministic and computable without touching disk, which lets a caller
// (e.g. a human-typed /compact) put the real path into a summary before the
// compaction event that will actually produce the file is even written.
//
// Guards against the pathological case of a session opened at a path that
// already ends in ".md" (e.g. --session notes.md): trimming the extension
// and appending ".md" would then equal the session path itself, and
// os.Create on it would truncate the live JSONL. Appending ".md" outright
// instead keeps the dump a strict sibling, never a collision.
func DumpPathFor(path string) string {
	dumpPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".md"
	if dumpPath == path {
		return path + ".md"
	}
	return dumpPath
}

// WriteMarkdownDump writes a grep-friendly, derived view beside the JSONL.
//
// Format constraints (item 10): the dump is searched with grep/rg, not read
// top-to-bottom, so every line stays on one line (never soft-wrapped — a
// wrapped line splits a path or identifier and grep stops finding it),
// carries a short stable self-identifying prefix (event index plus role or
// tool name, e.g. "[12] [bash]"), and writes paths/tool names/error text
// verbatim rather than paraphrased. ANSI escapes are stripped so a
// colour-coded error line still matches `grep error`.
func WriteMarkdownDump(path string) (string, error) {
	lines, err := parseSessionFile(path)
	if err != nil {
		return "", err
	}
	dumpPath := DumpPathFor(path)
	f, err := os.Create(dumpPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	writeDumpHeader(f, path, lines)

	for i, line := range lines {
		var ev struct {
			Type      EventType      `json:"type"`
			Timestamp string         `json:"timestamp"`
			Message   MessagePayload `json:"message"`
			Provider  string         `json:"provider,omitempty"`
			Model     string         `json:"model,omitempty"`
			Summary   MessagePayload `json:"summary"`
			Status    string         `json:"status,omitempty"`
			ExitCode  int            `json:"exit_code,omitempty"`
			TailStart string         `json:"tail_start_id,omitempty"`
			Dropped   int            `json:"dropped_events,omitempty"`
		}
		if json.Unmarshal([]byte(line.Raw), &ev) != nil {
			continue
		}
		n := i + 1
		switch ev.Type {
		case TypeSession:
			// The header is rendered separately at the top of the file; skip
			// it here so it is not duplicated line-for-line.
			continue
		case TypeMessage:
			writeMessageDumpLines(f, n, ev.Timestamp, ev.Provider, ev.Model, ev.Message)
		case TypeCompaction:
			text := dumpCap(cleanDumpText(messageText(ev.Summary)))
			fmt.Fprintf(f, "[%d] [compaction] %s dropped=%d tail_start=%s summary=%s\n",
				n, ev.Timestamp, ev.Dropped, orDash(ev.TailStart), text)
		case TypeSessionEnd:
			fmt.Fprintf(f, "[%d] [session_end] %s status=%s exit=%d\n", n, ev.Timestamp, ev.Status, ev.ExitCode)
		default:
			fmt.Fprintf(f, "[%d] [%s] %s\n", n, ev.Type, ev.Timestamp)
		}
	}
	return dumpPath, nil
}

// writeDumpHeader writes the metadata block a reader (or a `head`) sees
// first: agent, session id, project, model and date. It re-reads the
// session's own header line rather than trusting a caller-supplied value,
// since this whole file is a derived artifact regenerated from the JSONL.
func writeDumpHeader(f *os.File, path string, lines []ParsedLine) {
	fmt.Fprintf(f, "# tyci session dump\n\n")
	fmt.Fprintf(f, "- source: %s\n", path)
	for _, line := range lines {
		if line.MsgType != "header" {
			continue
		}
		var h Header
		if json.Unmarshal([]byte(line.Raw), &h) != nil {
			return
		}
		fmt.Fprintf(f, "- agent: tyci\n")
		fmt.Fprintf(f, "- session id: %s\n", h.ID)
		if h.ProjectRoot != "" {
			fmt.Fprintf(f, "- project: %s\n", h.ProjectRoot)
		} else if h.CWD != "" {
			fmt.Fprintf(f, "- project: %s\n", h.CWD)
		}
		if h.Provider != "" || h.Model != "" {
			fmt.Fprintf(f, "- model: %s/%s\n", h.Provider, h.Model)
		}
		if h.Timestamp != "" {
			fmt.Fprintf(f, "- date: %s\n", h.Timestamp)
		}
		break
	}
	fmt.Fprintf(f, "\n")
}

// writeMessageDumpLines emits one self-identifying, non-wrapping line per
// content block rather than squashing a whole turn into one line: a tool
// call and its eventual result are each exactly the kind of thing grepped
// for by name ("which file did I edit", "what was that error"), and merging
// them into one line per event would either bury or truncate one of them.
func writeMessageDumpLines(f *os.File, n int, ts, provider, model string, msg MessagePayload) {
	role := msg.Role
	if role == "" {
		role = "unknown"
	}
	modelSuffix := ""
	if role == "assistant" && (provider != "" || model != "") {
		modelSuffix = fmt.Sprintf(" model=%s/%s", provider, model)
	}
	wrote := false
	for _, b := range msg.Content {
		switch {
		case b.Type == "toolCall":
			wrote = true
			args := dumpCap(cleanDumpText(string(b.Arguments)))
			fmt.Fprintf(f, "[%d] [%s] %s call id=%s args=%s\n", n, orDash(b.Name), ts, b.ID, args)
		case role == "toolResult":
			wrote = true
			text := dumpCap(cleanDumpText(b.Text))
			fmt.Fprintf(f, "[%d] [%s] %s result id=%s error=%t %s\n", n, orDash(b.ToolName), ts, b.ToolCallID, b.IsError, text)
		case b.Type == "thinking" && b.Thinking != "":
			wrote = true
			fmt.Fprintf(f, "[%d] [thinking]%s %s %s\n", n, modelSuffix, ts, dumpCap(cleanDumpText(b.Thinking)))
		case b.Text != "":
			wrote = true
			fmt.Fprintf(f, "[%d] [%s]%s %s %s\n", n, role, modelSuffix, ts, dumpCap(cleanDumpText(b.Text)))
		}
	}
	if !wrote {
		// A content-less event (e.g. a skipped/empty assistant turn) still
		// gets one line so the event index in the prefix stays meaningful
		// against the raw JSONL line numbers a reader might cross-reference.
		fmt.Fprintf(f, "[%d] [%s]%s %s (empty)\n", n, role, modelSuffix, ts)
	}
}

// cleanDumpText strips ANSI escapes and collapses newlines so no field can
// ever break a dump line in two; grep depends on one match per line.
func cleanDumpText(s string) string {
	return strings.ReplaceAll(ansiEscape.ReplaceAllString(s, ""), "\n", " ")
}

// dumpCap truncates a field to dumpFieldCap runes. The marker names the
// cut explicitly rather than silently dropping the rest, and the raw JSONL
// (never deleted) is the place to look for the untruncated value.
func dumpCap(s string) string {
	r := []rune(s)
	if len(r) <= dumpFieldCap {
		return s
	}
	return string(r[:dumpFieldCap]) + fmt.Sprintf("...[truncated %d more chars]", len(r)-dumpFieldCap)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func messageText(msg MessagePayload) string {
	var out []string
	for _, b := range msg.Content {
		if b.Text != "" {
			out = append(out, b.Text)
		}
		if b.Thinking != "" {
			out = append(out, b.Thinking)
		}
	}
	return strings.Join(out, " ")
}

// refreshMarkdownDump keeps the derived dump current after ordinary event
// writes. The JSONL remains authoritative if regenerating the artifact fails.
func (s *Session) refreshMarkdownDump() error {
	if s.path == "" {
		return nil
	}
	_, err := WriteMarkdownDump(s.path)
	return err
}

func (s *Session) WriteSessionEnd(status string, exitCode int, totalUsage *Usage) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
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

	err := s.encoder.Encode(ev)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.refreshMarkdownDump()
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

// ─── Project keying ───────────────────────────────────────────────────────
//
// Sessions used to be keyed by the exact cwd they were started from, so
// "repo/", "repo/sub/", and each of repo's linked git worktrees each landed
// in a separate session pool — /resume in a subdirectory or a worktree could
// never find a session recorded from another one, even though all of those
// are "the same project" to a human. ProjectKey fixes that by keying on the
// git toplevel instead (worktrees resolved to their main repo, via
// gitinfo.ProjectRoot), falling back to the absolute cwd outside a git repo
// where the old per-directory behavior is still exactly what you'd want.

// ProjectKey returns the session-pool key for cwd: the git toplevel
// (worktrees resolved to their main repo) if cwd is inside a git repository,
// otherwise the absolute form of cwd itself.
func ProjectKey(cwd string) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	if root := gitinfo.ProjectRoot(abs); root != "" {
		return root, nil
	}
	return abs, nil
}

// encodeKey turns a project key into the directory-name-safe form used
// under ~/.tyci/sessions/. Mirrors the encoding sessions have always used
// for cwd, just applied to the (possibly different) project key now.
func encodeKey(key string) string {
	encoded := strings.ReplaceAll(key, "/", "--")
	if encoded == "" {
		encoded = "root"
	}
	return encoded
}

func sessionsRootDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tyci", "sessions"), nil
}

// ─── Default path ─────────────────────────────────────────────────────────

// DefaultPath returns the default session file path:
//
//	~/.tyci/sessions/<encoded-project-key>/<ts>_<uuid>.jsonl
func DefaultPath(cwd string) (string, error) {
	dir, err := SessionDir(cwd)
	if err != nil {
		return "", err
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	id, err := newID()
	if err != nil {
		return "", err
	}

	filename := fmt.Sprintf("%s_%s.jsonl", ts, id)
	return filepath.Join(dir, filename), nil
}

// ─── Resume: rebuild conversation ─────────────────────────────────────────

// RebuildMessages reconstructs []connector.Message from parsed session lines.
// It walks forward from the last header, applying any compaction event and then
// collecting the effective tail message history.
func RebuildMessages(lines []ParsedLine) ([]connector.Message, error) {
	var msgs []connector.Message

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

	return SanitizeMessageSequence(msgs), nil
}

func rebuildCompactionLine(raw string) ([]connector.Message, bool) {
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
	var msgs []connector.Message
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

func rebuildMessageLine(raw string) (connector.Message, bool) {
	var ev struct {
		Message struct {
			Role    string         `json:"role"`
			Content []ContentBlock `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return connector.Message{}, false
	}
	msg := contentBlocksToRichMessage(ev.Message.Role, ev.Message.Content)
	if msg == nil {
		return connector.Message{}, false
	}
	return *msg, true
}

func contentBlocksToRichMessage(role string, blocks []ContentBlock) *connector.Message {
	content := make([]connector.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		cb := connector.ContentBlock{
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
	return &connector.Message{Role: role, Content: content}
}

// SanitizeMessageSequence drops an orphan tool RESULT — one whose
// toolCallId was never seen among the toolCall blocks kept so far. This is
// the shape that shows up after compaction: the tail window can start
// mid-way through a multi-tool-call turn, keeping a later result whose call
// got summarized away (see TestRebuildMessages_DropsOrphanToolResults).
//
// Deliberately does NOT touch the opposite case — a trailing, unanswered
// tool CALL (the last message is an assistant turn whose tool calls have no
// recorded result yet) — because RebuildMessages/LoadForReplay use this for
// ordinary /resume, where that shape is a legitimate "the process died
// mid-tool-call" state the caller may still want to see and act on, not
// something to silently discard. Forking a transcript at an arbitrary cut
// point (ForkAtIndex/ForkAtEventID below) can land in that same spot for a
// different reason — there is no completing "the same call" in a forked
// context — and repairs it there instead, via dropTrailingUnansweredToolCalls.
//
// Exported so /btw's side-conversation fork, session forking, and any other
// caller that cuts a transcript share this repair instead of each growing
// its own.
func SanitizeMessageSequence(msgs []connector.Message) []connector.Message {
	if len(msgs) == 0 {
		return msgs
	}
	seenToolCalls := make(map[string]struct{})
	out := make([]connector.Message, 0, len(msgs))
	for _, msg := range msgs {
		keep := true
		if msg.Role == "toolResult" {
			for _, block := range msg.Content {
				if block.Type != "text" || block.ToolCallID == "" {
					continue
				}
				if _, ok := seenToolCalls[block.ToolCallID]; !ok {
					keep = false
					break
				}
			}
		}
		if !keep {
			continue
		}
		out = append(out, msg)
		for _, block := range msg.Content {
			if block.Type == "toolCall" && block.ID != "" {
				seenToolCalls[block.ID] = struct{}{}
			}
		}
	}
	return out
}

// dropTrailingUnansweredToolCalls is ForkAtIndex/ForkAtEventID's extra half
// of SanitizeMessageSequence's repair, applied only to a FORKED prefix (see
// SanitizeMessageSequence's doc comment for why it is not folded into that
// shared function): a prefix cut can leave the LAST message as an assistant
// turn whose toolCall blocks were never answered (their toolResult events,
// if any, come after the cut point and are not part of this slice). Left in
// place it makes the transcript invalid for replay — every provider requires
// a tool result for every tool_use in the immediately preceding assistant
// turn, and a fork has no way to ever produce the missing one. Only the last
// message can ever have this problem — every earlier assistant tool call is
// followed, in a well-formed transcript, by its result before the next
// assistant turn — so this only ever needs to look at the tail.
func dropTrailingUnansweredToolCalls(msgs []connector.Message) []connector.Message {
	if len(msgs) == 0 {
		return msgs
	}
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" {
		return msgs
	}
	hasToolCall := false
	kept := make([]connector.ContentBlock, 0, len(last.Content))
	for _, b := range last.Content {
		if b.Type == "toolCall" {
			hasToolCall = true
			continue
		}
		kept = append(kept, b)
	}
	if !hasToolCall {
		return msgs
	}
	if len(kept) == 0 {
		// The whole message was tool calls with no accompanying text or
		// thinking — drop it entirely rather than leave an empty assistant
		// turn dangling at the end of the transcript.
		return msgs[:len(msgs)-1]
	}
	out := make([]connector.Message, len(msgs))
	copy(out, msgs)
	out[len(out)-1] = connector.Message{Role: last.Role, Content: kept}
	return out
}

// SanitizeCompactionSequence applies the replay sanitizer plus the fork-style
// repair for a live compaction cut. A live compaction must not leave a trailing
// assistant tool call without its result: the next model request has no way to
// answer a call that was removed from the retained tail.
func SanitizeCompactionSequence(msgs []connector.Message) []connector.Message {
	return dropTrailingUnansweredToolCalls(SanitizeMessageSequence(msgs))
}

// ForkMessages returns an independent copy of msgs: a new backing array, so
// nothing appended to the fork afterwards can ever alias or mutate the
// original slice. The per-message Content slices are still shared (a fork
// never mutates a message in place, only appends whole new ones — the same
// assumption /btw's side-conversation fork has always relied on).
func ForkMessages(msgs []connector.Message) []connector.Message {
	forked := make([]connector.Message, len(msgs))
	copy(forked, msgs)
	return forked
}

// ForkMessagesWithTurn is ForkMessages plus a new user turn appended — the
// shape /btw's side-conversation fork needs (and, now, session forking and
// the "subagent" tool's inherit_history option): an independent copy of msgs
// with userText appended as a new user message, so nothing the fork appends
// next can ever alias or mutate the original.
func ForkMessagesWithTurn(msgs []connector.Message, userText string) []connector.Message {
	forked := make([]connector.Message, len(msgs), len(msgs)+1)
	copy(forked, msgs)
	return append(forked, connector.Message{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: userText}},
	})
}

// ForkAtIndex returns an independent, sanitized copy of the first n messages
// of msgs — the "transcript index" addressing for forking a LIVE, in-memory
// conversation (the counterpart to ForkAtEventID for a persisted session).
// n is the count of messages to keep (0 <= n <= len(msgs)), not a zero-based
// element index. Repairs a cut landing inside a tool-call/result pair in
// both directions: SanitizeMessageSequence for an orphan result, then
// dropTrailingUnansweredToolCalls for a trailing unanswered call — the
// latter applied here (and in ForkAtEventID), not folded into
// SanitizeMessageSequence itself, because that function also backs ordinary
// /resume, where an unanswered trailing call is a legitimate state to
// preserve rather than discard (see its doc comment).
func ForkAtIndex(msgs []connector.Message, n int) ([]connector.Message, error) {
	if n < 0 || n > len(msgs) {
		return nil, fmt.Errorf("fork index %d out of range (0..%d)", n, len(msgs))
	}
	cut := make([]connector.Message, n)
	copy(cut, msgs[:n])
	return dropTrailingUnansweredToolCalls(SanitizeMessageSequence(cut)), nil
}

// ForkAtEventID returns an independent, sanitized copy of the message
// history in the persisted session file at path, up to and including the
// event whose id is eventID — the "session event id" addressing for forking
// a PERSISTED session (the counterpart to ForkAtIndex for a live, in-memory
// conversation). Reuses the exact same id scheme session.WriteCompaction
// already records as tail_start_id, and the same RebuildMessages/
// SanitizeMessageSequence /resume already runs, plus the extra
// dropTrailingUnansweredToolCalls repair ForkAtIndex also applies — see its
// doc comment for why that extra step is fork-only rather than folded into
// SanitizeMessageSequence.
func ForkAtEventID(path, eventID string) ([]connector.Message, error) {
	if eventID == "" {
		return nil, fmt.Errorf("empty event id")
	}
	lines, err := parseSessionFile(path)
	if err != nil {
		return nil, err
	}
	cut := -1
	for i, l := range lines {
		if l.MsgType == "header" || l.MsgType == "session_end" {
			continue
		}
		if id, ok := eventLineID(l.Raw); ok && id == eventID {
			cut = i
			break
		}
	}
	if cut == -1 {
		return nil, fmt.Errorf("event id %q not found in session %s", eventID, path)
	}
	msgs, err := RebuildMessages(lines[:cut+1])
	if err != nil {
		return nil, err
	}
	return dropTrailingUnansweredToolCalls(msgs), nil
}

// eventLineID extracts the "id" field common to every event type (message,
// compaction, session_end) without decoding the rest of the line.
func eventLineID(raw string) (string, bool) {
	var h struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return "", false
	}
	return h.ID, h.ID != ""
}

// ContentBlocksFromConnector converts connector.ContentBlock to the
// session package's own ContentBlock — the two have identical structure
// (see agent/session_log.go's writeAssistantSessionEvent, which carries the
// same conversion inline) — for callers outside package agent that need to
// write a connector.Message's content into a session file, such as
// ForkNewSession-style session forking.
func ContentBlocksFromConnector(blocks []connector.ContentBlock) []ContentBlock {
	out := make([]ContentBlock, len(blocks))
	for i, cb := range blocks {
		out[i] = ContentBlock{
			Type:       cb.Type,
			Text:       cb.Text,
			Thinking:   cb.Thinking,
			ID:         cb.ID,
			Name:       cb.Name,
			Arguments:  cb.Arguments,
			IsError:    cb.IsError,
			ToolCallID: cb.ToolCallID,
			ToolName:   cb.ToolName,
		}
	}
	return out
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

// SessionDir returns the directory where session files for the project
// containing cwd are stored: ~/.tyci/sessions/<encoded-project-key>/ (see
// ProjectKey). Before returning, it runs a best-effort migration that pulls
// in any pre-existing sessions filed under the old exact-cwd keying that
// belong to this same project (see migrateLegacyDirs) — so `/resume` and
// `tyci session list` keep finding sessions recorded before this change.
func SessionDir(cwd string) (string, error) {
	root, err := sessionsRootDir()
	if err != nil {
		return "", err
	}
	key, err := ProjectKey(cwd)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, encodeKey(key))
	migrateLegacyDirs(root, dir, key)
	return dir, nil
}

// AllProjectDirs returns every per-project session directory under
// ~/.tyci/sessions, for the "--all" escape hatch that lists sessions across
// every project rather than scoping to the current one.
func AllProjectDirs() ([]string, error) {
	root, err := sessionsRootDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(root, e.Name()))
		}
	}
	return dirs, nil
}

// migrateLegacyDirs is the backward-compat path for the cwd->project-key
// rewrite above: a session recorded before this change is filed under
// ~/.tyci/sessions/<encoded exact cwd it was started from>, which no longer
// matches any project's directory unless that project happens to always be
// run from its own git toplevel. Those old sessions still carry the cwd
// they were started from in Header.CWD (ProjectRoot is empty — the field
// didn't exist yet), so recomputing ProjectKey(header.CWD) recovers which
// project they actually belong to.
//
// All files within one legacy directory share the same recorded cwd by
// construction (that directory *is* the encoding of that cwd), so peeking
// at just the first session's header is enough to decide the whole
// directory's fate — this keeps the cost to one small read per legacy
// directory, not per session file, regardless of how much history a project
// has. Matching directories are merged into targetDir file-by-file (a
// timestamp+uuid filename can't collide) and removed once empty. Every step
// is best-effort: a permissions error or a directory that isn't ours to
// touch just leaves that directory in place for next time rather than
// failing the caller's session lookup.
func migrateLegacyDirs(sessionsRoot, targetDir, targetKey string) {
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(sessionsRoot, e.Name())
		if dir == targetDir {
			continue
		}
		files, err := os.ReadDir(dir)
		if err != nil || len(files) == 0 {
			continue
		}
		var jsonlNames []string
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".jsonl") {
				jsonlNames = append(jsonlNames, f.Name())
			}
		}
		if len(jsonlNames) == 0 {
			continue
		}
		h, ok := peekHeader(filepath.Join(dir, jsonlNames[0]))
		if !ok || h.ProjectRoot != "" {
			// Already new-format (or unreadable): nothing to migrate here.
			continue
		}
		key, err := ProjectKey(h.CWD)
		if err != nil || key != targetKey {
			continue
		}
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			continue
		}
		moved := 0
		for _, name := range jsonlNames {
			src := filepath.Join(dir, name)
			dst := filepath.Join(targetDir, name)
			if err := os.Rename(src, dst); err == nil {
				moved++
			}
		}
		if moved == len(jsonlNames) {
			_ = os.Remove(dir) // best-effort; fails silently if not empty
		}
	}
}

// peekHeader reads just the first non-empty line of path and parses it as a
// Header. Used by migrateLegacyDirs to classify a whole directory without
// reading every session file in it.
func peekHeader(path string) (Header, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var h Header
		if err := json.Unmarshal([]byte(line), &h); err != nil {
			return Header{}, false
		}
		return h, true
	}
	return Header{}, false
}

// SessionDirFromPath returns the directory segment of a session file path,
// i.e. everything up to and including the encoded cwd directory. Useful for
// resuming from a path: callers pass the file's directory to ListEntries.
func SessionDirFromPath(path string) string {
	return filepath.Dir(path)
}

// ListEntries returns session files in dir, newest first. Each entry includes
// the path, file size, and modification time. Files with extension .jsonl are
// listed; everything else (lockfiles, partials) is ignored.
func ListEntries(dir string) ([]SessionEntry, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]SessionEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, SessionEntry{
			Path:    filepath.Join(dir, e.Name()),
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ModTime.After(out[j].ModTime)
	})
	return out, nil
}

// SessionEntry is one row in the output of ListEntries.
type SessionEntry struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
}

// DeleteSession removes a session file. It does not refuse to delete files
// that look like correct JSONL, only refuses missing paths so callers get a
// clear error instead of a silent success.
func DeleteSession(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return nil
}

// ─── Replay helper used by /resume ────────────────────────────────────────

// TotalUsage is the aggregated token/cost summary emitted by LoadForReplay.
// It mirrors the shape used by interactive session output so the caller can
// print it directly without re-summing.
type TotalUsage struct {
	Input       int
	Output      int
	Reasoning   int
	CacheRead   int
	CacheWrite  int
	TotalTokens int
	TotalCost   float64
}

// TotalUsageFromMap builds a TotalUsage from a key→count map (as produced by
// keeping raw "input"/"output" sums during a single replay). It is a tiny
// convenience helper kept here so the same code path can be reused.
func TotalUsageFromMap(m map[string]int) TotalUsage {
	return TotalUsage{
		Input:  m["input"],
		Output: m["output"],
	}
}

// ReplaySummary is the data returned by LoadForReplay for swapping an
// interactive session onto a different on-disk file.
type ReplaySummary struct {
	ID       string
	Provider string
	Model    string

	Messages []connector.Message
	Usage    Usage

	CorruptLines int
}

// LoadForReplay reads a session file, accumulates usage and rebuilds the
// conversation. It is the entry point used by the interactive /resume command.
func LoadForReplay(path string) (ReplaySummary, []connector.Message, TotalUsage, []string, error) {
	var zero ReplaySummary
	lines, err := parseSessionFile(path)
	if err != nil {
		return zero, nil, TotalUsage{}, nil, err
	}

	var corrupt []string
	var summary ReplaySummary
	var msgs []connector.Message
	usage := Usage{}
	total := TotalUsage{}

	for _, l := range lines {
		switch l.MsgType {
		case "header":
			var h Header
			if err := json.Unmarshal([]byte(l.Raw), &h); err == nil {
				summary.ID = h.ID
				summary.Provider = h.Provider
				summary.Model = h.Model
			}
		case "compaction":
			rebuilt, ok := rebuildCompactionLine(l.Raw)
			if !ok {
				corrupt = append(corrupt, l.Raw)
				continue
			}
			msgs = rebuilt
		case "session_end":
			continue
		case "user", "assistant", "toolResult":
			msg, ok := rebuildMessageLine(l.Raw)
			if !ok {
				corrupt = append(corrupt, l.Raw)
				continue
			}
			msgs = append(msgs, msg)
			if l.MsgType == "assistant" {
				var ev struct {
					Usage *Usage `json:"usage"`
				}
				if jerr := json.Unmarshal([]byte(l.Raw), &ev); jerr == nil && ev.Usage != nil {
					usage.Input += ev.Usage.Input
					usage.Output += ev.Usage.Output
					usage.Reasoning += ev.Usage.Reasoning
					usage.CacheRead += ev.Usage.CacheRead
					usage.CacheWrite += ev.Usage.CacheWrite
					if ev.Usage.TotalTokens > 0 {
						usage.TotalTokens += ev.Usage.TotalTokens
					}
					if ev.Usage.TotalCost > 0 {
						usage.TotalCost += ev.Usage.TotalCost
					}
				}
			}
		default:
			corrupt = append(corrupt, l.Raw)
		}
	}

	total.Input = usage.Input
	total.Output = usage.Output
	total.Reasoning = usage.Reasoning
	total.CacheRead = usage.CacheRead
	total.CacheWrite = usage.CacheWrite
	total.TotalTokens = usage.TotalTokens
	total.TotalCost = usage.TotalCost

	summary.Messages = msgs
	summary.Usage = usage
	summary.CorruptLines = len(corrupt)

	return summary, msgs, total, corrupt, nil
}
