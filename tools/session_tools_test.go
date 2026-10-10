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
	SetSessionRenamer(func(title string) (string, error) {
		got = title
		return title, nil
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
	SetSessionRenamer(func(string) (string, error) { t.Error("renamer must not run"); return "", nil })
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

func TestSessionRename_RemovesControlCharacters(t *testing.T) {
	var got string
	SetSessionRenamer(func(title string) (string, error) {
		got = title
		return title, nil
	})
	t.Cleanup(func() { SetSessionRenamer(nil) })

	res := (&SessionRenameTool{}).Run(context.Background(), map[string]any{"title": "a\x07b\x1b]2;x"})
	if !res.Success {
		t.Fatalf("want success, got %+v", res)
	}
	if want := "ab]2;x"; got != want {
		t.Errorf("renamer got %q, want %q", got, want)
	}
	if strings.ContainsAny(res.Content, "\x07\x1b") {
		t.Errorf("result %q still has control characters", res.Content)
	}
}

func TestSessionRename_CutsLongTitle(t *testing.T) {
	var got string
	SetSessionRenamer(func(title string) (string, error) {
		got = title
		return title, nil
	})
	t.Cleanup(func() { SetSessionRenamer(nil) })

	res := (&SessionRenameTool{}).Run(context.Background(), map[string]any{"title": strings.Repeat("x", 100)})
	if !res.Success {
		t.Fatalf("want success, got %+v", res)
	}
	if n := len([]rune(got)); n != 80 {
		t.Errorf("renamer got %d runes, want 80", n)
	}
}
