package display

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/crazy-goat/tyci-agent/connector"
)

func assistantBlocks(blocks ...connector.ContentBlock) connector.Message {
	return connector.Message{Role: "assistant", Content: blocks}
}

func toolCallBlock(id, name, args string) connector.ContentBlock {
	return connector.ContentBlock{Type: "toolCall", ID: id, Name: name, Arguments: json.RawMessage(args)}
}

func toolResultMsg(id string, isError bool) connector.Message {
	return connector.Message{Role: "toolResult", Content: []connector.ContentBlock{
		{Type: "toolResult", ToolCallID: id, IsError: isError, Text: "BODY-SECRET-OUTPUT"},
	}}
}

func TestFormatAgentLogLines_ToolCallOneLine(t *testing.T) {
	args := `{"command":"echo one\necho two\necho three"}`
	msgs := []connector.Message{assistantBlocks(toolCallBlock("c1", "bash", args))}
	lines := formatAgentLogLines(msgs)
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d: %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "tool bash(") || !strings.HasSuffix(lines[0], "  …") {
		t.Errorf("unexpected line %q", lines[0])
	}
}

func TestFormatAgentLogLines_ThinkingOneLine(t *testing.T) {
	long := strings.Repeat("word ", 80) // 400 characters over 5 lines
	long = strings.Replace(long, "word word", "word\nword", 2)
	msgs := []connector.Message{assistantBlocks(connector.ContentBlock{Type: "thinking", Thinking: long})}
	lines := formatAgentLogLines(msgs)
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	body, ok := strings.CutPrefix(lines[0], "thinking: ")
	if !ok {
		t.Fatalf("missing prefix: %q", lines[0])
	}
	if !strings.HasSuffix(body, "…") {
		t.Fatalf("long thinking must end with …: %q", body)
	}
	if n := utf8.RuneCountInString(strings.TrimSuffix(body, "…")); n != agentLogThinkingRunes {
		t.Errorf("want %d runes before …, got %d", agentLogThinkingRunes, n)
	}
	if strings.ContainsAny(lines[0], "\n\r") {
		t.Errorf("line must not hold a newline: %q", lines[0])
	}
}

func TestFormatAgentLogLines_ResultStatus(t *testing.T) {
	msgs := []connector.Message{
		assistantBlocks(
			toolCallBlock("ok1", "read", `{"path":"a.go"}`),
			toolCallBlock("err1", "read", `{"path":"b.go"}`),
			toolCallBlock("run1", "read", `{"path":"c.go"}`),
		),
		toolResultMsg("ok1", false),
		toolResultMsg("err1", true),
	}
	lines := formatAgentLogLines(msgs)
	want := []string{
		"tool read(a.go)  ok",
		"tool read(b.go)  err",
		"tool read(c.go)  …",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q, want %q", lines, want)
	}
}

func TestFormatAgentLogLines_NoResultBodies(t *testing.T) {
	msgs := []connector.Message{
		assistantBlocks(toolCallBlock("c1", "read", `{"path":"a.go"}`)),
		toolResultMsg("c1", false),
	}
	for _, l := range formatAgentLogLines(msgs) {
		if strings.Contains(l, "BODY-SECRET-OUTPUT") {
			t.Errorf("tool result body shown: %q", l)
		}
	}
}

func TestFormatAgentLogLines_StripsANSI(t *testing.T) {
	msgs := []connector.Message{
		assistantBlocks(
			connector.ContentBlock{Type: "text", Text: "\x1b[2Jcleared\x1b]0;title\x07 and\rback\x00"},
			connector.ContentBlock{Type: "thinking", Thinking: "\x1b[31mred\x1b[0m"},
			toolCallBlock("c1", "read", "{\"path\":\"\\u001b[H.go\"}"),
		),
	}
	for _, l := range formatAgentLogLines(msgs) {
		if strings.ContainsAny(l, "\x1b\r\x00\x07\n") {
			t.Errorf("control character in %q", l)
		}
	}
}

func TestFormatAgentLogLines_OperatorNote(t *testing.T) {
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "stop\nnow"}}},
	}
	lines := formatAgentLogLines(msgs)
	if len(lines) != 1 || lines[0] != "operator: stop now" {
		t.Errorf("got %q, want one operator line", lines)
	}
}

func TestFormatAgentLogLines_LongArgsTruncated(t *testing.T) {
	big := strings.Repeat("x", 10*1024)
	args, _ := json.Marshal(map[string]string{"path": big})
	msgs := []connector.Message{assistantBlocks(toolCallBlock("c1", "read", string(args)))}
	lines := formatAgentLogLines(msgs)
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	if len(lines[0]) > 200 {
		t.Errorf("line too long: %d bytes", len(lines[0]))
	}
}

func TestFormatAgentLogLines_TextNoteCapped(t *testing.T) {
	text := "first line\nsecond line " + strings.Repeat("y", 500)
	msgs := []connector.Message{assistantBlocks(connector.ContentBlock{Type: "text", Text: text})}
	lines := formatAgentLogLines(msgs)
	if len(lines) != 1 || strings.Contains(lines[0], "\n") {
		t.Fatalf("want one line, got %q", lines)
	}
	if n := utf8.RuneCountInString(lines[0]); n != agentLogTextRunes+1 {
		t.Errorf("want %d runes (cap plus …), got %d", agentLogTextRunes+1, n)
	}
}
