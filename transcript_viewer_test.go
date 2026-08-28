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
	resetResumableForTest(t)
	jobID := "job-immut"
	args := json.RawMessage(`{"a":1}`)
	msgs := []connector.Message{
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "toolCall", Name: "bash", Arguments: args}}},
	}
	stashResumable(jobID, resumableEntry{msgs: msgs})

	provider := buildTranscriptProvider()
	_, lines, _ := provider(jobID)
	// Mutate returned lines and try to mutate stashed entry via second read
	lines[0] = "mutated"
	resumableMu.Lock()
	stored := resumable[jobID].msgs[0].Content[0].Arguments
	resumableMu.Unlock()
	if string(stored) != `{"a":1}` {
		t.Errorf("stash mutated: %q", string(stored))
	}
	_, lines2, _ := provider(jobID)
	if lines2[0] == "mutated" {
		t.Errorf("second read returned mutated content")
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
