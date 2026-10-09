package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestLuaToolRun_RecordsOwner: a Lua run records the job id from its context
// as the owner. A run with no job id, as the main conversation makes, has an
// empty owner.
func TestLuaToolRun_RecordsOwner(t *testing.T) {
	defer SnapshotLuaRunHistoryForTesting()()

	dir := t.TempDir()
	script := `return {
  schema = {name = "owned", description = "test", parameters = {}},
  run = function(ctx, args) return {success = true, content = "ok"} end
}`
	if err := os.WriteFile(filepath.Join(dir, "owned.lua"), []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLuaTools(dir)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("LoadLuaTools: err=%v, tools=%d", err, len(loaded))
	}

	loaded[0].Run(context.WithValue(context.Background(), JobIDCtxKey{}, "sub-1"), map[string]any{})
	loaded[0].Run(context.Background(), map[string]any{})

	hist := LuaRunHistory()
	if len(hist) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(hist))
	}
	if hist[0].Owner != "sub-1" {
		t.Errorf("run from a subagent: owner = %q, want %q", hist[0].Owner, "sub-1")
	}
	if hist[1].Owner != "" {
		t.Errorf("run from main: owner = %q, want empty", hist[1].Owner)
	}
}
