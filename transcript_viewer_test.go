package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/jobs"
)

func TestTranscriptProvider_OKFalseForUnknown(t *testing.T) {
	resetResumableForTest(t)
	provider := buildTranscriptProvider()
	if _, _, ok := provider("no-such-id"); ok {
		t.Fatalf("expected ok=false for unknown id")
	}
}

func TestTranscriptProvider_FormatAndVerbatim(t *testing.T) {
	resetResumableForTest(t)
	jobID := "job-test-1"
	// Seed resumable entry with diverse blocks
	args := json.RawMessage(`{"task":"hello world","n":42}`)
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "do thing"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "ok"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "toolCall", Name: "write", Arguments: args}}},
		{Role: "tool", Content: []connector.ContentBlock{{Type: "toolResult", Text: "file written: /tmp/x"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "thinking", Thinking: "hmm let me think"}}},
	}
	stashResumable(jobID, resumableEntry{msgs: msgs})

	provider := buildTranscriptProvider()
	title, lines, ok := provider(jobID)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if title == "" {
		t.Fatalf("expected non-empty title")
	}
	// 5 messages -> 5 lines (one per block here)
	if len(lines) != 5 {
		t.Fatalf("want 5 lines, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "user:") || !strings.Contains(lines[0], "do thing") {
		t.Errorf("line0 = %q, want user text", lines[0])
	}
	if !strings.Contains(lines[2], "tool_call write") || !strings.Contains(lines[2], `"task":"hello world"`) {
		t.Errorf("tool_call line = %q, want verbatim args", lines[2])
	}
	if !strings.Contains(lines[3], "tool_result") || !strings.Contains(lines[3], "file written") {
		t.Errorf("tool_result line = %q", lines[3])
	}
	if !strings.HasPrefix(lines[4], "[4] thinking:") {
		t.Errorf("thinking line = %q, want prefix", lines[4])
	}
}

func TestTranscriptProvider_TruncationMarker(t *testing.T) {
	resetResumableForTest(t)
	jobID := "job-huge"
	huge := strings.Repeat("x", 6000)
	msgs := []connector.Message{
		{Role: "tool", Content: []connector.ContentBlock{{Type: "toolResult", Text: huge}}},
	}
	stashResumable(jobID, resumableEntry{msgs: msgs})
	_, lines, ok := buildTranscriptProvider()(jobID)
	if !ok {
		t.Fatalf("expected ok")
	}
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "…[+") || !strings.Contains(lines[0], "chars]") {
		t.Errorf("expected truncation marker in %q", lines[0][:200])
	}
	if strings.Contains(lines[0], huge[:100+0]) && len(lines[0]) >= len(huge) {
		t.Errorf("line should be truncated, got len %d", len(lines[0]))
	}
}

func TestTranscriptProvider_StripANSI(t *testing.T) {
	resetResumableForTest(t)
	jobID := "job-ansi"
	msgs := []connector.Message{
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "\x1b[31mred\x1b[0m text"}}},
	}
	stashResumable(jobID, resumableEntry{msgs: msgs})
	_, lines, _ := buildTranscriptProvider()(jobID)
	if strings.Contains(lines[0], "\x1b[") {
		t.Errorf("ANSI not stripped: %q", lines[0])
	}
	if !strings.Contains(lines[0], "red text") {
		t.Errorf("want red text, got %q", lines[0])
	}
}

