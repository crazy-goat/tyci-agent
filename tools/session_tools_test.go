package tools

import (
	"context"
	"strings"
	"testing"
)

func TestSessionRename_NilRenamerFails(t *testing.T) {
	SetSessionRenamer(nil)
	res := (&SessionRenameTool{}).Run(context.Background(), map[string]any{"title": "x"})
	if res.Success || !strings.Contains(res.Error, "unavailable") {
		t.Errorf("want unavailable failure, got %+v", res)
	}
}

func TestSessionRename_CallsRenamer(t *testing.T) {
	var got string
	SetSessionRenamer(func(title string) error {
		got = title
		return nil
	})
	t.Cleanup(func() { SetSessionRenamer(nil) })

	res := (&SessionRenameTool{}).Run(context.Background(), map[string]any{"title": "  Plan the API  "})
	if !res.Success {
		t.Fatalf("want success, got %+v", res)
	}
	if got != "Plan the API" {
		t.Errorf("renamer got %q, want %q", got, "Plan the API")
	}
	if !strings.Contains(res.Content, "Plan the API") {
		t.Errorf("content %q does not contain the title", res.Content)
	}
}

func TestSessionRename_EmptyTitleFails(t *testing.T) {
	SetSessionRenamer(func(string) error { t.Error("renamer must not run"); return nil })
	t.Cleanup(func() { SetSessionRenamer(nil) })
	res := (&SessionRenameTool{}).Run(context.Background(), map[string]any{"title": "   "})
	if res.Success {
		t.Errorf("want failure for empty title, got %+v", res)
	}
}

func TestSessionRename_NotInSubagentSchema(t *testing.T) {
	if !IsSubagentDenied("session_rename") {
		t.Error("session_rename must be denied to subagents")
	}
}
