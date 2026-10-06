package flow

import (
	"strings"
	"testing"
)

var testTaskData = TaskData{
	Repo: "o/r", Branch: "feat/b", DefaultBranch: "main", Worktree: "/wt/x",
	RunDir: "/run/y", Reason: "boom-reason", Issue: 7, PR: 42, Visit: 3,
}

func TestTasks_Render(t *testing.T) {
	cases := map[string][]string{
		"findings_to_issues": {"o/r", "/wt/x", "/run/y", "#42"},
		"merge_decision":     {"feat/b", "main", "boom-reason", "#42"},
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
	for _, n := range []string{"findings_to_issues", "merge_decision"} {
		b, _ := taskFS.ReadFile("tasks/" + n + ".md")
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

func TestMergeDecisionTask_ContainsReason(t *testing.T) {
	out, _ := RenderTask("merge_decision", testTaskData)
	if !strings.Contains(out, "```\nboom-reason\n```") {
		t.Error("reason not in fenced block")
	}
	for _, w := range []string{"retry", "code", "ask"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q", w)
		}
	}
}

func TestMergeDecisionTask_ReasonIsMasked(t *testing.T) {
	tok := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	out, err := TaskTemplates{}.Render("merge_decision", RunContext{PR: 1, Reason: "fail " + tok})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, tok) {
		t.Fatal("token leaked")
	}
}
