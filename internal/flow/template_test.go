package flow

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/tools"
)

func TestInit_WritesWorkflowDir(t *testing.T) {
	dir := t.TempDir()
	written, err := Init("issue-to-merge", "", dir)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, ".tyci", "workflows", "issue-to-merge")
	all := strings.Join(written, "\n")
	for _, rel := range []string{"workflow.json", "checks/merge.sh", "checks/merge_approved.sh", "tasks/fixer.md", "tasks/recover.md",
		"tasks/findings_to_issues.md", "tasks/ask.md", "prompts/worker.md", "prompts/oracle.md"} {
		if !strings.Contains(all, filepath.Join(root, rel)) {
			t.Errorf("%s not in written list", rel)
		}
	}
	if st, err := os.Stat(filepath.Join(root, "checks", "merge.sh")); err != nil || st.Mode()&0o100 == 0 {
		t.Fatalf("merge.sh not executable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tyci", "config.json")); err == nil {
		t.Fatal("init wrote config.json")
	}
	wf, src, err := Lookup("issue-to-merge", t.TempDir(), dir, true)
	if err != nil || src != root || wf.Source != root {
		t.Fatalf("lookup: %q %v", src, err)
	}
}

// #753: a protected PR waits at ask_protected, a human pause the oracle does not answer. Only
// the approve answer reaches merge_approved, which sets TYCI_ALLOW_PROTECTED=1 and is guarded.
func TestIssueToMerge_ProtectedApprovalStates(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init("issue-to-merge", "", dir); err != nil {
		t.Fatal(err)
	}
	wf, _, err := Lookup("issue-to-merge", t.TempDir(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := wf.States["merge"].On["protected"]; got != "ask_protected" {
		t.Errorf("merge protected -> %q, want ask_protected", got)
	}
	ap := wf.States["ask_protected"]
	if !ap.Human || ap.Ask == "" || ap.On["approve"] != "merge_approved" || ap.On["stop"] != "end" {
		t.Errorf("ask_protected = %+v", ap)
	}
	ma := wf.States["merge_approved"]
	if ma.Check != "checks/merge_approved.sh" || ma.On["merged"] != "findings" || ma.On["protected"] != "ask_protected" {
		t.Errorf("merge_approved = %+v", ma)
	}
	if !isGuarded(wf, "merge_approved") || !isGuarded(wf, "merge") {
		t.Error("merge_approved or merge is not guarded: an oracle could merge a protected PR without a human")
	}
}

// #753: a failed or unclear merge_approved must not reach merge_approved again without a human.
// Walk the static routes from merge_approved. A path back to merge_approved must pass
// ask_protected or merge first, because only ask_protected asks a human. "$failed" is skipped:
// it resolves to the check that failed last, and merge_approved is entered only from ask_protected.
func TestIssueToMerge_MergeApprovedLoopNeedsHuman(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init("issue-to-merge", "", dir); err != nil {
		t.Fatal(err)
	}
	wf, _, err := Lookup("issue-to-merge", t.TempDir(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	ma := wf.States["merge_approved"]
	for _, k := range []string{"fail", "default"} {
		if ma.On[k] != "ask_protected" {
			t.Errorf("merge_approved %s -> %q, want ask_protected", k, ma.On[k])
		}
	}
	blocked := map[string]bool{"ask_protected": true, "merge": true}
	seen := map[string]bool{}
	queue := []string{}
	for _, next := range ma.On {
		queue = append(queue, next)
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if strings.HasPrefix(name, "$") || blocked[name] || seen[name] {
			continue
		}
		if name == "merge_approved" {
			t.Fatal("merge_approved is reachable from itself without ask_protected or merge")
		}
		seen[name] = true
		for _, next := range wf.States[name].On {
			queue = append(queue, next)
		}
	}
}

func TestInit_NameTemplateAndBadInput(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init("roadmap", "plan", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tyci", "workflows", "plan", "workflow.json")); err != nil {
		t.Fatal(err)
	}
	wf, _, err := Lookup("plan", t.TempDir(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Name != "plan" {
		t.Fatalf("name = %q, want plan", wf.Name)
	}
	if _, err := Init("nope", "", dir); err == nil || !strings.Contains(err.Error(), "unknown template") {
		t.Fatalf("unknown template: %v", err)
	}
	if _, err := Init("roadmap", "../x", dir); err == nil {
		t.Fatal("bad name accepted")
	}
}

func TestInit_NeverOverwritesAndKeepsConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".tyci", "config.json")
	e2eWrite(t, cfg, `{"roles":{"worker":{"prompt":"custom"}}}`)
	if _, err := Init("issue-to-merge", "", dir); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, ".tyci", "workflows", "issue-to-merge")
	e2eWrite(t, filepath.Join(root, "tasks", "fixer.md"), "mine")
	if _, err := Init("issue-to-merge", "", dir); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second init: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "tasks", "fixer.md")); string(b) != "mine" {
		t.Fatalf("fixer.md overwritten: %s", b)
	}
	if b, _ := os.ReadFile(cfg); string(b) != `{"roles":{"worker":{"prompt":"custom"}}}` {
		t.Fatalf("config changed: %s", b)
	}
}

func TestTaskTemplates_ReadsWorkflowDirOnly(t *testing.T) {
	wf := t.TempDir()
	e2eWrite(t, filepath.Join(wf, "tasks", "findings_to_issues.md"), "LOCAL {{.Repo}}")
	tt := TaskTemplates{Dir: wf}
	rc := RunContext{Repo: "o/r"}
	if out, err := tt.Render("findings_to_issues", rc); err != nil || out != "LOCAL o/r" {
		t.Fatalf("local: %q %v", out, err)
	}
	if _, err := tt.Render("fixer", rc); err == nil {
		t.Fatal("missing task accepted")
	}
	if _, err := tt.Render("../x", rc); err == nil {
		t.Fatal("bad task name accepted")
	}
	link := filepath.Join(wf, "tasks", "fixer.md")
	if err := os.Symlink(filepath.Join(wf, "tasks", "findings_to_issues.md"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := tt.Render("fixer", rc); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink: %v", err)
	}
}

func TestLookup_StatePromptFile(t *testing.T) {
	home := t.TempDir()
	wf := filepath.Join(home, ".tyci", "workflows", "w")
	wfJSON := func(prompt string) string {
		return `{"description":"d","start":"code","states":{"code":{"agent":"worker","prompt":"` + prompt + `","on":{"default":"end"}},"end":{"end":true}}}`
	}
	e2eWrite(t, filepath.Join(wf, "prompts", "x.md"), "STATE PROMPT")
	e2eWrite(t, filepath.Join(wf, "workflow.json"), wfJSON("@prompts/x.md"))
	got, _, err := Lookup("w", home, "", false)
	if err != nil || got.States["code"].Prompt != "STATE PROMPT" {
		t.Fatalf("prompt = %q, %v", got.States["code"].Prompt, err)
	}
	if err := os.Symlink(filepath.Join(wf, "prompts", "x.md"), filepath.Join(wf, "prompts", "l.md")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"@../x.md", "@/etc/hosts", "@prompts/missing.md", "@prompts/l.md"} {
		e2eWrite(t, filepath.Join(wf, "workflow.json"), wfJSON(bad))
		if _, _, err := Lookup("w", home, "", false); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	errs := validateStructure(&Workflow{Name: "w", Description: "d", Start: "c", States: map[string]State{
		"c": {Check: "c.sh", Prompt: "x", On: map[string]string{"default": "end"}}, "end": {End: true},
	}})
	found := false
	for _, e := range errs {
		found = found || strings.Contains(e.Error(), "only allowed in an agent state")
	}
	if !found {
		t.Fatalf("errs = %v", errs)
	}
}

// A run uses the task template and the state prompt of its workflow directory (#372).
func TestRun_UsesWorkflowTaskAndStatePrompt(t *testing.T) {
	home := t.TempDir()
	wfDir := filepath.Join(home, ".tyci", "workflows", "w")
	e2eWrite(t, filepath.Join(wfDir, "prompts", "x.md"), "STATE PROMPT")
	e2eWrite(t, filepath.Join(wfDir, "tasks", "findings_to_issues.md"), "LOCAL FINDINGS {{.Repo}}")
	e2eWrite(t, filepath.Join(wfDir, "workflow.json"), `{"description":"d","start":"code","states":{
		"code":{"agent":"worker","prompt":"@prompts/x.md","on":{"default":"findings"}},
		"findings":{"agent":"review","task":"findings_to_issues","on":{"default":"end"}},
		"end":{"end":true}}}`)
	wf, _, err := Lookup("w", home, "", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &flowconfig.Config{DefaultModel: "p/m"}
	var specs []tools.TaskSpec
	agents := &SubagentRunner{Cfg: cfg, Render: TaskTemplates{Dir: wfDir},
		IssueContext: func(context.Context, string, int) (string, error) { return "ISSUE", nil },
		Spawn: func(_ context.Context, s tools.TaskSpec) (string, string, error) {
			specs = append(specs, s)
			return "done", "", nil
		}}
	st := &RunState{Run: "r", Repo: "o/r", Issue: 1, Worktree: t.TempDir()}
	if err := (&Runner{WF: wf, Agents: agents}).Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("specs = %d", len(specs))
	}
	if specs[0].SystemPrompt != "STATE PROMPT" || !strings.HasPrefix(specs[0].Task, "STATE PROMPT") {
		t.Fatalf("worker spec = %+v", specs[0])
	}
	if !strings.HasPrefix(specs[1].Task, "LOCAL FINDINGS o/r") {
		t.Fatalf("findings task = %q", specs[1].Task)
	}
	if want, _ := flowconfig.DefaultPrompt("review"); specs[1].SystemPrompt != want {
		t.Fatal("findings state lost the review role prompt")
	}
}

func TestRun_RolePromptFileBeatsConfigPrompt(t *testing.T) {
	home := t.TempDir()
	wfDir := filepath.Join(home, ".tyci", "workflows", "w")
	e2eWrite(t, filepath.Join(wfDir, "prompts", "worker.md"), "FILE")
	e2eWrite(t, filepath.Join(home, ".tyci", "config.json"),
		`{"default_model":"p/m","roles":{"worker":{"prompt":"CONFIG"},"review":{"prompt":"CONFIG REVIEW"}}}`)
	e2eWrite(t, filepath.Join(wfDir, "workflow.json"), `{"description":"d","start":"code","states":{
		"code":{"agent":"worker","on":{"default":"review"}},
		"review":{"agent":"review","on":{"default":"end"}},
		"end":{"end":true}}}`)
	wf, _, err := Lookup("w", home, "", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := flowconfig.Load(home, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var specs []tools.TaskSpec
	agents := &SubagentRunner{Cfg: cfg, Render: TaskTemplates{Dir: wfDir},
		IssueContext: func(context.Context, string, int) (string, error) { return "ISSUE", nil },
		Spawn: func(_ context.Context, s tools.TaskSpec) (string, string, error) {
			specs = append(specs, s)
			return "done", "", nil
		}}
	st := &RunState{Run: "r", Repo: "o/r", Issue: 1, Worktree: t.TempDir()}
	if err := (&Runner{WF: wf, Agents: agents}).Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("specs = %d", len(specs))
	}
	if specs[0].SystemPrompt != "FILE" {
		t.Fatalf("worker prompt = %q, want the role prompt file", specs[0].SystemPrompt)
	}
	if specs[1].SystemPrompt != "CONFIG REVIEW" {
		t.Fatalf("review prompt = %q, want the config prompt", specs[1].SystemPrompt)
	}
}

// #206: the milestone-close template is embedded, loads and validates with the oracle role.
func TestTemplate_MilestoneCloseParsesAndValidates(t *testing.T) {
	wf := initWF(t, t.TempDir(), "milestone-close", "milestone-close")
	cfg := &flowconfig.Config{
		Models: map[string]string{"m": "x://1"},
		Roles:  map[string]flowconfig.Role{"oracle": {Model: "m"}},
	}
	if _, err := Validate(wf, cfg, wfResolver(wf)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !wf.States["ask"].Human || wf.States["declined"].Result != "declined" {
		t.Fatalf("ask human or declined result missing: %+v", wf.States)
	}
}

func TestTemplatesValidate(t *testing.T) {
	names := Templates()
	for _, want := range []string{"dependency-bump", "fix-ci"} {
		if !slices.Contains(names, want) {
			t.Fatalf("Templates() = %v, lacks %q", names, want)
		}
	}
	for _, name := range names {
		paths, err := Init(name, "", t.TempDir())
		if err != nil {
			t.Fatalf("Init(%q): %v", name, err)
		}
		if len(paths) == 0 {
			t.Fatalf("Init(%q) wrote no files", name)
		}
	}
}
