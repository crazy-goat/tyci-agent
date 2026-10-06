package main

import (
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/jobs"
)

func TestWorkflowNotify_ReachesJobNotices(t *testing.T) {
	prev := JobNotices
	JobNotices = jobs.NewNotifier()
	defer func() { JobNotices = prev }()
	workflowNotify("workflow run 1 done: merged https://github.com/o/n/pull/171")
	got := JobNotices.Drain()
	if len(got) != 1 || !strings.Contains(got[0], "pull/171") {
		t.Fatalf("notices = %v", got)
	}
}
