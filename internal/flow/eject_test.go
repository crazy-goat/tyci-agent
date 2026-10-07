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

func TestEject_WritesFilesAndLookupUsesThem(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	e2eWrite(t, filepath.Join(dir, ".tyci", "config.json"), `{"models":{"m":"p/m"},"roles":{"worker":{"model":"m"}}}`)
	written, err := Eject("issue-to-merge", dir, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"workflows/issue-to-merge.json", "checks/describe.sh", "checks/merge.sh",
		"tasks/fixer.md", "tasks/recover.md", "tasks/findings_to_issues.md", "prompts/worker.md", "prompts/oracle.md", "config.json"} {
		p := filepath.Join(dir, ".tyci", rel)
		if !strings.Contains(strings.Join(written, "\n"), p) {
			t.Errorf("%s not in written list", rel)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, ".tyci", "checks", "merge.sh")); err != nil || st.Mode()&0o100 == 0 {
		t.Fatalf("merge.sh not executable: %v", err)
	}

	wf, src, err := Lookup("issue-to-merge", home, dir, true)
	if err != nil || src != filepath.Join(dir, ".tyci", "workflows", "issue-to-merge.json") || wf.Source != src {
		t.Fatalf("lookup: %q %q %v", src, wf.Source, err)
	}
	cfg, err := flowconfig.Load(home, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	role, _ := cfg.Role("worker")
	want, _ := flowconfig.DefaultPrompt("worker")
	if role.Model != "m" || role.Prompt != want {
		t.Fatalf("worker role = %+v", role)
	}

	if _, err := Eject("issue-to-merge", dir, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second eject: %v", err)
	}
	e2eWrite(t, filepath.Join(dir, ".tyci", "tasks", "fixer.md"), "mine")
	if _, err := Eject("issue-to-merge", dir, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".tyci", "tasks", "fixer.md")); string(b) == "mine" {
		t.Fatal("--force did not overwrite")
	}
}

func TestEject_RefusesOtherRolePrompt(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".tyci", "config.json")
	e2eWrite(t, cfg, `{"roles":{"worker":{"prompt":"custom"}}}`)
	if _, err := Eject("issue-to-merge", dir, false); err == nil || !strings.Contains(err.Error(), "roles.worker.prompt") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tyci", "workflows")); err == nil {
		t.Fatal("a refused eject wrote files")
	}
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), "custom") {
		t.Fatalf("config changed: %s", b)
	}
	if _, err := Eject("nope", dir, false); err == nil {
		t.Fatal("unknown workflow accepted")
	}
}

