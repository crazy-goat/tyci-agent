package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLuaToolLoading(t *testing.T) {
	// Create temp directory with test tool
	tmpDir := t.TempDir()
	toolContent := `return {
  schema = {
    name = "test-lua",
    description = "A test tool",
    parameters = {
      input = {type = "string", description = "Input text"}
    }
  },
  run = function(ctx, args)
    return {success = true, content = "Result: " .. (args.input or "none")}
  end
}`
	if err := os.WriteFile(filepath.Join(tmpDir, "test.lua"), []byte(toolContent), 0644); err != nil {
		t.Fatalf("Failed to write test tool: %v", err)
	}

	// Load tools from temp directory
	tools, err := LoadLuaTools(tmpDir)
	if err != nil {
		t.Fatalf("Failed to load lua tools: %v", err)
	}

	if len(tools) == 0 {
		t.Fatal("No lua tools found")
	}

	// Find the test-lua tool
	var testTool *LuaTool
	for _, tool := range tools {
		if tool.Name() == "test-lua" {
			testTool = tool
			break
		}
	}

	if testTool == nil {
		t.Fatal("test-lua tool not found")
	}

	// Run the tool
	ctx := context.Background()
	result := testTool.Run(ctx, map[string]any{
		"input": "hello",
	})

	if !result.Success {
		t.Fatalf("Tool failed: %s", result.Error)
	}

	expected := "Result: hello"
	if result.Content != expected {
		t.Errorf("Expected %q, got %q", expected, result.Content)
	}
}

// TestLuaToolRun_RecordsHistory covers item 1's Lua sidebar tab data source:
// every Run call — success or failure — must land in LuaRunHistory with a
// name, timing, and outcome, since the tab has nothing else real to show.
func TestLuaToolRun_RecordsHistory(t *testing.T) {
	// Start from an empty history. Without this the test reads a baseline
	// and asserts baseline+2, which silently stops being true once earlier
	// tests in this package have saturated the bounded buffer — see
	// SnapshotLuaRunHistoryForTesting.
	defer SnapshotLuaRunHistoryForTesting()()

	tmpDir := t.TempDir()
	ok := `return {schema = {name = "history-ok"}, run = function(ctx, args) return {success = true, content = "fine"} end}`
	bad := `return {schema = {name = "history-bad"}, run = function(ctx, args) error("boom") end}`
	if err := os.WriteFile(filepath.Join(tmpDir, "ok.lua"), []byte(ok), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "bad.lua"), []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLuaTools(tmpDir)
	if err != nil {
		t.Fatalf("LoadLuaTools: %v", err)
	}

	before := len(LuaRunHistory())
	for _, tl := range loaded {
		tl.Run(context.Background(), map[string]any{})
	}

	hist := LuaRunHistory()
	if len(hist) != before+2 {
		t.Fatalf("expected %d history entries, got %d", before+2, len(hist))
	}
	var sawOK, sawBad bool
	for _, r := range hist[before:] {
		if r.StartedAt.IsZero() {
			t.Errorf("run %q has zero StartedAt", r.Name)
		}
		switch r.Name {
		case "history-ok":
			sawOK = true
			if !r.Success {
				t.Errorf("expected history-ok to succeed, got Error=%q", r.Error)
			}
		case "history-bad":
			sawBad = true
			if r.Success {
				t.Errorf("expected history-bad to fail")
			}
			if r.Error == "" {
				t.Errorf("expected history-bad to carry an error message")
			}
		}
	}
	if !sawOK || !sawBad {
		t.Fatalf("missing expected history entries: sawOK=%v sawBad=%v", sawOK, sawBad)
	}
}

