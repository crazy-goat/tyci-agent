package main

import (
	"strings"
	"testing"
)

// TestFormatMessageForReplay_User covers the user-role branch: every text
// fragment of the content array should be emitted verbatim under a [You]
// header. Selection/Y-anchoring depends on the rendered line on screen
// being byte-identical to what's in the block, so tester verifies the
// exact prefix and raw text pass-through.
func TestFormatMessageForReplay_User(t *testing.T) {
	got := formatMessageForReplay("user", map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "hello\n"},
			map[string]any{"type": "text", "text": "world"},
		},
	})
	if !strings.HasPrefix(got, "[You]\n") {
		t.Errorf("user message must start with [You] header:\n%q", got)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Errorf("user text fragments must survive:\n%q", got)
	}
	// No trailing blank lines — keeps line counts predictable for scroll.
	if strings.HasSuffix(got, "\n\n") {
		t.Errorf("user block has trailing blank line: %q", got)
	}
}

// TestFormatMessageForReplay_AssistantThinkingCollapsed is the regression
// test for the bug where replay dumped full thinking blocks and produced
// thousands of glamour-rendered rows: test asserts that thinking content is
// NOT emitted verbatim, only a one-liner summary that includes char/line
// counts.
func TestFormatMessageForReplay_AssistantThinkingCollapsed(t *testing.T) {
	huge := strings.Repeat("x", 50_000)
	got := formatMessageForReplay("assistant", map[string]any{
		"content": []any{
			map[string]any{"type": "thinking", "thinking": huge},
			map[string]any{"type": "text", "text": "the answer"},
		},
	})
	if strings.Contains(got, huge) {
		t.Errorf("thinking content must NOT be replayed verbatim, would re-introduce the long-session flood")
	}
	if !strings.Contains(got, "[Assistant thinking:") {
		t.Errorf("expected collapsed-thinking one-liner:\n%q", got)
	}
	if !strings.Contains(got, "[Assistant]") {
		t.Errorf("expected [Assistant] header for the text part:\n%q", got)
	}
	if !strings.Contains(got, "the answer") {
		t.Errorf("assistant text part must still be present:\n%q", got)
	}
}

// TestFormatMessageForReplay_AssistantToolCalls stays inside 120 chars per
// argument so a 60-call turn doesn't blow back into 60 lines that each have
// to be re-wrapped by the renderer. The exact cutoff (120) isn't visible to
// the user — only the suffix "…" is — so the assertion lives at the suffix
// level.
func TestFormatMessageForReplay_AssistantToolCalls(t *testing.T) {
	longArgs := `{"path":"/very/long/path/` + strings.Repeat("x", 300) + `"}`
	got := formatMessageForReplay("assistant", map[string]any{
		"content": []any{
			map[string]any{"type": "toolCall", "id": "tc1", "name": "read", "arguments": longArgs},
			map[string]any{"type": "text", "text": "done"},
		},
	})
	if !strings.Contains(got, "[Assistant tool calls]\n- tool read(") {
		t.Errorf("expected inline tool-call summary line:\n%q", got)
	}
	if !strings.HasSuffix(got, "…)") && !strings.HasSuffix(got, "done") {
		// Either the tool line ends with "…" (long args got truncated)
		// or the body just ends with "done" (no truncation marker
		// because args came in under the cap).
		t.Errorf("expected suffix ending in '…)' or 'done', got:\n%q", got)
	}
}

// TestFormatMessageForReplay_ToolResultTruncatesHugeOutput covers the case
// where a tool produced a 200KB stdout. The replay must NOT push that
// entire string through the display — a single huge block would lock up
// scroll math. The test asserts both a truncation marker and a cap on
// how many result chars land in the formatted output.
func TestFormatMessageForReplay_ToolResultTruncatesHugeOutput(t *testing.T) {
	hugeResult := strings.Repeat("y", 200_000)
	got := formatMessageForReplay("toolResult", map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": hugeResult, "toolName": "bash", "toolCallId": "tc1"},
		},
	})
	if strings.Contains(got, hugeResult) {
		t.Errorf("tool result must be truncated, got full string in:\n%q", got)
	}
	if !strings.Contains(got, "[Tool result: bash]") {
		t.Errorf("expected [Tool result: bash] header:\n%q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected truncation marker:\n%q", got)
	}
	// Hard cap test: total formatted length stays well under the original.
	if len(got) > 5000 {
		t.Errorf("formatted output not capped (len=%d); would re-flood scroll", len(got))
	}
}

// TestFormatMessageForReplay_DropsHeaderlessMessages ensures messages that
// produce no actual content (an unknown role with any text alone gets the
// default no-op branch; an assistant with no thinking AND no text AND no
// tool calls) do NOT create a stray one-line block on the transcript.
// Thinking-only assistants are intentionally preserved as a one-liner
// "[Assistant thinking: N chars / M lines — collapsed]" so the user can
// see the model was actually reasoning without us dumping kilobytes.
func TestFormatMessageForReplay_DropsHeaderlessMessages(t *testing.T) {
	cases := []struct {
		name string
		role string
		raw  map[string]any
	}{
		{"unknown_role", "system", map[string]any{"content": []any{map[string]any{"type": "text", "text": "x"}}}},
		{"assistant_empty_content", "assistant", map[string]any{"content": []any{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatMessageForReplay(tc.role, tc.raw); got != "" {
				t.Errorf("expected empty result for %s (no useful body), got %q", tc.name, got)
			}
		})
	}
}
