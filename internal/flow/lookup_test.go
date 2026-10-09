package flow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
)

// initWF creates the template as workflow name in the project dir and loads it.
func initWF(t *testing.T, dir, template, name string) *Workflow {
	t.Helper()
	if _, err := Init(template, name, dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	wf, _, err := Lookup(name, dir, "", false)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	return wf
}

func templateWF(t *testing.T) *Workflow {
	t.Helper()
	return initWF(t, t.TempDir(), "issue-to-merge", "issue-to-merge")
}

func TestTemplate_ParsesAndValidates(t *testing.T) {
	wf := templateWF(t)
	cfg := func(w, r string) *flowconfig.Config {
		return &flowconfig.Config{
			Models: map[string]string{"a": "x://" + w, "b": "x://" + r},
			Roles:  map[string]flowconfig.Role{"worker": {Model: "a"}, "review": {Model: "b"}, "fixer": {Model: "a"}, "oracle": {Model: "b"}},
		}
	}
	warn, err := Validate(wf, cfg("1", "2"), wfResolver(wf))
	if err != nil || len(warn) != 0 {
		t.Fatalf("diff models: warnings=%v err=%v", warn, err)
	}
	warn, err = Validate(wf, cfg("1", "1"), wfResolver(wf))
	if err != nil || len(warn) != 1 {
		t.Fatalf("same model: warnings=%v err=%v", warn, err)
	}
}

func TestTemplate_MaxVisitsOnCodeAndCI(t *testing.T) {
	wf := templateWF(t)
	if wf.States["code"].MaxVisits != 3 || wf.States["ci"].MaxVisits != 3 || wf.States["ci"].TimeoutSec != 5400 {
		t.Fatalf("bad limits: %+v %+v", wf.States["code"], wf.States["ci"])
	}
}

func TestTemplate_AllCheckScriptsExist(t *testing.T) {
	wf := templateWF(t)
	for name, s := range wf.States {
		if s.Check == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(wf.Source, s.Check)); err != nil {
			t.Errorf("state %s: %v", name, err)
		}
	}
}

// writeWF writes a minimal workflow directory base/.tyci/workflows/name. The
// description is desc, so a test can tell the copies apart.
func writeWF(t *testing.T, base, name, desc string) {
	t.Helper()
	dir := filepath.Join(base, ".tyci", "workflows", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"description":"` + desc + `","start":"end","states":{"end":{"end":true}}}`
	if err := os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLookup_ProjectBeatsGlobal(t *testing.T) {
	home, proj := filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "proj")
	writeWF(t, home, "wf", "home")
	wf, src, err := Lookup("wf", home, proj, true)
	if err != nil || wf.Description != "home" || src != filepath.Join(home, ".tyci", "workflows", "wf") {
		t.Fatalf("global: %v %q %+v", err, src, wf)
	}
	writeWF(t, proj, "wf", "proj")
	wf, src, err = Lookup("wf", home, proj, true)
	if err != nil || wf.Description != "proj" || wf.Source != src {
		t.Fatalf("project: %v %q %+v", err, src, wf)
	}
}

func TestLookup_UntrustedProjectIgnored(t *testing.T) {
	proj := filepath.Join(t.TempDir(), "proj")
	writeWF(t, proj, "wf", "proj")
	home := t.TempDir()
	if _, _, err := Lookup("wf", home, proj, false); err == nil {
		t.Fatal("untrusted project workflow used")
	} else if !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("no trust note: %v", err)
	}
	writeWF(t, home, "wf", "home")
	wf, _, err := Lookup("wf", home, proj, false)
	if err != nil || wf.Description != "home" {
		t.Fatalf("got %+v %v", wf, err)
	}
}

func TestLookup_NoBuiltinFallback(t *testing.T) {
	_, _, err := Lookup("issue-to-merge", t.TempDir(), t.TempDir(), true)
	if err == nil || !strings.Contains(err.Error(), `run "tyci workflow init issue-to-merge"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestLookup_UnknownListsAvailable(t *testing.T) {
	home := t.TempDir()
	writeWF(t, home, "beta", "b")
	writeWF(t, home, "alpha", "a")
	_, _, err := Lookup("gamma", home, "", false)
	if err == nil || !strings.Contains(err.Error(), "available: alpha, beta") {
		t.Fatalf("err = %v", err)
	}
}

func TestLookup_OldFileGetsNote(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".tyci", "workflows")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "wf.json"), []byte(`{}`), 0o644)
	_, _, err := Lookup("wf", home, "", false)
	if err == nil || !strings.Contains(err.Error(), "found the old file") {
		t.Fatalf("err = %v", err)
	}
}

