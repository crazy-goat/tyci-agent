package jobs

import (
	"context"
	"testing"
	"time"
)

// TestJob_TranscriptFromContext checks that Start copies the transcript path
// of the context to Job.Transcript, and that a job started by that job gets
// no transcript path of its own.
func TestJob_TranscriptFromContext(t *testing.T) {
	r := NewRegistry(nil)
	var child *Job
	fn := func(ctx context.Context, _ string) (string, bool, error) {
		child = r.Start(ctx, "child", KindSubagent, "", func(context.Context, string) (string, bool, error) {
			return "", false, nil
		})
		r.Wait(context.Background(), child.ID, time.Second)
		return "", false, nil
	}

	parent := r.Start(context.WithValue(context.Background(), TranscriptCtxKey{}, "/tmp/a.jsonl"), "parent", KindSubagent, "", fn)
	r.Wait(context.Background(), parent.ID, time.Second)

	if parent.Transcript != "/tmp/a.jsonl" {
		t.Fatalf("Transcript = %q, want /tmp/a.jsonl", parent.Transcript)
	}
	if snap := parent.Snapshot(); snap.Transcript != "/tmp/a.jsonl" {
		t.Fatalf("snapshot Transcript = %q, want /tmp/a.jsonl", snap.Transcript)
	}
	if child.Transcript != "" {
		t.Fatalf("child Transcript = %q, want empty", child.Transcript)
	}
}
