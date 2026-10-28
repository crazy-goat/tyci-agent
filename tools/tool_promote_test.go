package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeOracle replaces promoteOracle. It answers with answer and err, and it
// records each call. Tests that use it must not run in parallel.
type fakeOracle struct {
	answer string
	err    error
	calls  int
	task   string
	dir    string
}

func useFakeOracle(t *testing.T, answer string, err error) *fakeOracle {
	t.Helper()
	f := &fakeOracle{answer: answer, err: err}
	old := promoteOracle
	promoteOracle = func(_ context.Context, task, dir string) (string, error) {
		f.calls++
		f.task = task
		f.dir = dir
		return f.answer, f.err
	}
	t.Cleanup(func() { promoteOracle = old })
	return f
}

// gitIn runs git in dir with a fixed identity and no signing.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.email=t@t.test", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// linkedWorktree makes a git repository with one commit and a linked
// worktree of it. It returns the real paths of the main checkout and of the
// worktree.
func linkedWorktree(t *testing.T) (mainDir, wt string) {
	t.Helper()
	mainDir = t.TempDir()
	gitIn(t, mainDir, "init", "-q")
	gitIn(t, mainDir, "commit", "-q", "--allow-empty", "-m", "init")
	wt = filepath.Join(t.TempDir(), "wt")
	gitIn(t, mainDir, "worktree", "add", "-q", wt, "-b", "promote-test")
	realMain, err := filepath.EvalSymlinks(mainDir)
	if err != nil {
		t.Fatal(err)
	}
	realWT, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	return realMain, realWT
}

// promoteCtx is the context of a main chat call in the run folder dir, working in wt.
func promoteCtx(dir, wt string) context.Context {
	return WithWorkdir(WithRunDir(context.Background(), dir), wt)
}

// promoteTarget is the file that tool_promote writes for name in the worktree wt.
func promoteTarget(wt, name string) string {
	return filepath.Join(wt, ".tyci", "tools", name+".lua")
}

// promoteInput is the input of a tool_promote call.
func promoteInput(name string) map[string]any {
	return map[string]any{"name": name}
}

func TestToolPromoteSafeCopiesToWorktree(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })
	_, wt := linkedWorktree(t)
	if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}
	oracle := useFakeOracle(t, "  SAFE \r\n", nil)

	res := RunTool(promoteCtx(dir, wt), "tool_promote", promoteInput("made_echo"))
	if !res.Success {
		t.Fatalf("a SAFE tool was not promoted: %s", res.Error)
	}
	got, err := os.ReadFile(promoteTarget(wt, "made_echo"))
	if err != nil {
		t.Fatalf("the tool was not copied to the worktree: %v", err)
	}
	if string(got) != createSource {
		t.Fatalf("the copy differs from the run folder script:\n%s", got)
	}
	if oracle.calls != 1 {
		t.Fatalf("the oracle was called %d times, want 1", oracle.calls)
	}
	if !strings.Contains(oracle.task, createSource) {
		t.Fatal("the task text given to the oracle does not contain the source")
	}
	if oracle.dir != wt {
		t.Fatalf("the oracle works in %q, want the worktree %q", oracle.dir, wt)
	}
}

