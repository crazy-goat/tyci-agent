package tools

import (
	"context"
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
