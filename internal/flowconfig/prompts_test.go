package flowconfig

import (
	"strings"
	"testing"
)

const progressNote = "Before using tools, write 1-2 sentences saying what you are about to do."

func prompt(t *testing.T, role string) string {
	t.Helper()
	p, ok := defaultPrompt(role)
	if !ok || p == "" {
		t.Fatalf("no embedded prompt for %q", role)
	}
	return p
}

func requireAll(t *testing.T, role string, phrases ...string) {
	t.Helper()
	p := prompt(t, role)
	for _, s := range phrases {
		if !strings.Contains(p, s) {
			t.Errorf("%s prompt lacks %q", role, s)
		}
	}
}

func TestPrompts_Embedded(t *testing.T) {
	for _, r := range []string{"worker", "review", "merge_decision"} {
		prompt(t, r)
	}
}

func TestPrompts_ReviewMentionsVerdictFormat(t *testing.T) {
	requireAll(t, "review", "ACCEPT", "CHANGES", "report.md")
}

func TestPrompts_ReviewAllowsTaskCommands(t *testing.T) {
	requireAll(t, "review", "The task text may allow specific commands")
}

func TestPrompts_WorkerForbidsPush(t *testing.T) {
	requireAll(t, "worker", "Commit but do not push.")
}

func TestPrompts_WorkerWritesFindings(t *testing.T) {
	requireAll(t, "worker", "findings.md")
}

func TestPrompts_AllRequireProgressNote(t *testing.T) {
	for _, r := range []string{"worker", "review", "merge_decision"} {
		requireAll(t, r, progressNote)
	}
}

func TestPrompts_AllForbidQuestions(t *testing.T) {
	for _, r := range []string{"worker", "review", "merge_decision"} {
		requireAll(t, r, "Do not ask questions.")
	}
}

// #340: every role must leave report.md; no prompt names $TYCI_RUN_DIR.
func TestPrompts_AllRequireReport(t *testing.T) {
	for _, r := range []string{"worker", "review", "merge_decision", "oracle"} {
		requireAll(t, r, "MUST write `report.md`")
		if strings.Contains(prompt(t, r), "TYCI_RUN_DIR") {
			t.Errorf("%s prompt mentions TYCI_RUN_DIR", r)
		}
	}
	requireAll(t, "worker", "If the run so far shows red CI, CHANGES, a conflict or new comments, fix that first.")
}

func TestPrompts_MergeDecisionOneWord(t *testing.T) {
	requireAll(t, "merge_decision", "`retry`", "`code`", "`ask`")
}

func TestRole_DefaultPromptUsedWhenConfigEmpty(t *testing.T) {
	c := &Config{Roles: map[string]Role{"worker": {Model: "m"}}}
	r, err := c.Role("worker")
	if err != nil || r.Prompt != prompt(t, "worker") || r.Model != "m" {
		t.Fatalf("got %+v, %v", r, err)
	}
	r, err = (&Config{}).Role("review")
	if err != nil || r.Prompt != prompt(t, "review") {
		t.Fatalf("got %+v, %v", r, err)
	}
}

func TestRole_ConfigPromptOverridesEmbedded(t *testing.T) {
	c := &Config{Roles: map[string]Role{"worker": {Prompt: "mine"}}}
	r, err := c.Role("worker")
	if err != nil || r.Prompt != "mine" {
		t.Fatalf("got %+v, %v", r, err)
	}
}
