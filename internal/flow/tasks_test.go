package flow

import (
	"strings"
	"testing"
)

var testTaskData = TaskData{
	Repo: "o/r", Branch: "feat/b", DefaultBranch: "main", Worktree: "/wt/x",
	RunDir: "/run/y", Reason: "boom-reason", Issue: 7, PR: 42, Visit: 3,
	Failed: "upd", FailedKey: "fail", FailedDir: "/run/y/artifacts/005-upd",
}

func TestTasks_Render(t *testing.T) {
	cases := map[string][]string{
		"findings_to_issues": {"o/r", "/wt/x", "Run so far", "#42"},
		"fixer":              {"`upd`", "`fail`", "/run/y/artifacts/005-upd/output.log", "#42", "`ok`", "`failed`", "failed.log"},
		"recover":            {"`upd`", "`fail`", "/run/y/artifacts/005-upd/output.log", "goto:<state>", "`stop`", "ask <reason>"},
	}
	for name, want := range cases {
		out, err := RenderTask(name, testTaskData)
		if err != nil {
			t.Fatal(name, err)
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q", name, w)
			}
		}
	}
}

func TestTasks_UnknownTask(t *testing.T) {
	if _, err := RenderTask("nope", testTaskData); err == nil {
		t.Fatal("want error")
	}
}

func TestTasks_UnknownFieldFails(t *testing.T) {
	if _, err := renderTaskText("x", "{{.Secret}}", testTaskData); err == nil {
		t.Fatal("want error")
	}
}

func TestTasks_NoFunctionsAllowed(t *testing.T) {
	for _, s := range []string{"{{call .Repo}}", "{{env \"HOME\"}}", "{{readFile \"x\"}}"} {
		if _, err := renderTaskText("x", s, testTaskData); err == nil {
			t.Errorf("%s: want error", s)
		}
	}
	for _, n := range []string{"findings_to_issues", "fixer", "recover"} {
		b, _ := embedded.ReadFile("tasks/" + n + ".md")
		for _, f := range []string{"{{call", "{{printf", "{{env", "{{exec"} {
			if strings.Contains(string(b), f) {
				t.Errorf("%s uses %s", n, f)
			}
		}
	}
}

func TestFindingsTask_NeverLines(t *testing.T) {
	out, _ := RenderTask("findings_to_issues", testTaskData)
	for _, l := range strings.Split(out, "\n") {
		if (strings.Contains(l, "--milestone") || strings.Contains(l, "accepted")) && !strings.Contains(l, "NEVER") {
			t.Errorf("line without NEVER: %q", l)
		}
	}
}

func TestFindingsTask_CoversDuplicateSearchAndComment(t *testing.T) {
	out, _ := RenderTask("findings_to_issues", testTaskData)
	for _, w := range []string{"gh issue list", "gh issue comment", "gh issue create"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q", w)
		}
	}
}

func TestFindingsTask_EndsWithDone(t *testing.T) {
	out, _ := RenderTask("findings_to_issues", testTaskData)
	if !strings.HasSuffix(strings.TrimSpace(out), "`done`.") {
		t.Errorf("bad ending: %q", out)
	}
}

func TestTaskTemplatesRenderPassesFailedStep(t *testing.T) {
	out, err := TaskTemplates{}.Render("fixer", RunContext{Failed: "merge", FailedKey: "fail", FailedDir: "/r/artifacts/009-merge"})
	if err != nil || !strings.Contains(out, "`merge` of the run") || !strings.Contains(out, "/r/artifacts/009-merge/output.log") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestTaskTemplatesRenderPassesInput(t *testing.T) {
	out, err := TaskTemplates{}.Render("roadmap", RunContext{Input: `{"issues":[]}`})
	if err != nil || !strings.Contains(out, `{"issues":[]}`) {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}
