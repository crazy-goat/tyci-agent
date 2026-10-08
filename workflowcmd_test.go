package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/hooks"
	"github.com/crazy-goat/tyci-agent/internal/trust"
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/tools"
)

const wfOKScript = "#!/bin/sh\nexit 0\n"

// wfCommandTimeout ends a workflow command that does not report its end, so a
// broken run fails the test instead of hanging until the test timeout.
const wfCommandTimeout = time.Minute

// wfHome isolates HOME, so the roles, runs and trust of a developer do not leak in.
// The empty providers.json stops a run from fetching the models.dev catalog.
func wfHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	wfWrite(t, filepath.Join(home, ".tyci", "providers.json"), "{}")
	return home
}

// wfGit runs git in dir and fails the test on error.
func wfGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// wfProject makes a git repository whose origin is a GitHub URL, which
// flow.DetectRepoAt needs. It returns the real path of the repository.
func wfProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wfGit(t, dir, "init", "-q", "-b", "main")
	wfGit(t, dir, "remote", "add", "origin", "https://github.com/acme/demo.git")
	wfGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// wfRunRepo makes a clone of a local origin with one commit on main. A run
// creates its worktree from origin/main, so it needs a real origin.
func wfRunRepo(t *testing.T, home string) flow.RepoInfo {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "demo")
	wfGit(t, root, "init", "-q", "--bare", "-b", "main", origin)
	wfGit(t, root, "clone", "-q", origin, work)
	wfGit(t, work, "commit", "-q", "--allow-empty", "-m", "init")
	wfGit(t, work, "push", "-q", "-u", "origin", "main")
	return flow.RepoInfo{Home: home, Root: work, Repo: "acme/demo", DefaultBranch: "main"}
}

// wfUseRepo makes the workflow commands use info as the repository of the run.
func wfUseRepo(t *testing.T, info flow.RepoInfo) {
	t.Helper()
	old := workflowRepoInfo
	workflowRepoInfo = func(string) (flow.RepoInfo, error) { return info, nil }
	t.Cleanup(func() { workflowRepoInfo = old })
}

// wfWrite writes a file and its directories.
func wfWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// oneCheckFlow is a workflow named name: one check state, then end.
func oneCheckFlow(name string) string {
	return `{"name":"` + name + `","start":"check","states":{` +
		`"check":{"check":"checks/ok.sh","on":{"default":"end"}},` +
		`"end":{"end":true}}}`
}

// runWorkflowCLI runs tyci with args and returns stdout, stderr and the error.
// The command context has the deadline wfCommandTimeout. Cobra keeps the context
// of a subcommand after its first run, so the workflow commands get this call's
// context each time.
func runWorkflowCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	resetWorkflowFlags()
	ctx, cancel := context.WithTimeout(context.Background(), wfCommandTimeout)
	t.Cleanup(cancel)
	for _, c := range workflowCmd.Commands() {
		c.SetContext(ctx)
	}
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	err := rootCmd.ExecuteContext(ctx)
	return out.String(), errOut.String(), err
}

// wfReportPath finds the report that an agent must write in its task text.
var wfReportPath = regexp.MustCompile(`MUST write (\S+report\.md)`)

// wfUseSpawn replaces the agent spawn of the workflow commands for the test.
func wfUseSpawn(t *testing.T, spawn func(context.Context, tools.TaskSpec) (string, string, error)) {
	t.Helper()
	old := workflowSpawn
	workflowSpawn = spawn
	t.Cleanup(func() { workflowSpawn = old })
}

// resetWorkflowFlags sets the flag variables back to their defaults. Cobra keeps
// the values of the previous Execute.
func resetWorkflowFlags() {
	workflowDir, workflowEjectForce = "", false
	workflowRunJSON, workflowRunDir = false, ""
	workflowValidateJSON, workflowValidateDir = false, ""
	workflowStatusJSON = false
}

// decodeWorkflowResult decodes out as exactly one JSON object.
func decodeWorkflowResult(t *testing.T, out string) workflowResult {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var r workflowResult
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, out)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("stdout holds more than one JSON value: %q", out)
	}
	return r
}

func TestWorkflowValidateOK(t *testing.T) {
	home := wfHome(t)
	project := wfProject(t)
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "one-check.json"), oneCheckFlow("one-check"))
	wfWrite(t, filepath.Join(home, ".tyci", "checks", "ok.sh"), wfOKScript)

	out, _, err := runWorkflowCLI(t, "workflow", "validate", "one-check", "--json", "--dir", project)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	r := decodeWorkflowResult(t, out)
	if r.OK == nil || !*r.OK || len(r.Errors) != 0 || r.Workflow != "one-check" {
		t.Fatalf("result = %+v", r)
	}
}

