package tools

import (
	"testing"
	"time"
)

// fakeJobProgressHeartbeat is a minimal JobProgressHeartbeat for exercising
// JobProgressHeartbeatCheck without a real jobs.Registry.
type fakeJobProgressHeartbeat struct {
	calls []struct {
		id    string
		after time.Duration
	}
	result bool
}

func (f *fakeJobProgressHeartbeat) NeedsProgressHeartbeat(id string, after time.Duration) bool {
	f.calls = append(f.calls, struct {
		id    string
		after time.Duration
	}{id, after})
	return f.result
}

func withFakeProgressHeartbeat(t *testing.T, f *fakeJobProgressHeartbeat) {
	old := getJobProgressHeartbeat()
	t.Cleanup(func() { SetJobProgressHeartbeat(old) })
	SetJobProgressHeartbeat(f)
}

// TestJobProgressHeartbeatCheck_NilWithoutWiringOrJobID pins the two
// harmless-no-op cases: nothing wired, and an empty job id (a call site with
// no job at all, e.g. a test calling run() directly outside any job).
func TestJobProgressHeartbeatCheck_NilWithoutWiringOrJobID(t *testing.T) {
	old := getJobProgressHeartbeat()
	SetJobProgressHeartbeat(nil)
	t.Cleanup(func() { SetJobProgressHeartbeat(old) })

	if check := JobProgressHeartbeatCheck("job-1-1"); check != nil {
		t.Fatal("expected nil callback when no JobProgressHeartbeat is wired")
	}

	withFakeProgressHeartbeat(t, &fakeJobProgressHeartbeat{})
	if check := JobProgressHeartbeatCheck(""); check != nil {
		t.Fatal("expected nil callback for an empty job id")
	}
}

// TestJobProgressHeartbeatCheck_DelegatesToWiredHeartbeat pins the happy
// path: the returned closure calls through to the wired
// JobProgressHeartbeat with exactly the bound job id and the current
// SubagentBackgroundAfterSec threshold, and returns whatever it reports.
func TestJobProgressHeartbeatCheck_DelegatesToWiredHeartbeat(t *testing.T) {
	restore := SetSubagentBackgroundAfterSecForTests(5 * time.Second)
	defer restore()

	fake := &fakeJobProgressHeartbeat{result: true}
	withFakeProgressHeartbeat(t, fake)

	check := JobProgressHeartbeatCheck("job-42-1")
	if check == nil {
		t.Fatal("expected a non-nil callback once a JobProgressHeartbeat is wired")
	}
	if got := check(); got != true {
		t.Fatalf("expected the callback to return the wired heartbeat's result (true), got %v", got)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("expected exactly 1 call into the wired heartbeat, got %d", len(fake.calls))
	}
	if fake.calls[0].id != "job-42-1" {
		t.Fatalf("expected job id %q, got %q", "job-42-1", fake.calls[0].id)
	}
	if fake.calls[0].after != 5*time.Second {
		t.Fatalf("expected the current SubagentBackgroundAfterSec (5s), got %s", fake.calls[0].after)
	}

	fake.result = false
	if got := check(); got != false {
		t.Fatalf("expected the callback to re-read the wired heartbeat's latest result (false), got %v", got)
	}
}