func TestToolPromoteRefusesWithoutSafe(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		err    error
		reason string
	}{
		{name: "doubt", answer: "DOUBT\nit reads a file", reason: "the oracle did not answer SAFE: DOUBT it reads a file"},
		{name: "unsafe", answer: "UNSAFE\nit calls the network", reason: "the oracle did not answer SAFE: UNSAFE it calls the network"},
		{name: "malformed", answer: "Safe, I think", reason: "the oracle did not answer SAFE: Safe, I think"},
		{name: "safe with a reason", answer: "SAFE\nbut UNSAFE because it reads a file", reason: "the oracle did not answer SAFE: SAFE but UNSAFE because it reads a file"},
		{name: "safe with a period", answer: "SAFE.", reason: "the oracle did not answer SAFE: SAFE."},
		{name: "lower case", answer: "safe", reason: "the oracle did not answer SAFE: safe"},
		{name: "empty", answer: " \n ", reason: "the oracle gave no answer"},
		{name: "oracle error", err: os.ErrPermission, reason: "the oracle failed: " + os.ErrPermission.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer SnapshotLuaToolsForTesting()()
			dir := t.TempDir()
			t.Cleanup(func() { RemoveRunTools(dir) })
			_, wt := linkedWorktree(t)
			if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
				t.Fatalf("tool_create failed: %s", res.Error)
			}
			oracle := useFakeOracle(t, tc.answer, tc.err)

			res := RunTool(promoteCtx(dir, wt), "tool_promote", promoteInput("made_echo"))
			want := "not promoted: " + tc.reason + "; ask the user"
			if res.Success || res.Error != want {
				t.Fatalf("the result is success=%v error=%q, want a failure with error %q", res.Success, res.Error, want)
			}
			if _, err := os.Stat(promoteTarget(wt, "made_echo")); !os.IsNotExist(err) {
				t.Fatalf("a refused tool was copied to the worktree: %v", err)
			}
			if _, err := os.Stat(filepath.Join(wt, ".tyci")); !os.IsNotExist(err) {
				t.Fatalf("a refused review left a .tyci folder in the worktree: %v", err)
			}
			if oracle.calls != 1 {
				t.Fatalf("the oracle was called %d times, want 1", oracle.calls)
			}
		})
	}
}

func TestToolPromoteUnknownNameIsError(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	_, wt := linkedWorktree(t)
	oracle := useFakeOracle(t, "SAFE", nil)

	res := RunTool(promoteCtx(dir, wt), "tool_promote", promoteInput("no_such_tool"))
	if res.Success || !strings.Contains(res.Error, "no Lua tool named \"no_such_tool\"") {
		t.Fatalf("an unknown tool was not an error: success=%v error=%q", res.Success, res.Error)
	}
	for _, bad := range []string{"../escape", "mcp_github_x", "1starts_with_digit"} {
		res = RunTool(promoteCtx(dir, wt), "tool_promote", promoteInput(bad))
		if res.Success || !strings.Contains(res.Error, "is not valid") {
			t.Fatalf("the bad name %q was not refused as invalid: success=%v error=%q", bad, res.Success, res.Error)
		}
	}
	if oracle.calls != 0 {
		t.Fatalf("the oracle was called %d times for an unknown tool, want 0", oracle.calls)
	}
}

func TestToolPromoteNeedsLinkedWorktree(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })
	if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}

	t.Run("not a git repository", func(t *testing.T) {
		oracle := useFakeOracle(t, "SAFE", nil)
		plain := t.TempDir()
		res := RunTool(promoteCtx(dir, plain), "tool_promote", promoteInput("made_echo"))
		if res.Success || !strings.Contains(res.Error, "needs a git worktree") {
			t.Fatalf("a folder outside git was not refused: success=%v error=%q", res.Success, res.Error)
		}
		if oracle.calls != 0 {
			t.Fatalf("the oracle was called %d times for a refused folder, want 0", oracle.calls)
		}
	})

	t.Run("main checkout", func(t *testing.T) {
		oracle := useFakeOracle(t, "SAFE", nil)
		mainDir, _ := linkedWorktree(t)
		res := RunTool(promoteCtx(dir, mainDir), "tool_promote", promoteInput("made_echo"))
		if res.Success || !strings.Contains(res.Error, "does not write into the main checkout") {
			t.Fatalf("the main checkout was not refused: success=%v error=%q", res.Success, res.Error)
		}
		if _, err := os.Stat(promoteTarget(mainDir, "made_echo")); !os.IsNotExist(err) {
			t.Fatalf("a tool was copied into the main checkout: %v", err)
		}
		if oracle.calls != 0 {
			t.Fatalf("the oracle was called %d times for the main checkout, want 0", oracle.calls)
		}
	})
}

