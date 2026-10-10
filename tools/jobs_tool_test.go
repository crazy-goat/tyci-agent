package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// detailJob is a registered job with the detail fields the jobs tool lists.
type detailJob struct {
	id, parentID, kind, status, description, question string
	started                                           time.Time
}

func (j detailJob) ID() string           { return j.id }
func (j detailJob) ParentID() string     { return j.parentID }
func (j detailJob) Kind() string         { return j.kind }
func (j detailJob) Status() string       { return j.status }
func (j detailJob) Description() string  { return j.description }
func (j detailJob) StartedAt() time.Time { return j.started }
func (j detailJob) Question() string     { return j.question }

// runJobsTool calls the jobs tool and returns the ids of the rows it lists.
func runJobsTool(t *testing.T, ctx context.Context, input map[string]any) []string {
	t.Helper()
	res := (&JobsTool{}).Run(ctx, input)
	if !res.Success {
		t.Fatalf("jobs tool failed: %s", res.Error)
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(res.Content), &rows); err != nil {
		t.Fatalf("jobs output is not a JSON array: %v (%q)", err, res.Content)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func sameIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestJobsTool_ChatSeesAllJobs: the main agent (no subagent sink) lists every job.
// Revert check: without the JobsTool the call fails as an unknown tool.
func TestJobsTool_ChatSeesAllJobs(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		detailJob{id: "job-1", status: "running", kind: "bash"},
		detailJob{id: "job-2", parentID: "job-1", status: "running", kind: "subagent"},
		detailJob{id: "job-3", status: "waiting_answer", kind: "subagent"},
	}}
	withKillWiring(t, nil, lister)

	got := runJobsTool(t, context.Background(), nil)
	if len(got) != 3 {
		t.Fatalf("chat should see 3 jobs, got %v", got)
	}
}

// TestJobsTool_SubagentSeesOwnSubtreeOnly: a subagent lists only its own
// subtree. job-c is unrelated and must not appear.
func TestJobsTool_SubagentSeesOwnSubtreeOnly(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		detailJob{id: "job-a", status: "running", kind: "subagent"},
		detailJob{id: "job-b", parentID: "job-a", status: "running", kind: "bash"},
		detailJob{id: "job-c", status: "running", kind: "subagent"},
	}}
	withKillWiring(t, nil, lister)

	got := runJobsTool(t, childCtx("job-a"), nil)
	if !sameIDs(got, "job-a", "job-b") {
		t.Fatalf("subagent job-a should see job-a and job-b, got %v", got)
	}
}

// TestJobsTool_DefaultStatusIsLiveOnly: with no input only live jobs are listed.
func TestJobsTool_DefaultStatusIsLiveOnly(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		detailJob{id: "job-run", status: "running"},
		detailJob{id: "job-done", status: "done"},
	}}
	withKillWiring(t, nil, lister)

	got := runJobsTool(t, context.Background(), nil)
	if !sameIDs(got, "job-run") {
		t.Fatalf("default should list only the running job, got %v", got)
	}
}

// TestJobsTool_FilterByKindAndStatus: kind and status filters combine.
func TestJobsTool_FilterByKindAndStatus(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		detailJob{id: "job-bash-failed", kind: "bash", status: "failed"},
		detailJob{id: "job-sub-failed", kind: "subagent", status: "failed"},
		detailJob{id: "job-bash-run", kind: "bash", status: "running"},
	}}
	withKillWiring(t, nil, lister)

	got := runJobsTool(t, context.Background(), map[string]any{"kind": "bash", "status": "failed"})
	if !sameIDs(got, "job-bash-failed") {
		t.Fatalf("kind=bash status=failed should match one job, got %v", got)
	}
}

// TestJobsTool_AllIncludesFinished: all=true lists finished jobs too.
func TestJobsTool_AllIncludesFinished(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		detailJob{id: "job-run", status: "running"},
		detailJob{id: "job-done", status: "done"},
	}}
	withKillWiring(t, nil, lister)

	got := runJobsTool(t, context.Background(), map[string]any{"all": true})
	if !sameIDs(got, "job-run", "job-done") {
		t.Fatalf("all=true should list both jobs, got %v", got)
	}
}

// TestJobsTool_EmptyListIsJSONArray: no jobs gives "[]", not "null".
func TestJobsTool_EmptyListIsJSONArray(t *testing.T) {
	withKillWiring(t, nil, &fakeLister{})

	res := (&JobsTool{}).Run(context.Background(), nil)
	if !res.Success || res.Content != "[]" {
		t.Fatalf("empty list should be \"[]\", got success=%v content=%q", res.Success, res.Content)
	}
}

// TestKillJob_UnknownIdListsLiveJobs: an unknown id names the live jobs the
// main agent may stop. Revert check: without liveJobsHint the text has no
// "live jobs" part.
func TestKillJob_UnknownIdListsLiveJobs(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		detailJob{id: "job-7", status: "running", kind: "bash", description: "sleep"},
		detailJob{id: "job-8", status: "done", kind: "bash", description: "old"},
	}}
	withKillWiring(t, &fakeCanceler{fail: true}, lister)

	res := (&KillJobTool{}).Run(context.Background(), map[string]any{"job_id": "nope"})
	if res.Success {
		t.Fatal("expected failure for an unknown id")
	}
	if !strings.Contains(res.Error, "live jobs") || !strings.Contains(res.Error, "7 (bash, sleep)") {
		t.Errorf("error should list the live job, got %q", res.Error)
	}
	if strings.Contains(res.Error, "old") {
		t.Errorf("error should not list a finished job, got %q", res.Error)
	}
}

// TestKillJob_FinishedJobInsideChildListsOnlyOwnSubtree: a subagent asking to
// kill a finished job in its subtree gets the live jobs of its own subtree
// only. job-c is unrelated and must not be named.
func TestKillJob_FinishedJobInsideChildListsOnlyOwnSubtree(t *testing.T) {
	lister := &fakeLister{jobs: []JobKindSource{
		detailJob{id: "job-a", status: "running", kind: "subagent", description: "parent-work"},
		detailJob{id: "job-b", parentID: "job-a", status: "done", kind: "bash", description: "finished-cmd"},
		detailJob{id: "job-c", status: "running", kind: "subagent", description: "unrelated-work"},
	}}
	withKillWiring(t, &fakeCanceler{fail: true}, lister)

	res := (&KillJobTool{}).Run(childCtx("job-a"), map[string]any{"job_id": "job-b"})
	if res.Success {
		t.Fatal("expected failure for a finished job")
	}
	if !strings.Contains(res.Error, "parent-work") {
		t.Errorf("error should list the child's own live job, got %q", res.Error)
	}
	if strings.Contains(res.Error, "unrelated-work") {
		t.Errorf("error must not list a job outside the child's subtree, got %q", res.Error)
	}
}
