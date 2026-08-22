package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/internal/hooks"
)

// RunTool is the single choke point every tool call passes through — built-in
// and MCP alike — so these cover the seam rather than hook behaviour itself
// (that lives in internal/hooks/hooks_test.go).

func TestRunToolPreHookCanVetoACall(t *testing.T) {
	ResetFileStamps()
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.env")
	defer hooks.SetForTesting([]hooks.Hook{{
		Event:   hooks.EventPreTool,
		Tools:   []string{"write"},
		Name:    "protect-env",
		Command: `case "$TYCI_TOOL_PATH" in *.env) echo "refusing: .env is off limits"; exit 1;; esac`,
	}})()

	res := RunTool(context.Background(), "write", map[string]any{"path": path, "content": "TOKEN=1"})
	if res.Success {
		t.Fatal("the hook vetoed this call; it should not have run")
	}
	if !strings.Contains(res.Error, "off limits") {
		t.Fatalf("the hook's reason must reach the model: %q", res.Error)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the file was created despite the veto")
	}
}

func TestRunToolPreHookLetsOtherPathsThrough(t *testing.T) {
	ResetFileStamps()
	path := filepath.Join(t.TempDir(), "ok.txt")
	defer hooks.SetForTesting([]hooks.Hook{{
		Event:   hooks.EventPreTool,
		Tools:   []string{"write"},
		Command: `case "$TYCI_TOOL_PATH" in *.env) exit 1;; esac`,
	}})()

	res := RunTool(context.Background(), "write", map[string]any{"path": path, "content": "fine"})
	if !res.Success {
		t.Fatalf("unrelated path should pass: %s", res.Error)
	}
}

// TestRunToolPostHookAnnotatesResult is the feedback loop: the check runs on
// the file that was just written and its output lands in the same tool result.
func TestRunToolPostHookAnnotatesResult(t *testing.T) {
	ResetFileStamps()
	path := filepath.Join(t.TempDir(), "x.txt")
	defer hooks.SetForTesting([]hooks.Hook{{
		Event:   hooks.EventPostTool,
		Tools:   []string{"write"},
		Name:    "line-count",
		Command: `wc -l < "$TYCI_TOOL_PATH" | tr -d " "`,
	}})()

	res := RunTool(context.Background(), "write", map[string]any{"path": path, "content": "a\nb\nc\n"})
	if !res.Success {
		t.Fatalf("write failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "line-count") || !strings.Contains(res.Content, "3") {
		t.Fatalf("hook output should be appended to the result: %q", res.Content)
	}
	if !strings.Contains(res.Content, "written") {
		t.Fatalf("the tool's own result must survive: %q", res.Content)
	}
}

// TestRunToolBlockingPostHookFailsTheCall: opt-in gating turns a successful
// write into an unfinished step, and the original result has to stay readable
// so the model knows the write itself happened.
func TestRunToolBlockingPostHookFailsTheCall(t *testing.T) {
	ResetFileStamps()
	path := filepath.Join(t.TempDir(), "x.txt")
	defer hooks.SetForTesting([]hooks.Hook{{
		Event: hooks.EventPostTool, Tools: []string{"write"}, Blocking: true,
		Name: "reject-tabs", Command: `grep -q "	" "$TYCI_TOOL_PATH" && { echo "tabs found"; exit 1; }; exit 0`,
	}})()

	res := RunTool(context.Background(), "write", map[string]any{"path": path, "content": "\tindented\n"})
	if res.Success {
		t.Fatal("a blocking hook should have failed this call")
	}
	if !strings.Contains(res.Error, "tabs found") {
		t.Fatalf("hook reason missing: %q", res.Error)
	}
	if !strings.Contains(res.Error, "written") {
		t.Fatalf("the model must be able to see the write itself succeeded: %q", res.Error)
	}
	// The write really did happen — that is exactly why the message says so.
	if data, _ := os.ReadFile(path); string(data) != "\tindented\n" {
		t.Fatalf("expected the write to have taken effect, got %q", string(data))
	}
}

func TestRunToolWithNoHooksIsUnchanged(t *testing.T) {
	ResetFileStamps()
	defer hooks.SetForTesting(nil)()
	path := filepath.Join(t.TempDir(), "x.txt")

	res := RunTool(context.Background(), "write", map[string]any{"path": path, "content": "hello"})
	if !res.Success || res.Content != "written "+path {
		t.Fatalf("result should be untouched with no hooks configured: %+v", res)
	}
}

// TestRunToolHooksSeeUnknownTools: hooks wrap the dispatcher, not the
// registry, so an audit hook logs every call the model makes — including ones
// that turn out not to exist.
func TestRunToolHooksSeeUnknownTools(t *testing.T) {
	log := filepath.Join(t.TempDir(), "audit.log")
	defer hooks.SetForTesting([]hooks.Hook{{
		Event: hooks.EventPreTool, Command: `echo "$TYCI_TOOL" >> ` + log,
	}})()

	RunTool(context.Background(), "no_such_tool", nil)

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("audit hook did not run: %v", err)
	}
	if strings.TrimSpace(string(data)) != "no_such_tool" {
		t.Fatalf("got %q", string(data))
	}
}
