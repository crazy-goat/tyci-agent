package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// trustWorkflowProject records project (an absolute path outside any git
// repo, matching session.ProjectKey's non-git fallback) as trusted in the
// test HOME's trust.json — the same file internal/trust.SetTrusted writes,
// built by hand here since the subprocess under test runs with HOME=testDir
// while this test process's own HOME is the real one. Needed because
// `tyci workflow run`/`list` now gate project-local .tyci/agents/*.lua
// discovery on trust.Decide, the same way .tyci/tools/*.lua and
// .tyci/cron.json are gated — see workflowcmd.go's workflowTrustedDirs.
type trustRecord struct {
	Trusted   bool      `json:"trusted"`
	DecidedAt time.Time `json:"decided_at"`
}

type trustFile struct {
	Projects map[string]trustRecord `json:"projects"`
}

func trustWorkflowProject(t *testing.T, project string) {
	t.Helper()
	abs, err := filepath.Abs(project)
	if err != nil {
		t.Fatal(err)
	}
	// Resolved through symlinks: on macOS the temp dir is reached through
	// one (/tmp -> /private/tmp, and similarly for /var/folders), and the
	// subprocess computes its project key from its own os.Getwd() after
	// actually cd'ing into project (cmd.Dir) — which returns the
	// symlink-resolved physical path, not the string this test started
	// with. Without this, the recorded trust.json key never matches what
	// workflowTrustedDirs looks up. See commands_trust_wiring_test.go's
	// TestInitCommon_TrustedProject_LoadsLocalHooksAndLuaToo for the same
	// fix applied to an in-process equivalent.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}

	tyciDir := filepath.Join(testDir, ".tyci")
	if err := os.MkdirAll(tyciDir, 0755); err != nil {
		t.Fatal(err)
	}
	trustPath := filepath.Join(tyciDir, "trust.json")

	f := trustFile{Projects: map[string]trustRecord{}}
	if data, err := os.ReadFile(trustPath); err == nil {
		_ = json.Unmarshal(data, &f) // corrupt/missing file: start clean, same as internal/trust.load
		if f.Projects == nil {
			f.Projects = map[string]trustRecord{}
		}
	}
	f.Projects[abs] = trustRecord{Trusted: true, DecidedAt: time.Now()}

	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trustPath, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestWorkflowRunCLI_DiscoversAndRunsProjectLocalScript verifies TODO.md
// item 7's CLI entry point end to end: `tyci workflow run <name>` discovers
// a Lua orchestration script under <project>/.tyci/agents/<name>.lua (the
// same directory named markdown agent definitions use — see
// internal/workflow.ResolveScript) and actually executes it through the
// engine.
func TestWorkflowRunCLI_DiscoversAndRunsProjectLocalScript(t *testing.T) {
	project := t.TempDir()
	agentsDir := filepath.Join(project, ".tyci", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	script := `return "hello, " .. prompt`
	if err := os.WriteFile(filepath.Join(agentsDir, "greet.lua"), []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	trustWorkflowProject(t, project)

	cmd := exec.Command(binPath, "workflow", "run", "greet", "--prompt", "world")
	cmd.Dir = project
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workflow run: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "hello, world" {
		t.Fatalf("workflow run output = %q, want %q", got, "hello, world")
	}
}

// TestWorkflowListCLI_ListsProjectLocalScript verifies `tyci workflow list`
// surfaces a project-local .lua script.
func TestWorkflowListCLI_ListsProjectLocalScript(t *testing.T) {
	project := t.TempDir()
	agentsDir := filepath.Join(project, ".tyci", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "triage.lua"), []byte(`return "ok"`), 0644); err != nil {
		t.Fatal(err)
	}
	trustWorkflowProject(t, project)

	cmd := exec.Command(binPath, "workflow", "list")
	cmd.Dir = project
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workflow list: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "triage") {
		t.Fatalf("workflow list output = %q, want it to contain %q", out, "triage")
	}
}

// TestWorkflowRunCLI_UntrustedProjectSkipsProjectLocalScript is the
// regression test for the security fix: an UNtrusted project's
// .tyci/agents/*.lua must not be discoverable by name at all — before this
// fix, ResolveScript/ListScripts consulted the project-local directory
// unconditionally, letting a name-based `tyci workflow run <name>` execute
// arbitrary project-supplied Lua (full os/io stdlib, at the time) with no
// trust decision in the way, in a directory the test process never marked
// trusted.
func TestWorkflowRunCLI_UntrustedProjectSkipsProjectLocalScript(t *testing.T) {
	project := t.TempDir()
	agentsDir := filepath.Join(project, ".tyci", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "untrusted.lua"), []byte(`return "should not run"`), 0644); err != nil {
		t.Fatal(err)
	}
	// Deliberately NOT calling trustWorkflowProject: this project has no
	// recorded trust decision, so trust.Decide (non-interactive here) must
	// default to untrusted.

	runCmd := exec.Command(binPath, "workflow", "run", "untrusted")
	runCmd.Dir = project
	runCmd.Env = testEnv()
	out, err := runCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected an untrusted project's workflow script to fail to resolve, got output %q", out)
	}
	if !strings.Contains(string(out), "not trusted") {
		t.Errorf("workflow run output = %q, want it to mention the project is not trusted", out)
	}
	if strings.Contains(string(out), "should not run") {
		t.Fatalf("untrusted project's script actually ran; output = %q", out)
	}

	listCmd := exec.Command(binPath, "workflow", "list")
	listCmd.Dir = project
	listCmd.Env = testEnv()
	listOut, err := listCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workflow list: %v\n%s", err, listOut)
	}
	if strings.Contains(string(listOut), "untrusted") {
		t.Fatalf("workflow list surfaced an untrusted project's script; output = %q", listOut)
	}
}

