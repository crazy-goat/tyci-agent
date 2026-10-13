package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSessionRead_RequiresPath(t *testing.T) {
	wireFakeBrowser(t, &fakeBrowser{})
	res := (&SessionReadTool{}).Run(context.Background(), map[string]any{})
	if res.Success || !strings.Contains(res.Error, "path is required") {
		t.Errorf("want path required failure, got %+v", res)
	}
}

func TestSessionRead_PassesMaxMessages(t *testing.T) {
	f := &fakeBrowser{}
	wireFakeBrowser(t, f)
	res := (&SessionReadTool{}).Run(context.Background(), map[string]any{"path": "/s/one.jsonl", "max_messages": float64(500)})
	if !res.Success {
		t.Fatalf("want success, got %+v", res)
	}
	if f.gotMax != 100 {
		t.Errorf("max = %d, want 100", f.gotMax)
	}
	if f.gotPath != "/s/one.jsonl" {
		t.Errorf("path = %q, want /s/one.jsonl", f.gotPath)
	}
}

func TestSessionRead_DefaultMaxIs20(t *testing.T) {
	f := &fakeBrowser{}
	wireFakeBrowser(t, f)
	res := (&SessionReadTool{}).Run(context.Background(), map[string]any{"path": "/s/one.jsonl"})
	if !res.Success {
		t.Fatalf("want success, got %+v", res)
	}
	if f.gotMax != 20 {
		t.Errorf("max = %d, want 20", f.gotMax)
	}
}

func TestSessionRead_OutputShape(t *testing.T) {
	wireFakeBrowser(t, &fakeBrowser{
		readTitle: "t",
		readMsgs: []SessionMessage{
			{Role: "user", Text: "hi"},
			{Role: "assistant", Text: "hello"},
		},
		readTrunc: true,
	})
	res := (&SessionReadTool{}).Run(context.Background(), map[string]any{"path": "/s/one.jsonl"})
	if !res.Success {
		t.Fatalf("want success, got %+v", res)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("content %q is not a JSON object: %v", res.Content, err)
	}
	for _, k := range []string{"path", "title", "truncated", "messages"} {
		if _, ok := out[k]; !ok {
			t.Errorf("key %q missing in %v", k, out)
		}
	}
	if out["title"] != "t" || out["truncated"] != true || out["path"] != "/s/one.jsonl" {
		t.Errorf("out = %v", out)
	}
	msgs, ok := out["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Errorf("messages = %v, want 2 entries", out["messages"])
	}
}

func TestSessionRead_EmptyMessagesIsJSONArray(t *testing.T) {
	wireFakeBrowser(t, &fakeBrowser{readTitle: "t"})
	res := (&SessionReadTool{}).Run(context.Background(), map[string]any{"path": "/s/one.jsonl"})
	if !res.Success || !strings.Contains(res.Content, `"messages":[]`) {
		t.Errorf("want empty messages array, got %+v", res)
	}
}

func TestSessionRead_ErrorIsReturned(t *testing.T) {
	wireFakeBrowser(t, &fakeBrowser{readErr: errors.New("not listed")})
	res := (&SessionReadTool{}).Run(context.Background(), map[string]any{"path": "/etc/passwd"})
	if res.Success || !strings.Contains(res.Error, "not listed") {
		t.Errorf("want refusal, got %+v", res)
	}
}

func TestSessionRead_UnlistedPathIsRefused(t *testing.T) {
	// The browser decides which paths are listed. The tool must hand the
	// path to it and return its refusal, without reading the file itself.
	f := &fakeBrowser{readErr: errors.New("session \"/tmp/other.jsonl\" is not in the session list; use session_list first")}
	wireFakeBrowser(t, f)
	res := (&SessionReadTool{}).Run(context.Background(), map[string]any{"path": "/tmp/other.jsonl"})
	if res.Success || !strings.Contains(res.Error, "session_list") {
		t.Errorf("want refusal naming session_list, got %+v", res)
	}
	if f.gotPath != "/tmp/other.jsonl" {
		t.Errorf("browser got path %q, want the path the caller sent", f.gotPath)
	}
}

func TestSessionRead_NilBrowserFails(t *testing.T) {
	SetSessionBrowser(nil)
	res := (&SessionReadTool{}).Run(context.Background(), map[string]any{"path": "/s/one.jsonl"})
	if res.Success || !strings.Contains(res.Error, "unavailable") {
		t.Errorf("want unavailable failure, got %+v", res)
	}
}

func TestSessionRead_SubagentDenied(t *testing.T) {
	if !IsSubagentDenied("session_read") {
		t.Error("session_read must be denied to subagents")
	}
}