func TestWorkflowValidateUnknownTarget(t *testing.T) {
	home := wfHome(t)
	project := wfProject(t)
	body := `{"name":"bad-target","start":"check","states":{` +
		`"check":{"check":"checks/ok.sh","on":{"default":"mrge"}},` +
		`"end":{"end":true}}}`
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "bad-target.json"), body)
	wfWrite(t, filepath.Join(home, ".tyci", "checks", "ok.sh"), wfOKScript)

	out, _, err := runWorkflowCLI(t, "workflow", "validate", "bad-target", "--json", "--dir", project)
	if err == nil {
		t.Fatal("validate of an invalid workflow must fail")
	}
	r := decodeWorkflowResult(t, out)
	if r.OK == nil || *r.OK || len(r.Errors) != 1 || !strings.Contains(r.Errors[0], `"mrge"`) {
		t.Fatalf("result = %+v", r)
	}
}

func TestWorkflowValidateUndefinedRole(t *testing.T) {
	home := wfHome(t)
	project := wfProject(t)
	body := `{"name":"role-typo","start":"review","states":{` +
		`"review":{"agent":"reviwer","on":{"default":"end"}},` +
		`"end":{"end":true}}}`
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "role-typo.json"), body)

	out, _, err := runWorkflowCLI(t, "workflow", "validate", "role-typo", "--json", "--dir", project)
	if err == nil {
		t.Fatal("validate with an undefined role must fail")
	}
	r := decodeWorkflowResult(t, out)
	if r.OK == nil || *r.OK || len(r.Errors) == 0 || !strings.Contains(strings.Join(r.Errors, "\n"), "reviwer") {
		t.Fatalf("result = %+v", r)
	}
}

func TestWorkflowRunDone(t *testing.T) {
	home := wfHome(t)
	wfUseRepo(t, wfRunRepo(t, home))
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "one-check.json"), oneCheckFlow("one-check"))
	wfWrite(t, filepath.Join(home, ".tyci", "checks", "ok.sh"), wfOKScript)

	out, _, err := runWorkflowCLI(t, "workflow", "run", "one-check", "7", "--json")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	r := decodeWorkflowResult(t, out)
	if r.Status != "done" || r.RunID == "" || r.Error != "" || strings.Contains(out, `"error"`) {
		t.Fatalf("result = %+v", r)
	}
	if _, err := os.Stat(r.StateFile); err != nil {
		t.Errorf("state file: %v", err)
	}
}

func TestWorkflowRunFailed(t *testing.T) {
	home := wfHome(t)
	wfUseRepo(t, wfRunRepo(t, home))
	body := `{"name":"fail-check","start":"check","states":{` +
		`"check":{"check":"checks/fail.sh","on":{"ok":"end"}},` +
		`"end":{"end":true}}}`
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "fail-check.json"), body)
	wfWrite(t, filepath.Join(home, ".tyci", "checks", "fail.sh"), "#!/bin/sh\necho fail\n")

	out, _, err := runWorkflowCLI(t, "workflow", "run", "fail-check", "8", "--json")
	if err == nil {
		t.Fatal("a failed run must exit with an error")
	}
	r := decodeWorkflowResult(t, out)
	if r.Status != "failed" || r.Error == "" {
		t.Fatalf("result = %+v", r)
	}
}

func TestWorkflowRunStopsAtAsk(t *testing.T) {
	home := wfHome(t)
	wfUseRepo(t, wfRunRepo(t, home))
	body := `{"name":"ask-flow","start":"check","states":{` +
		`"check":{"check":"checks/ok.sh","on":{"default":"wait"}},` +
		`"wait":{"ask":"need an answer","on":{"go":"end"}},` +
		`"end":{"end":true}}}`
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "ask-flow.json"), body)
	wfWrite(t, filepath.Join(home, ".tyci", "checks", "ok.sh"), wfOKScript)

	out, errOut, err := runWorkflowCLI(t, "workflow", "run", "ask-flow", "9", "--json")
	if err != nil {
		t.Fatalf("a run paused at ask must exit 0: %v", err)
	}
	r := decodeWorkflowResult(t, out)
	if r.Status != "paused" || r.State != "wait" {
		t.Fatalf("result = %+v", r)
	}
	if !strings.Contains(errOut, "waiting at ask state wait") {
		t.Errorf("stderr = %q, want the ask state notice", errOut)
	}
}