func TestToolPromoteRefusesExistingTarget(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })
	_, wt := linkedWorktree(t)
	if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}
	target := promoteTarget(wt, "made_echo")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("-- kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oracle := useFakeOracle(t, "SAFE", nil)

	res := RunTool(promoteCtx(dir, wt), "tool_promote", promoteInput("made_echo"))
	if res.Success || !strings.Contains(res.Error, "already exists") {
		t.Fatalf("an existing target was not refused: success=%v error=%q", res.Success, res.Error)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "-- kept\n" {
		t.Fatalf("the existing target was overwritten: %q", got)
	}
	if oracle.calls != 0 {
		t.Fatalf("the oracle was called %d times for an existing target, want 0", oracle.calls)
	}
}

func TestToolPromoteRefusedForUnrestrictedSubagent(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })
	_, wt := linkedWorktree(t)
	if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}
	oracle := useFakeOracle(t, "SAFE", nil)

	ctx := WithWorkdir(subagentCtx(dir, nil, nil), wt)
	res := RunTool(ctx, "tool_promote", promoteInput("made_echo"))
	if res.Success || !strings.Contains(res.Error, "is not available to this subagent") {
		t.Fatalf("an unrestricted subagent was not refused by the gate: success=%v error=%q", res.Success, res.Error)
	}
	if _, err := os.Stat(promoteTarget(wt, "made_echo")); !os.IsNotExist(err) {
		t.Fatalf("an unrestricted subagent copied a tool: %v", err)
	}
	if oracle.calls != 0 {
		t.Fatalf("the oracle was called %d times for a refused subagent, want 0", oracle.calls)
	}
}

func TestToolPromoteRefusedFromLuaInSubagent(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })
	_, wt := linkedWorktree(t)
	if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}
	oracle := useFakeOracle(t, "SAFE", nil)
	script := `return tool("tool_promote", {name = "made_echo"})`

	res := RunTool(WithWorkdir(subagentCtx(dir, nil, nil), wt), "lua", map[string]any{"script": script})
	if !strings.Contains(res.Error+res.Content, "is not available to this subagent") {
		t.Fatalf("lua tool() did not hit the gate: success=%v content=%q error=%q", res.Success, res.Content, res.Error)
	}
	if _, err := os.Stat(promoteTarget(wt, "made_echo")); !os.IsNotExist(err) {
		t.Fatalf("an unrestricted subagent copied a tool through lua: %v", err)
	}
	if oracle.calls != 0 {
		t.Fatalf("the oracle was called %d times through lua, want 0", oracle.calls)
	}

	// Control: a role that lists tool_promote copies the tool through lua.
	p := Policy{Role: "r", Permission: PermissionWorktreeWrite, Tools: []string{"lua", toolPromoteName}, Dir: wt}
	RunTool(WithWorkdir(subagentCtx(dir, p.Tools, &p), wt), "lua", map[string]any{"script": script})
	if _, err := os.Stat(promoteTarget(wt, "made_echo")); err != nil {
		t.Fatalf("control: a role that lists tool_promote did not copy the tool through lua: %v", err)
	}
}

func TestToolPromoteRoleNeedsListing(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })
	_, wt := linkedWorktree(t)
	if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}
	oracle := useFakeOracle(t, "SAFE", nil)

	if AllowedFor(Policy{Role: "r", Permission: PermissionFull}, toolPromoteName) {
		t.Fatal("a full role without a tools list may call tool_promote")
	}
	p := Policy{Role: "r", Permission: PermissionWorktreeWrite, Tools: []string{"read"}, Dir: wt}
	ctx := WithPolicy(WithWorkdir(WithRunDir(context.Background(), dir), wt), p)
	res := RunTool(ctx, "tool_promote", promoteInput("made_echo"))
	if res.Success || !strings.Contains(res.Error, "tool_promote") {
		t.Fatalf("a role without tool_promote in its list was not refused: success=%v error=%q", res.Success, res.Error)
	}
	if oracle.calls != 0 {
		t.Fatalf("the oracle was called %d times for a refused role, want 0", oracle.calls)
	}
}

