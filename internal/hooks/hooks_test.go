package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPreAllowsWhenHookSucceeds(t *testing.T) {
	defer SetForTesting([]Hook{{Event: EventPreTool, Command: "exit 0"}})()

	blocked, msg := RunPre(context.Background(), "write", map[string]any{"path": "x.go"})
	if blocked {
		t.Fatalf("a zero-exit hook must not block: %s", msg)
	}
}

// TestRunPreBlocksAndExplains is the protected-path case: the hook's own
// words have to reach the model, otherwise it retries the same call.
func TestRunPreBlocksAndExplains(t *testing.T) {
	defer SetForTesting([]Hook{{
		Event:   EventPreTool,
		Tools:   []string{"write"},
		Name:    "protect-secrets",
		Command: `echo "never edit .env"; exit 1`,
	}})()

	blocked, msg := RunPre(context.Background(), "write", map[string]any{"path": ".env"})
	if !blocked {
		t.Fatal("non-zero exit must block the call")
	}
	if !strings.Contains(msg, "never edit .env") {
		t.Fatalf("hook output missing from the message: %q", msg)
	}
	if !strings.Contains(msg, "protect-secrets") {
		t.Fatalf("message should name the hook so the model knows what spoke: %q", msg)
	}
}

func TestHooksOnlyRunForMatchingTools(t *testing.T) {
	defer SetForTesting([]Hook{{
		Event: EventPreTool, Tools: []string{"write"}, Command: "exit 1",
	}})()

	if blocked, _ := RunPre(context.Background(), "read", nil); blocked {
		t.Fatal("a write-only hook blocked a read")
	}
	if blocked, _ := RunPre(context.Background(), "write", nil); !blocked {
		t.Fatal("a write-only hook did not run for write")
	}
}

func TestHookWithNoToolsMatchesEverything(t *testing.T) {
	defer SetForTesting([]Hook{{Event: EventPreTool, Command: "exit 1"}})()

	for _, tool := range []string{"read", "write", "bash", "some_mcp_tool"} {
		if blocked, _ := RunPre(context.Background(), tool, nil); !blocked {
			t.Fatalf("unrestricted hook did not run for %q", tool)
		}
	}
}

// TestRunPreStopsAtFirstVeto: once the call is not happening, later hooks
// would only produce advice about work that will not be done.
func TestRunPreStopsAtFirstVeto(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "second-ran")
	defer SetForTesting([]Hook{
		{Event: EventPreTool, Command: "exit 1"},
		{Event: EventPreTool, Command: "touch " + marker},
	})()

	if blocked, _ := RunPre(context.Background(), "write", nil); !blocked {
		t.Fatal("expected a block")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hooks kept running after a veto")
	}
}

// TestHookReceivesPayloadOnStdin covers the structured channel: anything
// beyond the convenience env vars has to come from the JSON.
func TestHookReceivesPayloadOnStdin(t *testing.T) {
	out := filepath.Join(t.TempDir(), "payload.json")
	defer SetForTesting([]Hook{{Event: EventPreTool, Command: "cat > " + out}})()

	args := map[string]any{"path": "main.go", "oldString": "a", "newString": "b"}
	if blocked, msg := RunPre(context.Background(), "write", args); blocked {
		t.Fatalf("unexpected block: %s", msg)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("hook got no stdin: %v", err)
	}
	var got payload
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("stdin was not valid JSON: %v (%q)", err, string(data))
	}
	if got.Tool != "write" || got.Event != EventPreTool {
		t.Fatalf("wrong envelope: %+v", got)
	}
	if got.Args["oldString"] != "a" {
		t.Fatalf("args did not survive the round trip: %+v", got.Args)
	}
}

// TestHookEnvExposesPath is what makes one-line hooks usable: no JSON
// parsing needed for the overwhelmingly common "act on the file" case.
func TestHookEnvExposesPath(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env.txt")
	defer SetForTesting([]Hook{{
		Event:   EventPostTool,
		Command: `printf '%s|%s|%s' "$TYCI_TOOL" "$TYCI_TOOL_PATH" "$TYCI_TOOL_SUCCESS" > ` + out,
	}})()

	RunPost(context.Background(), "write", map[string]any{"path": "cmd/main.go"}, true, "written", "")

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "write|cmd/main.go|true" {
		t.Fatalf("got %q", string(data))
	}
}

// TestRunPostQuietHookAddsNothing is the common path and matters for context
// budget: a formatter that found nothing must not append a line to every
// single tool result.
func TestRunPostQuietHookAddsNothing(t *testing.T) {
	defer SetForTesting([]Hook{{Event: EventPostTool, Command: "exit 0"}})()

	note, fail := RunPost(context.Background(), "write", nil, true, "written", "")
	if note != "" || fail {
		t.Fatalf("a silent passing hook should be invisible, got note=%q fail=%v", note, fail)
	}
}