func TestAwaitRunEndsOnFinalStatusOfItsRun(t *testing.T) {
	events := make(chan flow.RunEvent, 4)
	events <- flow.RunEvent{Run: "other", Status: "done"}
	events <- flow.RunEvent{Run: "mine", Status: "running"}
	events <- flow.RunEvent{Run: "mine", Status: "paused"}
	if err := awaitRun(context.Background(), events, "mine"); err != nil {
		t.Fatalf("awaitRun: %v", err)
	}
}

func TestAwaitRunReturnsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	events := make(chan flow.RunEvent)
	if err := awaitRun(ctx, events, "mine"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("awaitRun = %v, want the deadline error", err)
	}
}

// TestWorkflowRunAgentGetsProvidersAndHooks runs a workflow with an agent state.
// The stub agent resolves the model the way the real agent runner does, so a
// missing provider fails the run. It also records whether the global hooks loaded.
func TestWorkflowRunAgentGetsProvidersAndHooks(t *testing.T) {
	home := wfHome(t)
	t.Cleanup(hooks.SetForTesting(nil))
	wfUseRepo(t, wfRunRepo(t, home))
	wfWrite(t, filepath.Join(home, ".tyci", "config.json"),
		`{"default_model":"wfprov/wfmodel","roles":{"helper":{"prompt":"Do the task."}}}`)
	wfWrite(t, filepath.Join(home, ".tyci", "model.json"),
		`{"wfprov":{"wfmodel":{"uri":"openai://wfmodel@$KEY@example.com/v1"}}}`)
	wfWrite(t, filepath.Join(home, ".tyci", "hooks.json"),
		`{"hooks":[{"event":"pre_tool","command":"true"}]}`)
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "agent-flow.json"),
		`{"name":"agent-flow","start":"work","states":{`+
			`"work":{"agent":"helper","on":{"done":"end"}},`+
			`"end":{"end":true}}}`)

	hookSeen := false
	wfUseSpawn(t, func(ctx context.Context, spec tools.TaskSpec) (string, string, error) {
		hookSeen = hooks.Any(hooks.EventPreTool)
		if _, err := resolveModelClient(ctx, spec.Model); err != nil {
			return "", "", err
		}
		m := wfReportPath.FindStringSubmatch(spec.Task)
		if m == nil {
			return "", "", errors.New("the task names no report")
		}
		if err := os.WriteFile(m[1], []byte("done\n"), 0o600); err != nil {
			return "", "", err
		}
		return "done", "wf-session", nil
	})

	out, errOut, err := runWorkflowCLI(t, "workflow", "run", "agent-flow", "11", "--json")
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errOut)
	}
	r := decodeWorkflowResult(t, out)
	if r.Status != "done" {
		t.Fatalf("result = %+v", r)
	}
	if !hookSeen {
		t.Error("the global hooks.json was not loaded for the agent")
	}
}

func TestWorkflowRunInvalidDoesNotStart(t *testing.T) {
	home := wfHome(t)
	wfUseRepo(t, wfRunRepo(t, home))
	body := `{"name":"bad-target","start":"check","states":{` +
		`"check":{"check":"checks/ok.sh","on":{"default":"mrge"}},` +
		`"end":{"end":true}}}`
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "bad-target.json"), body)
	wfWrite(t, filepath.Join(home, ".tyci", "checks", "ok.sh"), wfOKScript)

	out, _, err := runWorkflowCLI(t, "workflow", "run", "bad-target", "10", "--json")
	if err == nil {
		t.Fatal("run of an invalid workflow must fail")
	}
	r := decodeWorkflowResult(t, out)
	if r.OK == nil || *r.OK || r.RunID != "" {
		t.Fatalf("result = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(home, ".tyci", "runs")); err == nil {
		t.Error("an invalid workflow must not create a run")
	}
}