func TestToolPromoteSchemaVisibility(t *testing.T) {
	names := func(schema []map[string]any) []string {
		var out []string
		for _, s := range schema {
			if fn, ok := s["function"].(map[string]any); ok {
				if n, ok := fn["name"].(string); ok {
					out = append(out, n)
				}
			}
		}
		return out
	}

	if !slices.Contains(names(GetTopLevelToolsSchema()), toolPromoteName) {
		t.Fatal("the main chat schema does not offer tool_promote")
	}
	if slices.Contains(names(GetSubagentToolsSchema()), toolPromoteName) {
		t.Fatal("an unrestricted subagent is offered tool_promote")
	}
	if !slices.Contains(names(subagentToolsSchemaFor([]string{"read", toolPromoteName})), toolPromoteName) {
		t.Fatal("a subagent whose list names tool_promote is not offered it")
	}
	if slices.Contains(names(subagentToolsSchemaFor([]string{"read"})), toolPromoteName) {
		t.Fatal("a subagent whose list does not name tool_promote is offered it")
	}
}

func TestToolPromoteRefusedForReadOnlyRole(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })
	_, wt := linkedWorktree(t)
	if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}
	oracle := useFakeOracle(t, "SAFE", nil)

	// A read-only role that lists tool_promote is refused: the list alone does not grant it.
	for _, perm := range []string{PermissionReadOnly, ""} {
		p := Policy{Role: "r", Permission: perm, Tools: []string{"read", toolPromoteName}, Dir: wt}
		if AllowedFor(p, toolPromoteName) {
			t.Fatalf("a role with permission %q may call tool_promote although it lists it", perm)
		}
	}
	// Control: the same list with worktree-write is allowed.
	if !AllowedFor(Policy{Role: "r", Permission: PermissionWorktreeWrite, Tools: []string{"read", toolPromoteName}, Dir: wt}, toolPromoteName) {
		t.Fatal("control: a worktree-write role that lists tool_promote may not call it")
	}

	p := Policy{Role: "r", Permission: PermissionReadOnly, Tools: []string{"read", toolPromoteName}, Dir: wt}
	ctx := WithPolicy(WithWorkdir(WithRunDir(context.Background(), dir), wt), p)
	res := RunTool(ctx, "tool_promote", promoteInput("made_echo"))
	if res.Success || !strings.Contains(res.Error, "tool_promote") {
		t.Fatalf("a read-only role that lists tool_promote was not refused: success=%v error=%q", res.Success, res.Error)
	}
	if _, err := os.Stat(promoteTarget(wt, "made_echo")); !os.IsNotExist(err) {
		t.Fatalf("a read-only role copied a tool into the worktree: %v", err)
	}
	if oracle.calls != 0 {
		t.Fatalf("the oracle was called %d times for a read-only role, want 0", oracle.calls)
	}
}

// policyRunner is a SubAgentRunner that records the tool policy and the tool
// list of each run. It answers SAFE, so the oracle lets the tool be copied.
type policyRunner struct {
	called    bool
	policy    Policy
	hasPolicy bool
	tools     []string
}

func (r *policyRunner) answer(ctx context.Context, opts SubagentOptions) (string, error) {
	r.called = true
	r.policy, r.hasPolicy = PolicyOf(ctx)
	r.tools = opts.Tools
	return "SAFE", nil
}

func (r *policyRunner) RunTask(ctx context.Context, _ string, _ string, opts SubagentOptions) (string, error) {
	return r.answer(ctx, opts)
}

func (r *policyRunner) RunTaskWithSystem(ctx context.Context, _ string, _ string, _ string, opts SubagentOptions) (string, error) {
	return r.answer(ctx, opts)
}

