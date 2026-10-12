package main

// B1: a resumed job used to drain the WRONG (dead) mailbox, because
// jobResumerAdapter.Resume (btw.go) reused the stashed resumableEntry's
// agent.Config verbatim — including its NextMessages closure, which
// tools.JobMailboxNextMessages had bound to the ORIGINAL job id, not the
// brand-new one Resume actually starts. A message posted to the new job_id
// (via the "message" tool) landed in the registry's mailbox for that new
// id, but the resumed agent loop kept draining the terminal original job's
// (now-empty-forever) mailbox instead — so "message" reported success while
// the child never actually saw it.
//
// These tests drive the fix through the REAL production wiring
// (withTestWiring, exactly like wiring_test.go), the same way the M1 test
// there proves ordinary mailbox delivery to a running async subagent.

import (
	"encoding/json"
	"fmt"
	"testing"
)

// resumeJobID extracts the "job_id" field the "resume" tool's result JSON
// carries on its first line (see ResumeTool.Run).
func resumeJobID(t *testing.T, content string) string {
	t.Helper()
	id, err := parseResumeJobID(content)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return id
}

// parseResumeJobID is resumeJobID's non-fatal half: safe to call from a
// goroutine OTHER than the test's own (t.Fatal/t.Fatalf from a non-test
// goroutine calls runtime.Goexit, which silently kills that goroutine
// without ever sending on a result channel a WaitGroup/select elsewhere is
// blocked on — turning a real parse failure into a test hang instead of a
// reported failure). Callers running off the test goroutine must report
// this error over a channel themselves; see
// TestWiring_B1_ConcurrentResumesOfSameJobDoNotCrossTalk's launch closure.
func parseResumeJobID(content string) (string, error) {
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(firstLineOf(content)), &out); err != nil {
		return "", fmt.Errorf("unmarshal resume result %q: %w", content, err)
	}
	if out.JobID == "" {
		return "", fmt.Errorf("resume result carried no job_id: %q", content)
	}
	return out.JobID, nil
}
