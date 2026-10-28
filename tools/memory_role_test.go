package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/instructions"
)

// roleMemoryHome points HOME at a temp dir, so the role notes never reach the
// real ~/.tyci. os.UserHomeDir reads $HOME on the platforms tyci supports.
func roleMemoryHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func runMemoryAs(t *testing.T, ctx context.Context, input map[string]any) ToolResult {
	t.Helper()
	return (&MemoryTool{}).Run(ctx, input)
}

func TestMemoryRoleScopeWithoutRoleIsValidationError(t *testing.T) {
	roleMemoryHome(t)
	res := runMemoryAs(t, context.Background(), map[string]any{
		"scope": "role", "action": "write", "content": "a note",
	})
	if res.Success || !res.validationError {
		t.Fatalf("want a validation error, got %+v", res)
	}
	if !strings.Contains(res.Error, "role") {
		t.Errorf("the error should say a role is needed: %q", res.Error)
	}
}

func TestMemoryRoleScopeWriteThenRead(t *testing.T) {
	roleMemoryHome(t)
	ctx := WithRoleContext(context.Background(), "tyci-agent", "worker")

	if res := runMemoryAs(t, ctx, map[string]any{"scope": "role", "action": "read"}); !res.Success || !strings.Contains(res.Content, "no role note yet") {
		t.Fatalf("read before any write: %+v", res)
	}

	res := runMemoryAs(t, ctx, map[string]any{
		"scope": "role", "action": "write", "content": "run make check; go test skips the golden files",
	})
	if !res.Success {
		t.Fatalf("write failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "next session") {
		t.Errorf("the reply should say the note is loaded next session: %q", res.Content)
	}

	res = runMemoryAs(t, ctx, map[string]any{"scope": "role", "action": "read"})
	if !res.Success || res.Content != "run make check; go test skips the golden files" {
		t.Fatalf("read after write: %+v", res)
	}
}

// TestMemoryRoleScopeLeavesProjectNotesAlone: a role note is not a project
// note, in either direction.
func TestMemoryRoleScopeLeavesProjectNotesAlone(t *testing.T) {
	roleMemoryHome(t)
	dir := memoryProject(t)
	ctx := WithRoleContext(context.Background(), "tyci-agent", "worker")

	if res := runMemoryAs(t, ctx, map[string]any{"scope": "role", "action": "write", "content": "role only"}); !res.Success {
		t.Fatalf("write failed: %s", res.Error)
	}
	if names, _ := instructions.List(dir); len(names) != 0 {
		t.Fatalf("a role write created project notes: %v", names)
	}
}

// TestMemoryRoleScopeTwoRolesSeeOnlyTheirOwnNote: the note is picked by the
// role in the context, so a role cannot read or overwrite another role's note.
func TestMemoryRoleScopeTwoRolesSeeOnlyTheirOwnNote(t *testing.T) {
	roleMemoryHome(t)
	worker := WithRoleContext(context.Background(), "tyci-agent", "worker")
	review := WithRoleContext(context.Background(), "tyci-agent", "review")

	runMemoryAs(t, worker, map[string]any{"scope": "role", "action": "write", "content": "worker secret"})
	runMemoryAs(t, review, map[string]any{"scope": "role", "action": "write", "content": "review note"})

	if res := runMemoryAs(t, review, map[string]any{"scope": "role", "action": "read"}); res.Content != "review note" {
		t.Errorf("review reads %q", res.Content)
	}
	if res := runMemoryAs(t, worker, map[string]any{"scope": "role", "action": "read"}); res.Content != "worker secret" {
		t.Errorf("worker reads %q", res.Content)
	}
	// A name argument is refused, so there is no way to ask for another file.
	res := runMemoryAs(t, review, map[string]any{"scope": "role", "action": "read", "name": "../worker"})
	if res.Success || !res.validationError {
		t.Errorf("a name in role scope must be refused: %+v", res)
	}
}

func TestMemoryRoleScopeRejectsUnsupportedActionsAndNames(t *testing.T) {
	roleMemoryHome(t)
	ctx := WithRoleContext(context.Background(), "tyci-agent", "worker")

	cases := []map[string]any{
		{"scope": "role"},
		{"scope": "role", "action": "list"},
		{"scope": "role", "action": "delete"},
		{"scope": "role", "action": "read", "name": "other"},
		{"scope": "role", "action": "write", "name": "other", "content": "x"},
		{"scope": "role", "action": "write", "content": "   "},
	}
	for _, in := range cases {
		res := runMemoryAs(t, ctx, in)
		if res.Success || !res.validationError {
			t.Errorf("input %v: want a validation error, got %+v", in, res)
		}
	}
}

func TestMemoryUnknownScopeIsValidationError(t *testing.T) {
	res := runMemory(t, map[string]any{"scope": "team", "action": "list"})
	if res.Success || !res.validationError {
		t.Fatalf("want a validation error, got %+v", res)
	}
}

// TestMemoryProjectScopeUnchanged: scope "project" and no scope at all write
// to the project notes exactly as before.
func TestMemoryProjectScopeUnchanged(t *testing.T) {
	roleMemoryHome(t)
	dir := memoryProject(t)
	ctx := WithRoleContext(context.Background(), "tyci-agent", "worker")

	for _, scope := range []any{nil, "project"} {
		input := map[string]any{"action": "write", "name": "test-command", "content": "make check"}
		if scope != nil {
			input["scope"] = scope
		}
		if res := runMemoryAs(t, ctx, input); !res.Success {
			t.Fatalf("scope %v: %s", scope, res.Error)
		}
	}
	names, err := instructions.List(dir)
	if err != nil || len(names) != 1 || names[0] != "test-command" {
		t.Fatalf("project notes = %v, %v", names, err)
	}
}