// TestLuaToolSchemaVisibleToModel guards against the item-9 regression: a
// loaded Lua tool's name/description/parameters must show up in the schema
// the model is actually offered (GetToolsSchema and, transitively,
// GetAllToolsSchema), not just be usable once you already know its name and
// argument shape by some other means.
func TestLuaToolSchemaVisibleToModel(t *testing.T) {
	tmpDir := t.TempDir()
	toolContent := `return {
  schema = {
    name = "schema-visible-lua",
    description = "Exercises schema visibility",
    parameters = {
      input = {type = "string", description = "Input text"}
    }
  },
  run = function(ctx, args)
    return {success = true, content = "ok"}
  end
}`
	if err := os.WriteFile(filepath.Join(tmpDir, "schema_visible.lua"), []byte(toolContent), 0644); err != nil {
		t.Fatalf("Failed to write test tool: %v", err)
	}

	tools, err := LoadLuaTools(tmpDir)
	if err != nil {
		t.Fatalf("Failed to load lua tools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 lua tool, got %d", len(tools))
	}

	const name = "schema-visible-lua"
	if _, exists := toolRegistry[name]; exists {
		t.Fatalf("test tool name %q already registered — pick a different name", name)
	}
	toolRegistry[name] = tools[0]
	defer delete(toolRegistry, name)

	for _, schema := range []struct {
		label string
		fn    func() []map[string]any
	}{
		{"GetToolsSchema", GetToolsSchema},
		{"GetAllToolsSchema", GetAllToolsSchema},
	} {
		entries := schema.fn()
		var found map[string]any
		for _, e := range entries {
			fn, ok := e["function"].(map[string]any)
			if ok && fn["name"] == name {
				found = fn
				break
			}
		}
		if found == nil {
			t.Fatalf("%s: lua tool %q not found in schema", schema.label, name)
		}
		if found["description"] != "Exercises schema visibility" {
			t.Errorf("%s: unexpected description %v", schema.label, found["description"])
		}
		params, ok := found["parameters"].(map[string]any)
		if !ok {
			t.Fatalf("%s: parameters not a map: %v", schema.label, found["parameters"])
		}
		props, ok := params["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: parameters.properties not a map: %v", schema.label, params)
		}
		if _, ok := props["input"]; !ok {
			t.Errorf("%s: expected \"input\" among properties, got %v", schema.label, props)
		}
	}
}

// TestLuaToolRunDoesNotReReadScript asserts that once a Lua tool is loaded,
// calling Run repeatedly does not re-read/re-parse the script from disk:
// the script is overwritten after load with different behavior, and Run
// must keep producing the original behavior because it's working off the
// cached compiled bytecode, not the file on disk.
func TestLuaToolRunDoesNotReReadScript(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "cached.lua")
	original := `return {
  schema = { name = "cached-lua", description = "d", parameters = {} },
  run = function(ctx, args)
    return {success = true, content = "original"}
  end
}`
	if err := os.WriteFile(scriptPath, []byte(original), 0644); err != nil {
		t.Fatalf("Failed to write test tool: %v", err)
	}

	tool, err := loadLuaTool(scriptPath)
	if err != nil {
		t.Fatalf("Failed to load lua tool: %v", err)
	}

	ctx := context.Background()
	if res := tool.Run(ctx, nil); !res.Success || res.Content != "original" {
		t.Fatalf("first run: got success=%v content=%q, want %q", res.Success, res.Content, "original")
	}

	// Overwrite the script on disk with different behavior. If Run were
	// still re-DoFile-ing on every call, the next call would observe this.
	changed := `return {
  schema = { name = "cached-lua", description = "d", parameters = {} },
  run = function(ctx, args)
    return {success = true, content = "changed"}
  end
}`
	if err := os.WriteFile(scriptPath, []byte(changed), 0644); err != nil {
		t.Fatalf("Failed to overwrite test tool: %v", err)
	}

	if res := tool.Run(ctx, nil); !res.Success || res.Content != "original" {
		t.Fatalf("second run after on-disk change: got success=%v content=%q, want unchanged %q (cache was bypassed)", res.Success, res.Content, "original")
	}
}

// TestLuaToolRunConcurrent exercises the "one LState per call, shared
// immutable proto" design under concurrent callers: gopher-lua's LState is
// not safe for concurrent use, so if Run ever went back to sharing a state
// (or a state-bound LFunction) across goroutines, this is where it would
// show up as a race or a corrupted result. Run with -race.
func TestLuaToolRunConcurrent(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "concurrent.lua")
	content := `return {
  schema = { name = "concurrent-lua", description = "d", parameters = {} },
  run = function(ctx, args)
    return {success = true, content = "Result: " .. (args.input or "none")}
  end
}`
	if err := os.WriteFile(scriptPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test tool: %v", err)
	}

	tool, err := loadLuaTool(scriptPath)
	if err != nil {
		t.Fatalf("Failed to load lua tool: %v", err)
	}

	const n = 20
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			ctx := context.Background()
			res := tool.Run(ctx, map[string]any{"input": "x"})
			if !res.Success || res.Content != "Result: x" {
				errCh <- fmt.Errorf("goroutine %d: success=%v content=%q", i, res.Success, res.Content)
				return
			}
			errCh <- nil
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Error(err)
		}
	}
}