// TestRunPostAppendsFeedback is the point of the whole post event: the linter
// speaks in the same turn the change was made.
func TestRunPostAppendsFeedback(t *testing.T) {
	defer SetForTesting([]Hook{{
		Event: EventPostTool, Name: "gofmt", Command: `echo "main.go needs formatting"`,
	}})()

	note, fail := RunPost(context.Background(), "write", nil, true, "written", "")
	if !strings.Contains(note, "main.go needs formatting") {
		t.Fatalf("hook output missing: %q", note)
	}
	if !strings.Contains(note, "gofmt") {
		t.Fatalf("note should name the hook: %q", note)
	}
	if fail {
		t.Fatal("a non-blocking hook must not fail the tool call")
	}
}

// TestRunPostBlockingFailsTheCall: opt-in gating, and the message has to stop
// the model from redoing a write that already succeeded.
func TestRunPostBlockingFailsTheCall(t *testing.T) {
	defer SetForTesting([]Hook{{
		Event: EventPostTool, Name: "vet", Blocking: true,
		Command: `echo "undefined: foo"; exit 1`,
	}})()

	note, fail := RunPost(context.Background(), "write", nil, true, "written", "")
	if !fail {
		t.Fatal("a blocking hook exiting non-zero must fail the call")
	}
	if !strings.Contains(note, "undefined: foo") {
		t.Fatalf("hook output missing: %q", note)
	}
	if !strings.Contains(note, "completed") {
		t.Fatalf("note must say the tool itself ran, or the model will repeat it: %q", note)
	}
}

// TestRunPostRunsEveryHook: unlike pre, these report on one finished action,
// so a formatter's complaint must not hide a linter's.
func TestRunPostRunsEveryHook(t *testing.T) {
	defer SetForTesting([]Hook{
		{Event: EventPostTool, Command: `echo first; exit 1`},
		{Event: EventPostTool, Command: `echo second`},
	})()

	note, _ := RunPost(context.Background(), "write", nil, true, "", "")
	if !strings.Contains(note, "first") || !strings.Contains(note, "second") {
		t.Fatalf("both hooks should have spoken: %q", note)
	}
}

func TestHookTimeoutIsReportedNotHung(t *testing.T) {
	defer SetForTesting([]Hook{{Event: EventPreTool, Timeout: 1, Command: "sleep 30"}})()

	blocked, msg := RunPre(context.Background(), "write", nil)
	if !blocked {
		t.Fatal("a hook that never finishes cannot be treated as passing")
	}
	if !strings.Contains(msg, "timed out") {
		t.Fatalf("message should say it timed out: %q", msg)
	}
}

func TestHookOutputIsCapped(t *testing.T) {
	defer SetForTesting([]Hook{{
		Event: EventPostTool, Command: `yes lots-of-output | head -20000`,
	}})()

	note, _ := RunPost(context.Background(), "bash", nil, true, "", "")
	if len(note) > maxHookOutput+512 {
		t.Fatalf("note is %d bytes; the cap is meant to protect the context window", len(note))
	}
	if !strings.Contains(note, "truncated") {
		t.Fatalf("truncation must be visible, not silent: %q", note[max(0, len(note)-200):])
	}
}

// ---------------------------------------------------------------------------
// Config loading
// ---------------------------------------------------------------------------

func writeHookConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReadsHooksInOrder(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	a := writeHookConfig(t, dirA, `{"hooks":[{"event":"pre_tool","command":"a","name":"global"}]}`)
	b := writeHookConfig(t, dirB, `{"hooks":[{"event":"post_tool","command":"b","name":"project"}]}`)
	defer SetForTesting(nil)()

	if errs := Load(a, b); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !Any(EventPreTool) || !Any(EventPostTool) {
		t.Fatal("hooks from both files should be loaded")
	}
	pre := matching(EventPreTool, "write", "x.go")
	if len(pre) != 1 || pre[0].Name != "global" {
		t.Fatalf("got %+v", pre)
	}
}

func TestLoadIgnoresMissingFileButReportsBadJSON(t *testing.T) {
	dir := t.TempDir()
	defer SetForTesting(nil)()

	if errs := Load(filepath.Join(dir, "absent.json")); len(errs) != 0 {
		t.Fatalf("a missing config is not an error (hooks are opt-in): %v", errs)
	}

	bad := writeHookConfig(t, dir, `{"hooks":[`)
	errs := Load(bad)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "not valid JSON") {
		t.Fatalf("a broken config must be reported, got %v", errs)
	}
}

// TestLoadRejectsUnusableEntries: an entry with a typo'd event would never
// fire, and a hook that silently never fires is worse than no hook — the user
// believes a protection is in place.
func TestLoadRejectsUnusableEntries(t *testing.T) {
	dir := t.TempDir()
	defer SetForTesting(nil)()

	path := writeHookConfig(t, dir, `{"hooks":[
		{"event":"preTool","command":"x"},
		{"event":"pre_tool"},
		{"event":"pre_tool","command":"ok"}
	]}`)
	errs := Load(path)
	if len(errs) != 2 {
		t.Fatalf("expected the typo'd event and the missing command to be reported, got %v", errs)
	}
	if got := len(matching(EventPreTool, "write", "x.go")); got != 1 {
		t.Fatalf("only the valid entry should be active, got %d", got)
	}
}