func TestTranscriptProvider_DoesNotMutateStash(t *testing.T) {
	// Guard the deep copy itself (the append(json.RawMessage(nil)...) line),
	// not merely the formatted []string. Mutating formatted lines can never
	// alias the stashed entry's Arguments backing array, so the previous test
	// was vacuous — it would pass even if deepCopyMessages were removed.
	// This one copies via the helper, mutates the COPY's bytes in place,
	// and asserts the original is unchanged.
	msgs := []connector.Message{
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "toolCall", Name: "bash", Arguments: json.RawMessage(`{"a":1}`)}}},
	}
	copied := deepCopyMessages(msgs)
	// Mutate the copy's backing bytes in place.
	if len(copied[0].Content[0].Arguments) > 0 {
		copied[0].Content[0].Arguments[0] = 'X'
	}
	if string(msgs[0].Content[0].Arguments) != `{"a":1}` {
		t.Fatalf("deep copy aliased Arguments: original now %q", string(msgs[0].Content[0].Arguments))
	}
	// Also guard the stashed entry path via the real provider.
	resetResumableForTest(t)
	jobID := "job-immut"
	stashResumable(jobID, resumableEntry{msgs: msgs})
	// Read through the provider, then mutate via deepCopyMessages and re-read stash.
	resumableMu.Lock()
	stored := resumable[jobID].msgs
	resumableMu.Unlock()
	copy2 := deepCopyMessages(stored)
	if len(copy2[0].Content[0].Arguments) > 0 {
		copy2[0].Content[0].Arguments[0] = 'Y'
	}
	resumableMu.Lock()
	storedAgain := resumable[jobID].msgs[0].Content[0].Arguments
	resumableMu.Unlock()
	if string(storedAgain) != `{"a":1}` {
		t.Errorf("stash mutated via deep-copy alias: %q", string(storedAgain))
	}
}

func TestTranscriptProvider_TitleFromRegistry(t *testing.T) {
	resetResumableForTest(t)
	// Use a fresh registry for this test
	oldReg := JobRegistry
	JobRegistry = jobs.NewRegistry()
	defer func() { JobRegistry = oldReg }()

	j := JobRegistry.Start(context.Background(), "my desc", jobs.KindSubagent, "", func(ctx context.Context, id string) (string, bool, error) {
		// Sleep a bit so job stays running during provider call? No — make it finish fast
		return "result", false, nil
	})
	// Let it finish
	time.Sleep(20 * time.Millisecond)
	_ = j
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}}
	stashResumable(j.ID, resumableEntry{msgs: msgs})
	title, _, ok := buildTranscriptProvider()(j.ID)
	if !ok {
		t.Fatalf("expected ok")
	}
	if !strings.Contains(title, "my desc") {
		t.Errorf("title %q should contain description", title)
	}
}

func TestTranscriptProvider_CJKTruncationIsRuneBased(t *testing.T) {
	resetResumableForTest(t)
	jobID := "job-cjk"
	huge := strings.Repeat("漢", 5000)
	msgs := []connector.Message{
		{Role: "tool", Content: []connector.ContentBlock{{Type: "toolResult", Text: huge}}},
	}
	stashResumable(jobID, resumableEntry{msgs: msgs})
	_, lines, _ := buildTranscriptProvider()(jobID)
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "…[+") {
		t.Fatalf("expected truncation marker, got %q", lines[0][:200])
	}
	payload := strings.TrimPrefix(lines[0], "[0] tool_result ")
	markerIdx := strings.Index(payload, "…[+")
	if markerIdx < 0 {
		t.Fatalf("no marker in payload")
	}
	contentRunes := []rune(payload[:markerIdx])
	if len(contentRunes) != 4000 {
		t.Fatalf("CJK truncation: want 4000 runes before marker, got %d", len(contentRunes))
	}
}

func TestTranscriptProvider_TruncationMarkerUsesStrippedLen(t *testing.T) {
	resetResumableForTest(t)
	jobID := "job-stripped-marker"
	visible := strings.Repeat("a", 4001)
	raw := "\x1b[31m" + visible + "\x1b[0m"
	msgs := []connector.Message{
		{Role: "tool", Content: []connector.ContentBlock{{Type: "toolResult", Text: raw}}},
	}
	stashResumable(jobID, resumableEntry{msgs: msgs})
	_, lines, _ := buildTranscriptProvider()(jobID)
	if !strings.Contains(lines[0], "…[+1 chars]") {
		t.Errorf("marker should be +1 on stripped len (4001→4000), got %q", lines[0])
	}
}

func TestStripAnsiTranscript_CSIQuestionMark(t *testing.T) {
	// "\x1b[?25lhello\x1b[0m" — the old m/K/H/J-only terminator swallowed "hello"
	input := "\x1b[?25lhello\x1b[0m world"
	got := stripAnsiTranscript(input)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ANSI left in %q", got)
	}
	if got != "hello world" {
		t.Fatalf("want %q, got %q", "hello world", got)
	}
	// Also cover args path (strip is shared)
	visible := "\x1b[?25lhello"
	if stripAnsiTranscript(visible) != "hello" {
		t.Fatalf("strip failed for ?25l prefix")
	}
}