// TestToolPromoteOracleUsesReadOnlyPolicy runs the real promoteOracle, not a
// fake, with a fake SubAgentRunner. It checks the policy that the oracle run gets.
func TestToolPromoteOracleUsesReadOnlyPolicy(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })
	_, wt := linkedWorktree(t)
	if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}
	// SetSubAgentRunner replaces package globals. The test does not run in parallel.
	oldSub, oldScout := subagentToolInstance, scoutToolInstance
	t.Cleanup(func() {
		subagentToolInstance, scoutToolInstance = oldSub, oldScout
		if oldSub != nil {
			registerTool("subagent", oldSub)
		}
		if oldScout != nil {
			registerTool("scout", oldScout)
		}
	})
	runner := &policyRunner{}
	SetSubAgentRunner(runner)

	res := RunTool(promoteCtx(dir, wt), "tool_promote", promoteInput("made_echo"))
	if !res.Success {
		t.Fatalf("a SAFE tool was not promoted: %s", res.Error)
	}
	if !runner.called || !runner.hasPolicy {
		t.Fatalf("the oracle run did not reach the runner with a policy: called=%v policy=%v", runner.called, runner.hasPolicy)
	}
	if runner.policy.Role != "oracle" || runner.policy.Permission != PermissionReadOnly {
		t.Fatalf("the oracle policy is role %q permission %q, want oracle read-only", runner.policy.Role, runner.policy.Permission)
	}
	if runner.policy.Tools == nil || len(runner.policy.Tools) != 0 {
		t.Fatalf("the oracle policy lists tools %v, want an empty list", runner.policy.Tools)
	}
	if !slices.Equal(runner.tools, []string{"help"}) {
		t.Fatalf("the oracle run offers tools %v, want [help]", runner.tools)
	}
}

// submoduleCheckout makes a superproject with one submodule. It returns the
// real path of the submodule checkout, whose .git is a file.
func submoduleCheckout(t *testing.T) string {
	t.Helper()
	super := t.TempDir()
	gitIn(t, super, "init", "-q")
	gitIn(t, super, "commit", "-q", "--allow-empty", "-m", "init")
	lib := t.TempDir()
	gitIn(t, lib, "init", "-q")
	gitIn(t, lib, "commit", "-q", "--allow-empty", "-m", "init")
	gitIn(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", lib, "libsub")
	real, err := filepath.EvalSymlinks(filepath.Join(super, "libsub"))
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// TestToolPromoteAcceptsGitFileRoots checks that a worktree root whose .git is
// a file (a linked worktree or a submodule) is accepted.
func TestToolPromoteAcceptsGitFileRoots(t *testing.T) {
	defer SnapshotLuaToolsForTesting()()
	dir := t.TempDir()
	t.Cleanup(func() { RemoveRunTools(dir) })
	if res := createInRun(t, dir, "made_echo", createSource); !res.Success {
		t.Fatalf("tool_create failed: %s", res.Error)
	}
	_, wt := linkedWorktree(t)
	sub := submoduleCheckout(t)

	for _, tc := range []struct{ name, root string }{{"linked worktree", wt}, {"submodule", sub}} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := os.Stat(filepath.Join(tc.root, ".git"))
			if err != nil || !info.Mode().IsRegular() {
				t.Fatalf("the fixture .git is not a file: %v", err)
			}
			oracle := useFakeOracle(t, "SAFE", nil)

			res := RunTool(promoteCtx(dir, tc.root), "tool_promote", promoteInput("made_echo"))
			if !res.Success {
				t.Fatalf("a root with a .git file was refused: %s", res.Error)
			}
			if _, err := os.Stat(promoteTarget(tc.root, "made_echo")); err != nil {
				t.Fatalf("the tool was not copied: %v", err)
			}
			if oracle.dir != tc.root {
				t.Fatalf("the oracle works in %q, want %q", oracle.dir, tc.root)
			}
		})
	}
}