func TestTaskTemplates_LocalThenHomeThenEmbedded(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	tt := TaskTemplates{Dirs: []string{repo, home}}
	rc := RunContext{Repo: "o/r"}
	out, err := tt.Render("findings_to_issues", rc)
	if err != nil || strings.HasPrefix(out, "HOME") || strings.HasPrefix(out, "LOCAL") {
		t.Fatalf("embedded: %q %v", out, err)
	}
	e2eWrite(t, filepath.Join(home, ".tyci", "tasks", "findings_to_issues.md"), "HOME {{.Repo}}")
	if out, _ = tt.Render("findings_to_issues", rc); out != "HOME o/r" {
		t.Fatalf("home: %q", out)
	}
	e2eWrite(t, filepath.Join(repo, ".tyci", "tasks", "findings_to_issues.md"), "LOCAL {{.Repo}}")
	if out, _ = tt.Render("findings_to_issues", rc); out != "LOCAL o/r" {
		t.Fatalf("local: %q", out)
	}
	if out, _ = (TaskTemplates{Dirs: []string{"", home}}).Render("findings_to_issues", rc); out != "HOME o/r" {
		t.Fatalf("untrusted repo: %q", out)
	}
	if _, err := tt.Render("../x", rc); err == nil {
		t.Fatal("bad task name accepted")
	}
	link := filepath.Join(repo, ".tyci", "tasks", "fixer.md")
	if err := os.Symlink(filepath.Join(home, ".tyci", "tasks", "findings_to_issues.md"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := tt.Render("fixer", rc); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink: %v", err)
	}
}

func TestLookup_StatePromptFile(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	wfJSON := func(prompt string) string {
		return `{"name":"w","start":"code","states":{"code":{"agent":"worker","prompt":"` + prompt + `","on":{"default":"end"}},"end":{"end":true}}}`
	}
	e2eWrite(t, filepath.Join(dir, ".tyci", "prompts", "x.md"), "STATE PROMPT")
	e2eWrite(t, filepath.Join(dir, ".tyci", "workflows", "w.json"), wfJSON("@prompts/x.md"))
	wf, _, err := Lookup("w", home, dir, true)
	if err != nil || wf.States["code"].Prompt != "STATE PROMPT" {
		t.Fatalf("prompt = %q, %v", wf.States["code"].Prompt, err)
	}
	if err := os.Symlink(filepath.Join(dir, ".tyci", "prompts", "x.md"), filepath.Join(dir, ".tyci", "prompts", "l.md")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"@../x.md", "@/etc/hosts", "@prompts/missing.md", "@prompts/l.md"} {
		e2eWrite(t, filepath.Join(dir, ".tyci", "workflows", "w.json"), wfJSON(bad))
		if _, _, err := Lookup("w", home, dir, true); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	errs := validateStructure(&Workflow{Name: "w", Start: "c", States: map[string]State{
		"c": {Check: "c.sh", Prompt: "x", On: map[string]string{"default": "end"}}, "end": {End: true},
	}})
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "only allowed in an agent state") {
		t.Fatalf("errs = %v", errs)
	}
}

// A run uses a local task template and a state prompt (#372).
func TestRun_UsesLocalTaskAndStatePrompt(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	e2eWrite(t, filepath.Join(dir, ".tyci", "prompts", "x.md"), "STATE PROMPT")
	e2eWrite(t, filepath.Join(dir, ".tyci", "tasks", "findings_to_issues.md"), "LOCAL FINDINGS {{.Repo}}")
	e2eWrite(t, filepath.Join(dir, ".tyci", "workflows", "w.json"), `{"name":"w","start":"code","states":{
		"code":{"agent":"worker","prompt":"@prompts/x.md","on":{"default":"findings"}},
		"findings":{"agent":"review","task":"findings_to_issues","on":{"default":"end"}},
		"end":{"end":true}}}`)
	wf, _, err := Lookup("w", home, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &flowconfig.Config{DefaultModel: "p/m"}
	var specs []tools.TaskSpec
	agents := &SubagentRunner{Cfg: cfg, Render: TaskTemplates{Dirs: []string{dir, home}},
		IssueContext: func(context.Context, string, int) (string, error) { return "ISSUE", nil },
		Spawn: func(_ context.Context, s tools.TaskSpec) (string, string, error) {
			specs = append(specs, s)
			return "done", "", nil
		}}
	st := &RunState{Run: "r", Repo: "o/r", Issue: 1, Worktree: dir}
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

func TestEjectMissing_KeepsExistingFilesAndPrompts(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".tyci", "config.json")
	e2eWrite(t, cfg, `{"roles":{"worker":{"prompt":"custom"}}}`)
	merge := filepath.Join(dir, ".tyci", "checks", "merge.sh")
	e2eWrite(t, merge, "mine")
	written, err := EjectMissing("issue-to-merge", dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(written, "\n"), merge) {
		t.Fatal("existing merge.sh in the written list")
	}
	if b, _ := os.ReadFile(merge); string(b) != "mine" {
		t.Fatalf("merge.sh overwritten: %s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tyci", "workflows", "issue-to-merge.json")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(cfg)
	if !strings.Contains(string(b), `"custom"`) || !strings.Contains(string(b), "@prompts/oracle.md") {
		t.Fatalf("config = %s", b)
	}
}
