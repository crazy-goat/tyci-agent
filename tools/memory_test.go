package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/internal/instructions"
)

// memoryProject moves the test into an empty project so the tool's notes land
// in a temp directory and not in the repository this test runs from.
func memoryProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func runMemory(t *testing.T, input map[string]any) ToolResult {
	t.Helper()
	return (&MemoryTool{}).Run(context.Background(), input)
}

func TestMemoryWriteThenListAndRead(t *testing.T) {
	dir := memoryProject(t)

	res := runMemory(t, map[string]any{
		"action":  "write",
		"name":    "Test Command",
		"content": "make check runs the whole suite; go test ./... misses the golden files",
	})
	if !res.Success {
		t.Fatalf("write failed: %s", res.Error)
	}
	// The note cannot appear in this session's prompt, and the model has to be
	// told that or it will conclude the write did not work.
	if !strings.Contains(res.Content, "next time") {
		t.Errorf("the result should explain when the note takes effect: %q", res.Content)
	}

	onDisk := filepath.Join(instructions.MemoryDir(dir), "test-command.md")
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("note not written to %s: %v", onDisk, err)
	}

	list := runMemory(t, map[string]any{"action": "list"})
	if !list.Success || !strings.Contains(list.Content, "test-command") {
		t.Fatalf("list did not show the note: %+v", list)
	}

	read := runMemory(t, map[string]any{"action": "read", "name": "test-command"})
	if !read.Success || !strings.Contains(read.Content, "make check") {
		t.Fatalf("read did not return the note: %+v", read)
	}
}

// TestMemoryListWithNoNotesExplainsWhatToWrite: the empty case is the one the
// model sees first, so it is where the guidance belongs.
func TestMemoryListWithNoNotesExplainsWhatToWrite(t *testing.T) {
	memoryProject(t)

	res := runMemory(t, map[string]any{"action": "list"})
	if !res.Success {
		t.Fatalf("listing an empty project should succeed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "no notes yet") || !strings.Contains(res.Content, "write") {
		t.Errorf("got %q", res.Content)
	}
}

// TestMemoryDefaultsToList so that a call with no action is useful rather than
// an error.
func TestMemoryDefaultsToList(t *testing.T) {
	memoryProject(t)
	if res := runMemory(t, map[string]any{}); !res.Success {
		t.Fatalf("a bare call should list: %s", res.Error)
	}
}

func TestMemoryDelete(t *testing.T) {
	memoryProject(t)
	runMemory(t, map[string]any{"action": "write", "name": "gone", "content": "x"})

	if res := runMemory(t, map[string]any{"action": "delete", "name": "gone"}); !res.Success {
		t.Fatalf("delete failed: %s", res.Error)
	}
	if res := runMemory(t, map[string]any{"action": "delete", "name": "gone"}); res.Success {
		t.Fatal("deleting a note twice should fail, not silently succeed")
	}
}

func TestMemoryRejectsBadInput(t *testing.T) {
	memoryProject(t)

	cases := []struct {
		name  string
		input map[string]any
	}{
		{"unknown action", map[string]any{"action": "forget"}},
		{"write without a name", map[string]any{"action": "write", "content": "x"}},
		{"write without content", map[string]any{"action": "write", "name": "x"}},
		{"read without a name", map[string]any{"action": "read"}},
		{"read a missing note", map[string]any{"action": "read", "name": "nope"}},
		{"delete without a name", map[string]any{"action": "delete"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if res := runMemory(t, tc.input); res.Success {
				t.Fatalf("expected failure, got %q", res.Content)
			}
		})
	}
}

// TestMemoryIsInTheToolSchema: a tool the model is never told about is a tool
// that is never used.
func TestMemoryIsInTheToolSchema(t *testing.T) {
	if _, ok := toolRegistry["memory"]; !ok {
		t.Fatal("memory is not registered")
	}

	var schema []struct {
		Function struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"function"`
	}
	if err := json.Unmarshal(GetToolsSchemaJSON(), &schema); err != nil {
		t.Fatal(err)
	}
	for _, entry := range schema {
		if entry.Function.Name == "memory" {
			if !strings.Contains(entry.Function.Description, ".tyci/memory") {
				t.Error("the description should say where notes live")
			}
			return
		}
	}
	t.Fatal("memory is missing from the tool schema")
}

func TestMemoryDeleteWithoutNameIncludesHelpHint(t *testing.T) {
	memoryProject(t)
	res := RunTool(context.Background(), "memory", map[string]any{"action": "delete"})
	if res.Success {
		t.Fatal("expected missing name to fail")
	}
	if !strings.Contains(res.Error, `help(tool="memory")`) {
		t.Fatalf("expected memory help hint, got %q", res.Error)
	}
}

func TestMemoryValidationErrorsFromInstructionsIncludeHelpHint(t *testing.T) {
	memoryProject(t)

	cases := []map[string]any{
		{"action": "read", "name": "!!!"},
		{"action": "write", "name": "valid", "content": "   "},
		{"action": "delete", "name": "!!!"},
	}
	for _, input := range cases {
		res := RunTool(context.Background(), "memory", input)
		if res.Success {
			t.Fatalf("expected validation failure for %+v", input)
		}
		if !strings.Contains(res.Error, `help(tool="memory")`) {
			t.Fatalf("expected memory help hint for %+v, got %q", input, res.Error)
		}
	}

	missing := RunTool(context.Background(), "memory", map[string]any{"action": "read", "name": "missing"})
	if missing.Success || strings.Contains(missing.Error, "help(") {
		t.Fatalf("missing note is runtime failure and must not get hint: %+v", missing)
	}
}