// wfSaveRun writes the state of a run of repo under home, as a run does.
func wfSaveRun(t *testing.T, home, repo, id, status string) string {
	t.Helper()
	dir := flow.RunDir(home, repo, id)
	st := &flow.RunState{
		Version: 1, Run: id, Workflow: "one-check", Repo: "acme/" + repo, Issue: 42,
		Status: status, Current: "end", Visits: map[string]int{"check": 1},
	}
	if err := (&flow.Store{Dir: dir}).Save(st); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWorkflowStatusReadsStateFile(t *testing.T) {
	home := wfHome(t)
	const id = "20261005-153012-42"
	dir := wfSaveRun(t, home, "demo", id, "done")

	out, _, err := runWorkflowCLI(t, "workflow", "status", id, "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	r := decodeWorkflowResult(t, out)
	if r.Status != "done" || r.State != "end" || r.Workflow != "one-check" || r.Visits["check"] != 1 {
		t.Fatalf("result = %+v", r)
	}
	if r.StateFile != flow.StatePath(dir) || strings.Contains(out, `"error"`) {
		t.Fatalf("state_file = %q, want %q", r.StateFile, flow.StatePath(dir))
	}
}

func TestWorkflowStatusUnknownRun(t *testing.T) {
	wfHome(t)
	out, _, err := runWorkflowCLI(t, "workflow", "status", "20261005-153012-99", "--json")
	if err == nil {
		t.Fatal("status of an unknown run must fail")
	}
	r := decodeWorkflowResult(t, out)
	if !strings.Contains(r.Error, "run not found") {
		t.Fatalf("result = %+v", r)
	}
}

func TestWorkflowStatusAmbiguousRun(t *testing.T) {
	home := wfHome(t)
	const id = "20261005-153012-42"
	wfSaveRun(t, home, "a", id, "done")
	wfSaveRun(t, home, "b", id, "done")

	out, _, err := runWorkflowCLI(t, "workflow", "status", id, "--json")
	if err == nil {
		t.Fatal("status of an ambiguous run must fail")
	}
	r := decodeWorkflowResult(t, out)
	if !strings.Contains(r.Error, "ambiguous") {
		t.Fatalf("result = %+v", r)
	}
}

func TestWorkflowJSONIsOneObject(t *testing.T) {
	home := wfHome(t)
	project := wfProject(t)
	// An end state that no transition reaches gives a warning, which goes to stderr.
	body := `{"name":"orphan","start":"check","states":{` +
		`"check":{"check":"checks/ok.sh","on":{"default":"end"}},` +
		`"end":{"end":true},"orphan":{"end":true}}}`
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "orphan.json"), body)
	wfWrite(t, filepath.Join(home, ".tyci", "checks", "ok.sh"), wfOKScript)

	out, errOut, err := runWorkflowCLI(t, "workflow", "validate", "orphan", "--json", "--dir", project)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	decodeWorkflowResult(t, out)
	if !strings.Contains(errOut, "unreachable") {
		t.Errorf("stderr = %q, want the warning", errOut)
	}
}

func TestWorkflowUntrustedProjectSkipsLocalWorkflows(t *testing.T) {
	wfHome(t)
	project := wfProject(t)
	wfWrite(t, filepath.Join(project, ".tyci", "workflows", "local-only.json"), oneCheckFlow("local-only"))
	wfWrite(t, filepath.Join(project, ".tyci", "checks", "ok.sh"), wfOKScript)

	out, errOut, err := runWorkflowCLI(t, "workflow", "validate", "local-only", "--json", "--dir", project)
	if err == nil {
		t.Fatal("a local workflow of an untrusted project must not be found")
	}
	r := decodeWorkflowResult(t, out)
	if !strings.Contains(r.Error, "not found") {
		t.Fatalf("result = %+v", r)
	}
	if !strings.Contains(errOut, "not trusted") {
		t.Errorf("stderr = %q, want the untrusted notice", errOut)
	}
}

func TestWorkflowTrustedProjectLoadsLocalWorkflows(t *testing.T) {
	wfHome(t)
	project := wfProject(t)
	key, err := session.ProjectKey(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := trust.SetTrusted(key, true); err != nil {
		t.Fatal(err)
	}
	wfWrite(t, filepath.Join(project, ".tyci", "workflows", "local-only.json"), oneCheckFlow("local-only"))
	wfWrite(t, filepath.Join(project, ".tyci", "checks", "ok.sh"), wfOKScript)

	out, errOut, err := runWorkflowCLI(t, "workflow", "validate", "local-only", "--json", "--dir", project)
	if err != nil {
		t.Fatalf("validate: %v\nstderr: %s", err, errOut)
	}
	r := decodeWorkflowResult(t, out)
	if r.OK == nil || !*r.OK {
		t.Fatalf("result = %+v", r)
	}
	if strings.Contains(errOut, "not trusted") {
		t.Errorf("stderr = %q, a trusted project must not warn", errOut)
	}
}