func TestAnyIsFalseWithNoHooks(t *testing.T) {
	defer SetForTesting(nil)()
	if Any(EventPreTool) || Any(EventPostTool) {
		t.Fatal("no hooks configured, but Any reported some")
	}
}

func TestDefaultPathsCoverGlobalThenProject(t *testing.T) {
	paths := DefaultPaths("/repo")
	if len(paths) != 2 {
		t.Fatalf("got %v", paths)
	}
	if !strings.HasSuffix(paths[1], filepath.Join("/repo", ".tyci", "hooks.json")) {
		t.Fatalf("project path wrong: %v", paths)
	}
	if !strings.Contains(paths[0], filepath.Join(".tyci", "hooks.json")) {
		t.Fatalf("global path wrong: %v", paths)
	}
}

// ---------------------------------------------------------------------------
// Path filtering
// ---------------------------------------------------------------------------

// TestHooksOnlyRunForMatchingPaths is the whole point of the Paths field: in a
// mixed repository, a Go formatter and a PHP linter have to coexist in one
// config without either running on the other's files.
func TestHooksOnlyRunForMatchingPaths(t *testing.T) {
	defer SetForTesting([]Hook{{
		Event:   EventPreTool,
		Tools:   []string{"write"},
		Paths:   []string{"**/*.go"},
		Name:    "go-only",
		Command: `echo "this is a Go file"; exit 1`,
	}})()

	if blocked, _ := RunPre(context.Background(), "write", map[string]any{"path": "display/tui.go"}); !blocked {
		t.Fatal("a .go path must reach a **/*.go hook")
	}
	if blocked, msg := RunPre(context.Background(), "write", map[string]any{"path": "src/App.php"}); blocked {
		t.Fatalf("a .php path must not reach a **/*.go hook: %s", msg)
	}
}

// TestPathFilterMatchesFilesInTheRoot: "**/*.go" has to cover "main.go" as
// well as "a/b/main.go", or every config would need both patterns.
func TestPathFilterMatchesFilesInTheRoot(t *testing.T) {
	h := Hook{Paths: []string{"**/*.go"}}
	for _, p := range []string{"main.go", "a/b/main.go", "./main.go", "/abs/path/main.go"} {
		if !h.matchesPath(p) {
			t.Errorf("%q should match **/*.go", p)
		}
	}
	if h.matchesPath("main.php") {
		t.Error("main.php should not match **/*.go")
	}
}

// TestPathFilterSkipsCallsWithoutAPath: a hook restricted to paths is about
// files, so a bash command — which has no path at all — is none of its
// business. The alternative (matching everything) would run a formatter on
// every shell command.
func TestPathFilterSkipsCallsWithoutAPath(t *testing.T) {
	defer SetForTesting([]Hook{{
		Event:   EventPreTool,
		Paths:   []string{"**/*.go"},
		Command: "exit 1",
	}})()

	if blocked, msg := RunPre(context.Background(), "bash", map[string]any{"command": "ls"}); blocked {
		t.Fatalf("a path-restricted hook must not fire on a pathless call: %s", msg)
	}
}

// TestNoPathFilterStillMatchesEverything keeps the pre-existing behaviour of
// every config written before this field existed.
func TestNoPathFilterStillMatchesEverything(t *testing.T) {
	h := Hook{}
	if !h.matchesPath("anything.txt") || !h.matchesPath("") {
		t.Fatal("an empty Paths filter must match every call, with or without a path")
	}
}

func TestPathFilterAcceptsSeveralPatterns(t *testing.T) {
	h := Hook{Paths: []string{"**/*.go", "**/*.php"}}
	if !h.matchesPath("a/x.go") || !h.matchesPath("a/x.php") {
		t.Fatal("both patterns should match")
	}
	if h.matchesPath("a/x.py") {
		t.Fatal("an unlisted extension should not match")
	}
}

// TestLoadRejectsBadPathPattern: a glob typo must be reported. A hook that
// silently matches nothing is the failure this package exists to avoid — the
// user believes a check is running when it is not.
func TestLoadRejectsBadPathPattern(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	cfg := `{"hooks":[
	  {"event":"post_tool","paths":["[unclosed"],"command":"true"},
	  {"event":"post_tool","paths":["**/*.go"],"command":"gofmt -l \"$TYCI_TOOL_PATH\""}
	]}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	errs := Load(path)
	if len(errs) != 1 {
		t.Fatalf("expected exactly one error, got %v", errs)
	}
	if !strings.Contains(errs[0].Error(), "invalid path pattern") {
		t.Fatalf("error should name the problem: %v", errs[0])
	}
	// The valid entry must survive: one bad hook does not disable the rest.
	if !Any(EventPostTool) {
		t.Fatal("the well-formed hook should still be loaded")
	}
}
