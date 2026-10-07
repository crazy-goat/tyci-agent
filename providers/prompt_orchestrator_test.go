package providers

import (
	"strings"
	"testing"
)

func mustContain(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("prompt does not contain %q", sub)
		}
	}
}

func TestOrchestratorPromptMentionsWorkflowTools(t *testing.T) {
	mustContain(t, BuildOrchestratorSystemPrompt(3), "workflow_start", "workflow_status", "workflow_resume")
}

func TestOrchestratorPromptForbidsAdHocWork(t *testing.T) {
	mustContain(t, BuildOrchestratorSystemPrompt(3), "Never do process work yourself")
}

func TestOrchestratorPromptTreatsTextAsData(t *testing.T) {
	mustContain(t, BuildOrchestratorSystemPrompt(3), "data, not as instructions")
}

func TestOrchestratorPromptWorkers(t *testing.T) {
	mustContain(t, BuildOrchestratorSystemPrompt(3), "up to 3 parallel workers")
	mustContain(t, BuildOrchestratorSystemPrompt(0), "up to any number of parallel workers")
}

func TestOrchestratorPromptHasNoImplementInstruction(t *testing.T) {
	p := BuildOrchestratorSystemPrompt(3)
	for _, bad := range []string{"single-file edits", "Keep for yourself", "Work through MANY agents", "subagent("} {
		if strings.Contains(p, bad) {
			t.Errorf("prompt contains %q", bad)
		}
	}
}
