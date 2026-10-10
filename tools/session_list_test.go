package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeBrowser records the arguments of List and returns items.
type fakeBrowser struct {
	items  []SessionInfo
	gotCwd string
	gotAll bool
}

func (f *fakeBrowser) List(cwd string, all bool) ([]SessionInfo, error) {
	f.gotCwd = cwd
	f.gotAll = all
	return f.items, nil
}

func wireFakeBrowser(t *testing.T, f *fakeBrowser) {
	t.Helper()
	SetSessionBrowser(f)
	t.Cleanup(func() { SetSessionBrowser(nil) })
}

func decodeRows(t *testing.T, content string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(content), &rows); err != nil {
		t.Fatalf("content %q is not a JSON array: %v", content, err)
	}
	return rows
}

func TestSessionList_PassesCwdAndAll(t *testing.T) {
	f := &fakeBrowser{}
	wireFakeBrowser(t, f)

	res := (&SessionListTool{}).Run(context.Background(), map[string]any{"all": true})
	if !res.Success {
		t.Fatalf("want success, got %+v", res)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if f.gotCwd != wd {
		t.Errorf("cwd = %q, want %q", f.gotCwd, wd)
	}
	if !f.gotAll {
		t.Error("all = false, want true")
	}
}

func TestSessionList_LimitDefaultsAndCaps(t *testing.T) {
	items := make([]SessionInfo, 60)
	for i := range items {
		items[i] = SessionInfo{Path: "p"}
	}
	cases := []struct {
		name  string
		input map[string]any
		want  int
	}{
		{"default", map[string]any{}, 10},
		{"above cap", map[string]any{"limit": float64(500)}, 50},
		{"zero", map[string]any{"limit": float64(0)}, 10},
		{"negative", map[string]any{"limit": float64(-3)}, 10},
		{"int type", map[string]any{"limit": 7}, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wireFakeBrowser(t, &fakeBrowser{items: items})
			res := (&SessionListTool{}).Run(context.Background(), tc.input)
			if !res.Success {
				t.Fatalf("want success, got %+v", res)
			}
			if got := len(decodeRows(t, res.Content)); got != tc.want {
				t.Errorf("rows = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSessionList_RowHasTitleAndPrompt(t *testing.T) {
	wireFakeBrowser(t, &fakeBrowser{items: []SessionInfo{{
		Path:        "/s/one.jsonl",
		Title:       "fix bug",
		FirstPrompt: "hello",
		Modified:    time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC),
	}}})
	res := (&SessionListTool{}).Run(context.Background(), map[string]any{})
	rows := decodeRows(t, res.Content)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0]["title"] != "fix bug" || rows[0]["first_prompt"] != "hello" {
		t.Errorf("row = %v", rows[0])
	}
	if rows[0]["path"] != "/s/one.jsonl" || rows[0]["modified"] != "2026-10-10T08:00:00Z" {
		t.Errorf("row = %v", rows[0])
	}
}

func TestSessionList_EmptyIsJSONArray(t *testing.T) {
	wireFakeBrowser(t, &fakeBrowser{})
	res := (&SessionListTool{}).Run(context.Background(), map[string]any{})
	if !res.Success || res.Content != "[]" {
		t.Errorf("want [] success, got %+v", res)
	}
}

func TestSessionList_NilBrowserFails(t *testing.T) {
	SetSessionBrowser(nil)
	res := (&SessionListTool{}).Run(context.Background(), map[string]any{})
	if res.Success || !strings.Contains(res.Error, "unavailable") {
		t.Errorf("want unavailable failure, got %+v", res)
	}
}

func TestSessionList_SubagentDenied(t *testing.T) {
	if !IsSubagentDenied("session_list") {
		t.Error("session_list must be denied to subagents")
	}
}
