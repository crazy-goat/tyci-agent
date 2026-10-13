package jobs

import (
	"context"
	"testing"
	"time"
)

// TestJob_RunIDFromContext checks that Start copies the run ID of the context
// to Job.RunID, and that an ad-hoc job (no run ID in the context) gets "".
func TestJob_RunIDFromContext(t *testing.T) {
	r := NewRegistry(nil)
	fn := func(context.Context, string) (string, bool, error) { return "", false, nil }

	inRun := r.Start(context.WithValue(context.Background(), RunIDCtxKey{}, "#7"), "a", KindSubagent, "", fn)
	adHoc := r.Start(context.Background(), "b", KindSubagent, "", fn)
	// Wait for the jobs to finish, so that Snapshot does not race with the
	// goroutine that finishes them.
	r.Wait(context.Background(), inRun.ID, time.Second)
	r.Wait(context.Background(), adHoc.ID, time.Second)

	if inRun.RunID != "#7" {
		t.Fatalf("RunID = %q, want #7", inRun.RunID)
	}
	if adHoc.RunID != "" {
		t.Fatalf("ad-hoc RunID = %q, want empty", adHoc.RunID)
	}
	if snap := inRun.Snapshot(); snap.RunID != "#7" {
		t.Fatalf("snapshot RunID = %q, want #7", snap.RunID)
	}
}
