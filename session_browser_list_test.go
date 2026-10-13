package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/crazy-goat/tyci-agent/tools"
)

func TestSessionListAll_MarksSessionsOfOtherProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	otherCwd := t.TempDir()
	writeSessionFile(t, otherCwd, []string{
		`{"type":"session","id":"o","projectRoot":"` + otherCwd + `"}`,
		`{"type":"message","id":"o1","message":{"role":"user","content":[{"type":"text","text":"other project"}]}}`,
	})
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	currentPath := writeSessionFile(t, cwd, []string{
		`{"type":"session","id":"c","projectRoot":"` + cwd + `"}`,
		`{"type":"message","id":"c1","message":{"role":"user","content":[{"type":"text","text":"current project"}]}}`,
	})

	tools.SetSessionBrowser(sessionBrowserAdapter{})
	t.Cleanup(func() { tools.SetSessionBrowser(nil) })
	res := (&tools.SessionListTool{}).Run(context.Background(), map[string]any{"all": true})
	if !res.Success {
		t.Fatalf("session_list all: %+v", res)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(res.Content), &rows); err != nil {
		t.Fatalf("content is not a JSON array: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %s", len(rows), res.Content)
	}
	for _, r := range rows {
		if r["path"] == currentPath {
			if _, marked := r["readable"]; marked {
				t.Errorf("current project row has a readable field: %v", r)
			}
			continue
		}
		if r["readable"] != false {
			t.Errorf("other project row is not marked readable=false: %v", r)
		}
	}
}
