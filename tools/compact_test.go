package tools

import (
	"context"
	"strings"
	"testing"
)

func TestCompactToolRequiresSummaryAndOwner(t *testing.T) {
	tool := &CompactTool{}
	if res := tool.Run(context.Background(), map[string]any{}); res.Success || !strings.Contains(res.Error, "summary") {
		t.Fatalf("missing summary: %+v", res)
	}
	if res := tool.Run(context.Background(), map[string]any{"summary": "keep this"}); res.Success || !strings.Contains(res.Error, "unavailable") {
		t.Fatalf("missing owner: %+v", res)
	}
}

func TestCompactToolCallsOwner(t *testing.T) {
	ctx := WithCompactor(context.Background(), func(summary, focus string) (string, error) {
		if summary != "keep" || focus != "tests" {
			t.Fatalf("got %q/%q", summary, focus)
		}
		return "/tmp/session.md", nil
	})
	res := (&CompactTool{}).Run(ctx, map[string]any{"summary": " keep ", "focus": " tests "})
	if !res.Success || !strings.Contains(res.Content, "/tmp/session.md") {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestCompactIsHiddenFromSubagents(t *testing.T) {
	for _, schema := range GetSubagentToolsSchema() {
		fn, ok := schema["function"].(map[string]any)
		if ok && fn["name"] == "compact" {
			t.Fatal("compact must not be offered to subagents")
		}
	}
	if !IsSubagentDenied("compact") {
		t.Fatal("compact must be denied at runtime for subagents")
	}
	if err := DenySubagentRecursion()("compact"); err == nil {
		t.Fatal("compact must be denied at runtime for unrestricted subagents")
	}
}

func TestCompactToolRejectsEmptySummaryEvenWithOwner(t *testing.T) {
	ctx := WithCompactor(context.Background(), func(summary, focus string) (string, error) {
		t.Fatal("empty summary must not invoke compactor")
		return "", nil
	})
	res := (&CompactTool{}).Run(ctx, map[string]any{"summary": "  "})
	if res.Success || !strings.Contains(res.Error, "summary") {
		t.Fatalf("expected empty-summary validation error, got %+v", res)
	}
}