// TestWorkflowRunCLI_UnknownScriptFails verifies a name that resolves to
// nothing is a clean error, not a panic or an empty success.
func TestWorkflowRunCLI_UnknownScriptFails(t *testing.T) {
	cmd := exec.Command(binPath, "workflow", "run", "does-not-exist")
	cmd.Dir = t.TempDir()
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected an error for an unknown workflow script, got output %q", out)
	}
	if !strings.Contains(string(out), "does-not-exist") {
		t.Fatalf("error output = %q, want it to mention the missing script name", out)
	}
}

// TestWorkflowRunCLI_ExplicitPathBypassesDiscovery verifies a .lua path
// (rather than a bare name) is used directly, without needing to live under
// .tyci/agents at all.
func TestWorkflowRunCLI_ExplicitPathBypassesDiscovery(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "adhoc.lua")
	if err := os.WriteFile(scriptPath, []byte(`return "ran adhoc script"`), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binPath, "workflow", "run", scriptPath)
	cmd.Env = testEnv()
	// stdout and stderr captured separately, not CombinedOutput: this
	// fixture directory has no recorded trust decision, so (since round 1
	// finding 1) stderr now carries the untrusted-project warning even for
	// an explicit path. This test's own concern is narrower: the script's
	// own result must be exactly its return value on stdout, regardless of
	// that warning (which TestWorkflowRunCLI_UntrustedProject_Skips... above
	// asserts the text of).
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("workflow run: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "ran adhoc script" {
		t.Fatalf("workflow run stdout = %q, want %q", got, "ran adhoc script")
	}
}

