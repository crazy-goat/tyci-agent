package jobs

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClockedJob(t *testing.T) (*Registry, *Job, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	r := NewRegistry()
	r.SetClockForTests(clock.Now)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	job := r.Start(context.Background(), "quiet", KindSubagent, "", blockingJobFn(release))
	return r, job, clock
}

func progressOf(r *Registry, id string) []string {
	for _, j := range r.List() {
		if j.ID == id {
			return j.ProgressHistory
		}
	}
	return nil
}

func TestAutoProgress_FiresAfterInterval(t *testing.T) {
	r, job, clock := newClockedJob(t)
	clock.Advance(5 * time.Minute)
	if !r.AutoProgress(job.ID, 5*time.Minute, "bash: go test ./...") {
		t.Fatal("expected an auto note after a full interval")
	}
	h := progressOf(r, job.ID)
	if len(h) != 1 || h[0] != "[auto] bash: go test ./..." {
		t.Fatalf("unexpected history %q", h)
	}
	// The auto note re-arms the clock: nothing again until another interval.
	clock.Advance(4 * time.Minute)
	if r.AutoProgress(job.ID, 5*time.Minute, "x") {
		t.Fatal("auto note fired twice within one interval")
	}
}

func TestAutoProgress_NotBeforeInterval(t *testing.T) {
	r, job, clock := newClockedJob(t)
	clock.Advance(5*time.Minute - time.Second)
	if r.AutoProgress(job.ID, 5*time.Minute, "x") {
		t.Fatal("auto note fired before the interval")
	}
	if r.AutoProgress(job.ID, 0, "x") {
		t.Fatal("auto note fired for after <= 0")
	}
}

func TestAutoProgress_ResetsOnModelNote(t *testing.T) {
	r, job, clock := newClockedJob(t)
	clock.Advance(4 * time.Minute)
	r.SetProgress(job.ID, "working")
	clock.Advance(4 * time.Minute)
	if r.AutoProgress(job.ID, 5*time.Minute, "x") {
		t.Fatal("a real note must reset the auto clock")
	}
	clock.Advance(time.Minute)
	if !r.AutoProgress(job.ID, 5*time.Minute, "x") {
		t.Fatal("expected an auto note one interval after the real note")
	}
}

func TestAutoProgress_NotForTerminalJob(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	r := NewRegistry()
	r.SetClockForTests(clock.Now)
	job := r.Start(context.Background(), "short", KindSubagent, "", func(ctx context.Context, id string) (string, bool, error) {
		return "done", false, nil
	})
	<-job.done
	clock.Advance(time.Hour)
	if r.AutoProgress(job.ID, time.Minute, "x") {
		t.Fatal("auto note written for a finished job")
	}
	if r.AutoProgress("no-such-job", time.Minute, "x") {
		t.Fatal("auto note written for an unknown job")
	}
}

func TestAutoPing_Redacted(t *testing.T) {
	r, job, clock := newClockedJob(t)
	clock.Advance(time.Hour)
	secret := "ghp_" + strings.Repeat("a", 36)
	r.AutoProgress(job.ID, time.Minute, "bash: curl -H "+secret)
	h := progressOf(r, job.ID)
	if len(h) != 1 || strings.Contains(h[0], secret) || !strings.Contains(h[0], "[REDACTED]") {
		t.Fatalf("secret not redacted: %q", h)
	}
}

func TestSnapshot_LastProgressAt(t *testing.T) {
	r, job, clock := newClockedJob(t)
	start := clock.Now()
	clock.Advance(time.Minute)
	r.SetProgress(job.ID, "hi")
	for _, j := range r.List() {
		if j.ID == job.ID {
			if want := start.Add(time.Minute); !j.LastProgressAt.Equal(want) {
				t.Fatalf("LastProgressAt = %v, want %v", j.LastProgressAt, want)
			}
			return
		}
	}
	t.Fatal("job not listed")
}

// With ping_interval 10m the nudge threshold is 5m (4m59s: no, 5m: yes).
func TestHeartbeatThreshold_IsHalfPingInterval(t *testing.T) {
	r, job, clock := newClockedJob(t)
	half := 10 * time.Minute / 2
	clock.Advance(time.Minute) // the old 60 s knob must not fire
	if r.NeedsProgressHeartbeat(job.ID, half) {
		t.Fatal("nudged at 60s")
	}
	clock.Advance(3*time.Minute + 59*time.Second)
	if r.NeedsProgressHeartbeat(job.ID, half) {
		t.Fatal("nudged at 4m59s")
	}
	clock.Advance(time.Second)
	if !r.NeedsProgressHeartbeat(job.ID, half) {
		t.Fatal("no nudge at 5m")
	}
}
