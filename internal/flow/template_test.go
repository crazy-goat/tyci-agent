package flow

import (
	"context"
	"os"
	"path/filepath"
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
	for _, rel := range []string{"workflow.json", "checks/merge.sh", "tasks/fixer.md", "tasks/recover.md",
		"tasks/findings_to_issues.md", "prompts/worker.md", "prompts/oracle.md"} {
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

func TestInit_NameTemplateAndBadInput(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init("roadmap", "plan", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tyci", "workflows", "plan", "workflow.json")); err != nil {
		t.Fatal(err)
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