// TestWorkflowRunCLI_RegistersProvidersWithoutTyciModels is the regression
// test for the fix to workflowcmd.go: `tyci workflow run` used to skip
// registerProviders() entirely (unlike every other agent-running path, all
// wired through initCommon), so session:await() failed with "model not
// found" unless a script happened to call tyci.models() first — a call
// whose real job is listing providers, and whose side effect of also
// registering them was the only thing making session:await() work at all.
//
// This drives a script that calls session:await() WITHOUT ever calling
// tyci.models(), and checks the failure it gets back (a real model call
// against a fake test URI has nowhere to actually succeed) is a connection
// failure, never "model not found: ...": that specific error string is only
// possible when providers were never registered.
func TestWorkflowRunCLI_RegistersProvidersWithoutTyciModels(t *testing.T) {
	// A project-local model.json with a model unique to this test, pointed
	// at a host nothing listens on (127.0.0.1:1 — connection refused
	// immediately, unlike the real example.com the global test-provider
	// uses, which this test cannot afford to actually dial).
	project := t.TempDir()
	tyciDir := filepath.Join(project, ".tyci")
	if err := os.MkdirAll(tyciDir, 0755); err != nil {
		t.Fatal(err)
	}
	modelCfg := map[string]map[string]map[string]string{
		"wf-provider": {
			"wf-model": {"uri": "openai://wf-model@$TEST_API_KEY@127.0.0.1:1/v1"},
		},
	}
	data, err := json.Marshal(modelCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tyciDir, "model.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(project, "await.lua")
	script := `
		local s = tyci.new_session("wf-provider/wf-model")
		local reply, err = s:await()
		if err then
			return "ERR:" .. err
		end
		return "OK:" .. (reply.content or "")
	`
	if err := os.WriteFile(scriptPath, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binPath, "workflow", "run", scriptPath)
	cmd.Dir = project // so localModelJSONPath (os.Getwd()-based) picks up .tyci/model.json above
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	// The connection-refused dial is expected to fail (RunE returns that
	// error); what matters is which failure.
	_ = err
	if strings.Contains(string(out), "model not found") {
		t.Fatalf("workflow run reported \"model not found\" — registerProviders() was not called before running the script; output = %q", out)
	}
}

// writeWorkflowEnvProbe writes a project with a .tyci/hooks.json that vetoes
// the built-in "read" tool with a distinctive message, a .tyci/tools/*.lua
// tool ("wf-env-probe-tool"), and a probe.lua workflow script (returned as
// its absolute path) that calls both directly via tyci.run_tool and reports
// what happened as "<hook-outcome>|<tool-outcome>". Shared by the trusted
// and untrusted variants of TestWorkflowRunCLI below (F28): the fixture is
// identical, only trust differs.
//
// The probe script uses an EXPLICIT .lua path (see workflowExplicitPath),
// deliberately bypassing the separate script-DISCOVERY trust gate (already
// covered by TestWorkflowRunCLI_UntrustedProjectSkipsProjectLocalScript
// above) so this test isolates the hooks/Lua-tools/cron-dir/MCP trust gate
// instead — the one setupProjectLocalEnv wires up.
func writeWorkflowEnvProbe(t *testing.T, project string) (scriptPath string) {
	t.Helper()
	tyciDir := filepath.Join(project, ".tyci")
	toolsDir := filepath.Join(tyciDir, "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		t.Fatal(err)
	}
	hooksJSON := `{"hooks":[{"event":"pre_tool","name":"wf-env-probe-hook","tools":["read"],"command":"echo hook-vetoed-wf-read; exit 1"}]}`
	if err := os.WriteFile(filepath.Join(tyciDir, "hooks.json"), []byte(hooksJSON), 0644); err != nil {
		t.Fatal(err)
	}
	luaTool := `return {
  schema = { name = "wf-env-probe-tool", description = "d", parameters = {} },
  run = function(ctx, args) return {success = true, content = "project-tool-ran"} end
}`
	if err := os.WriteFile(filepath.Join(toolsDir, "probe.lua"), []byte(luaTool), 0644); err != nil {
		t.Fatal(err)
	}
	script := `
		local hook_res = tyci.run_tool("read", {path = "wf-env-probe-nonexistent-marker"})
		local hook_outcome = "ALLOWED"
		if not hook_res.success then hook_outcome = hook_res.error end
		local tool_res = tyci.run_tool("wf-env-probe-tool", {})
		local tool_outcome = "MISSING"
		if tool_res.success then tool_outcome = tool_res.content end
		return hook_outcome .. "|" .. tool_outcome
	`
	scriptPath = filepath.Join(project, "probe.lua")
	if err := os.WriteFile(scriptPath, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	return scriptPath
}

// TestWorkflowRunCLI_TrustedProject_LoadsProjectLocalHooksAndLuaTools is
// F28's positive case: `tyci workflow run` on a trusted project must load
// project-local hooks (.tyci/hooks.json) and Lua tools (.tyci/tools/*.lua)
// exactly like `tyci run`/console/tui already do via initCommon — before
// the fix, workflowcmd.go called only registerProviders(), so a workflow
// script's tyci.run_tool calls silently ran with hooks off and no
// project-local tools regardless of trust.
func TestWorkflowRunCLI_TrustedProject_LoadsProjectLocalHooksAndLuaTools(t *testing.T) {
	project := t.TempDir()
	scriptPath := writeWorkflowEnvProbe(t, project)
	trustWorkflowProject(t, project)

	cmd := exec.Command(binPath, "workflow", "run", scriptPath)
	cmd.Dir = project
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workflow run: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if !strings.Contains(got, "hook-vetoed-wf-read") {
		t.Errorf("workflow run output = %q, want the project-local hook to have vetoed the read call", got)
	}
	if !strings.Contains(got, "project-tool-ran") {
		t.Errorf("workflow run output = %q, want the project-local Lua tool to have run", got)
	}
}

// TestWorkflowRunCLI_UntrustedProject_SkipsProjectLocalHooksAndLuaTools is
// F28's negative case: an untrusted project must keep getting the same
// posture initCommon already gives `tyci run` — project-local hooks and Lua
// tools skipped, exactly the same trust gate .tyci/agents/*.lua discovery
// goes through (workflowTrustedDirs), not a second, independently-decided
// trust answer.
func TestWorkflowRunCLI_UntrustedProject_SkipsProjectLocalHooksAndLuaTools(t *testing.T) {
	project := t.TempDir()
	scriptPath := writeWorkflowEnvProbe(t, project)
	// Deliberately NOT calling trustWorkflowProject: this project has no
	// recorded trust decision, so trust.Decide (non-interactive here)
	// defaults to untrusted.

	cmd := exec.Command(binPath, "workflow", "run", scriptPath)
	cmd.Dir = project
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workflow run: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if strings.Contains(got, "hook-vetoed-wf-read") {
		t.Errorf("workflow run output = %q, project-local hook must not run for an untrusted project", got)
	}
	if strings.Contains(got, "project-tool-ran") {
		t.Errorf("workflow run output = %q, project-local Lua tool must not run for an untrusted project", got)
	}
	if !strings.Contains(got, "MISSING") {
		t.Errorf("workflow run output = %q, want the unregistered project-local tool to report MISSING", got)
	}

	// Round 1 finding 1's regression check: an untrusted project run with
	// an EXPLICIT .lua path (scriptPath here, not a bare name) used to
	// print NOTHING about hooks/tools/mcp being skipped — the warning only
	// fired inside the name-based-discovery branch, which an explicit path
	// bypasses entirely. Silence there was a silent loss of a protection
	// the user thinks they have. The warning must appear regardless, and
	// must name what THIS invocation actually skipped (hooks/Lua
	// tools/cron dir/mcp.json), not just workflow-script discovery.
	if !strings.Contains(got, "not trusted") {
		t.Fatalf("workflow run output = %q, want an untrusted-project warning even for an explicit .lua path", got)
	}
	if !strings.Contains(got, "hooks (.tyci/hooks.json)") {
		t.Errorf("workflow run output = %q, want the warning to name project-local hooks as skipped", got)
	}
	if !strings.Contains(got, "Lua tools (.tyci/tools/*.lua)") {
		t.Errorf("workflow run output = %q, want the warning to name project-local Lua tools as skipped", got)
	}
	if !strings.Contains(got, "mcp.json are skipped") {
		t.Errorf("workflow run output = %q, want the warning to name mcp.json as skipped", got)
	}
	// An explicit path never went through name-based script discovery, so
	// the warning must not claim that gate did anything here.
	if strings.Contains(got, "not discoverable by name") {
		t.Errorf("workflow run output = %q, an explicit .lua path bypasses script discovery entirely, so the warning must not mention it", got)
	}
}
