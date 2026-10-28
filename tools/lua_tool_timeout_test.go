package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadLuaTool_TopLevelLoopTimesOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loop.lua")
	if err := os.WriteFile(path, []byte(`while true do end`), 0o644); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	tool, err := loadLuaTool(path)
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("load: tool=%v error=%v, want deadline exceeded", tool, err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("load took %s, want the 5s load limit", elapsed)
	}
}

func TestLuaTool_RunAppliesDefaultDeadline(t *testing.T) {
	var callCtx context.Context
	defer replaceRegisteredTool(t, "bash", func(ctx context.Context, _ map[string]any) ToolResult {
		callCtx = ctx
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("ctx.run received no deadline")
		} else if remaining := time.Until(deadline); remaining <= 295*time.Second || remaining > 300*time.Second {
			t.Errorf("ctx.run deadline is %s away, want 300s", remaining)
		}
		return ToolResult{Type: "result", Success: true, Content: "ok"}
	})()
	tool := luaToolFromSource(t, `return {schema = {name = "deadline"}, run = function(ctx, args)
		return {success = true, content = ctx.run("deadline")}
	end}`)

	res := tool.Run(context.Background(), nil)
	if !res.Success || res.Content != "ok" {
		t.Fatalf("run: %+v", res)
	}
	if callCtx == nil {
		t.Fatal("ctx.run did not reach the bash tool")
	}
	if err := callCtx.Err(); err != context.Canceled {
		t.Errorf("run context after return: %v, want canceled", err)
	}
}

func TestLuaTool_RunInternalTimeout(t *testing.T) {
	for _, tt := range []struct {
		name   string
		source string
	}{
		{"run function", `return {schema = {name = "loop"}, run = function(ctx, args)
			while true do end
		end}`},
		{"top-level chunk", `if ctx then while true do end end
			return {schema = {name = "loop"}, run = function(ctx, args) return {success = true} end}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tool := luaToolFromSource(t, tt.source)
			started := time.Now()
			// Use a short internal budget without giving the caller a deadline.
			res := tool.run(context.Background(), nil, 50*time.Millisecond)
			if res.Success || !strings.Contains(res.Error, context.DeadlineExceeded.Error()) {
				t.Fatalf("run: %+v, want deadline exceeded", res)
			}
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("loop took %s, want the 50ms internal limit", elapsed)
			}
		})
	}
}

func TestLuaTool_RunHonorsEarlierCallerDeadline(t *testing.T) {
	tool := luaToolFromSource(t, `return {schema = {name = "caller-deadline"}, run = function(ctx, args)
		while true do end
	end}`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	res := tool.Run(ctx, nil)
	if res.Success || !strings.Contains(res.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("run: %+v, want the caller's deadline exceeded", res)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("loop took %s, want the caller's 50ms limit", elapsed)
	}
}
