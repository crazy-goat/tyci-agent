package main

import (
	"strings"
	"testing"
)

func TestWorkflowNotify_DeliveredOnce_ToOrchestrator(t *testing.T) {
	withTestWiring(t)
	workflowNotify("workflow run 1 done: merged https://github.com/o/n/pull/171")
	got := drainNotices()
	if len(got) != 1 || !strings.Contains(got[0], "pull/171") {
		t.Fatalf("notices = %v", got)
	}
	if again := drainNotices(); len(again) != 0 {
		t.Fatalf("second drain = %v, want nothing", again)
	}
}