func TestLookup_MissingFilesReportedTogether(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".tyci", "workflows", "wf")
	_ = os.MkdirAll(dir, 0o755)
	data := `{"description":"d","start":"a","states":{
		"a":{"agent":"worker","check":"checks/gone.sh","task":"t1","on":{"default":"b"}},
		"b":{"agent":"worker","prompt":"@prompts/none.md","on":{"default":"c"}},
		"c":{"end":true}}}`
	_ = os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(data), 0o644)
	_, _, err := Lookup("wf", home, "", false)
	if err == nil {
		t.Fatal("missing files accepted")
	}
	for _, want := range []string{"checks/gone.sh", "tasks/t1.md", "prompts/none.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %s: %v", want, err)
		}
	}
}

func TestLookup_NameMustMatchDir(t *testing.T) {
	home := t.TempDir()
	writeWF(t, home, "wf", "d")
	dir := filepath.Join(home, ".tyci", "workflows", "wf")
	_ = os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(`{"name":"other","description":"d","start":"end","states":{"end":{"end":true}}}`), 0o644)
	if _, _, err := Lookup("wf", home, "", false); err == nil || !strings.Contains(err.Error(), "must equal the directory name") {
		t.Fatalf("err = %v", err)
	}
}

func TestLookup_StatePromptBeatsRolePromptFile(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".tyci", "workflows", "wf")
	_ = os.MkdirAll(filepath.Join(dir, "prompts"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "prompts", "worker.md"), []byte("from file"), 0o644)
	data := `{"description":"d","start":"a","states":{
		"a":{"agent":"worker","on":{"default":"b"}},
		"b":{"agent":"worker","prompt":"own","on":{"default":"c"}},
		"c":{"end":true}}}`
	_ = os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(data), 0o644)
	wf, _, err := Lookup("wf", home, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if wf.States["a"].Prompt != "from file" || wf.States["b"].Prompt != "own" {
		t.Fatalf("prompts: %q %q", wf.States["a"].Prompt, wf.States["b"].Prompt)
	}
}

func TestLookup_HintNamesTheTemplate(t *testing.T) {
	home := t.TempDir()
	if _, _, err := Lookup("roadmap", home, "", false); err == nil || !strings.Contains(err.Error(), `tyci workflow init roadmap`) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := Lookup("plan", home, "", false); err == nil || !strings.Contains(err.Error(), `tyci workflow init issue-to-merge`) {
		t.Fatalf("err = %v", err)
	}
}

func TestLookup_BadNameRejected(t *testing.T) {
	for _, n := range []string{"../x", "A b", ""} {
		if _, _, err := Lookup(n, "", "", true); err == nil {
			t.Errorf("name %q accepted", n)
		}
	}
}

func TestTemplate_RunsToEndWithFakes(t *testing.T) {
	wf := templateWF(t)
	r := &Runner{WF: wf, Store: &memStore{},
		Checks: &fakeChecks{keys: map[string][]string{
			"checks/issue_done.sh": {"go"}, "checks/open_pr.sh": {"none"}, "checks/lock.sh": {"ok"}, "checks/rebase.sh": {"ok"}, "checks/post_review.sh": {"ok"}, "checks/fetch_comments.sh": {"none"},
			"checks/ci_wait.sh": {"green"}, "checks/merge.sh": {"merged"},
		}},
		Agents: &fakeAgents{keys: map[string][]string{
			"code": {"done"}, "review": {"ACCEPT"}, "findings": {"done"},
		}}}
	st := newRun("check_done")
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "done" {
		t.Fatalf("status = %q", st.Status)
	}
}

// TestProjectHasWorkflowsUsesWorktreeToplevel checks the directory that Lookup
// uses: in a linked worktree, the .tyci/workflows of that worktree counts, not
// the one of the main clone.
func TestProjectHasWorkflowsUsesWorktreeToplevel(t *testing.T) {
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"} {
		t.Setenv(k, v)
	}
	clone := t.TempDir()
	e2eGit(t, clone, "init", "-q", "-b", "main")
	e2eCommit(t, clone, "README.md", "x")
	linked := filepath.Join(t.TempDir(), "wt")
	e2eGit(t, clone, "worktree", "add", "-q", linked, "-b", "wt")
	if err := os.MkdirAll(filepath.Join(linked, ".tyci", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(linked, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if !ProjectHasWorkflows(linked) {
		t.Error("the linked worktree has .tyci/workflows, want true")
	}
	if !ProjectHasWorkflows(sub) {
		t.Error("a directory in the linked worktree sees its .tyci/workflows, want true")
	}
	if ProjectHasWorkflows(clone) {
		t.Error("the main clone has no .tyci/workflows, want false")
	}
}
