package jobs

import (
	"context"
	"testing"
	"time"
)

// TestPostDrainMessages_Basic covers the mailbox's core FIFO semantics: a
// message posted before a drain is delivered, and the mailbox is empty
// afterwards.
func TestPostDrainMessages_Basic(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job := r.Start(context.Background(), "demo", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	})
	defer close(release)

	if !r.Post(job.ID, "hello") {
		t.Fatalf("Post on a running job should succeed")
	}

	got := r.DrainMessages(job.ID)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("DrainMessages = %v, want [hello]", got)
	}
}

// TestDrainMessages_Empty: draining a job that has never been posted to
// (or was already drained) returns nil, not an error.
func TestDrainMessages_Empty(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job := r.Start(context.Background(), "demo", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	})
	defer close(release)

	if got := r.DrainMessages(job.ID); got != nil {
		t.Fatalf("DrainMessages on a never-posted job = %v, want nil", got)
	}

	if !r.Post(job.ID, "one") {
		t.Fatalf("Post should succeed")
	}
	if got := r.DrainMessages(job.ID); len(got) != 1 {
		t.Fatalf("first drain = %v, want 1 message", got)
	}
	if got := r.DrainMessages(job.ID); got != nil {
		t.Fatalf("second drain (already emptied) = %v, want nil", got)
	}
}

// TestDrainMessages_UnknownID: draining (or posting to) a job id the
// registry has never seen is a no-op, not a panic or error.
func TestDrainMessages_UnknownID(t *testing.T) {
	r := NewRegistry()
	if got := r.DrainMessages("job-does-not-exist"); got != nil {
		t.Fatalf("DrainMessages on unknown id = %v, want nil", got)
	}
	if ok := r.Post("job-does-not-exist", "hi"); ok {
		t.Fatalf("Post on unknown id should return false")
	}
}

// TestPostDrainMessages_MultiplePostsBeforeDrain: several posts queue up
// FIFO and all arrive in one drain.
func TestPostDrainMessages_MultiplePostsBeforeDrain(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job := r.Start(context.Background(), "demo", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	})
	defer close(release)

	r.Post(job.ID, "first")
	r.Post(job.ID, "second")
	r.Post(job.ID, "third")

	got := r.DrainMessages(job.ID)
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("DrainMessages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DrainMessages[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRegistry_Resolve: full id, short id with "#", and short id without
// "#" must all resolve to the same job. An id matching nothing is ok=false.
func TestRegistry_Resolve(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job := r.Start(context.Background(), "demo", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	})
	defer close(release)

	short := ShortID(job.ID)

	cases := []string{job.ID, "#" + short, short}
	for _, in := range cases {
		got, ok := r.Resolve(in)
		if !ok {
			t.Fatalf("Resolve(%q) ok=false, want true", in)
		}
		if got != job.ID {
			t.Fatalf("Resolve(%q) = %q, want %q", in, got, job.ID)
		}
	}

	if _, ok := r.Resolve("#does-not-exist"); ok {
		t.Fatalf("Resolve of an unknown short id should fail")
	}
	if _, ok := r.Resolve("job-nonexistent-1"); ok {
		t.Fatalf("Resolve of an unknown full id should fail")
	}
	if _, ok := r.Resolve("#"); ok {
		t.Fatalf("Resolve(\"#\") (empty short form) should fail")
	}
	if _, ok := r.Resolve(""); ok {
		t.Fatalf("Resolve(\"\") should fail")
	}
}

// TestRegistry_PostThenDrain_ConcurrentSafe exercises Post/DrainMessages
// under -race with concurrent posts from multiple goroutines, mirroring how
// a human (/msg) and the model (the "message" tool) could both post to the
// same job around the same time.
func TestRegistry_PostThenDrain_ConcurrentSafe(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job := r.Start(context.Background(), "demo", KindOther, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	})
	defer close(release)

	const n = 20
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			r.Post(job.ID, "msg")
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}

	// Drain in a loop with a short timeout guard rather than a fixed sleep,
	// in case some posts are still landing.
	deadline := time.Now().Add(2 * time.Second)
	total := 0
	for time.Now().Before(deadline) {
		got := r.DrainMessages(job.ID)
		total += len(got)
		if total == n {
			break
		}
	}
	if total != n {
		t.Fatalf("drained %d messages total, want %d", total, n)
	}
}

func TestRegistry_ResolveByRoleAgentName(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)
	fn := func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	}
	r.Start(context.Background(), "run-1/worker", KindSubagent, "", fn)
	time.Sleep(2 * time.Millisecond)
	newer := r.Start(context.Background(), "run-1/worker", KindSubagent, "", fn)
	got, ok := r.Resolve("run-1/worker")
	if !ok || got != newer.ID {
		t.Fatalf("Resolve = %q %v, want newest %q", got, ok, newer.ID)
	}
	if _, ok := r.Resolve("run-1/review"); ok {
		t.Fatal("unknown role resolved")
	}
}

// TestPostedCountsAcceptedPostsOnly: Posted counts the messages Post accepted.
// The count survives DrainMessages, and a refused post or an unknown id does
// not change it.
func TestPostedCountsAcceptedPostsOnly(t *testing.T) {
	r := NewRegistry()
	if got := r.Posted("missing"); got != 0 {
		t.Fatalf("Posted for an unknown id = %d, want 0", got)
	}

	release := make(chan struct{})
	job := r.Start(context.Background(), "demo", KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	})
	r.Post(job.ID, "one")
	r.Post(job.ID, "two")
	r.DrainMessages(job.ID)
	if got := r.Posted(job.ID); got != 2 {
		t.Fatalf("Posted after two posts and a drain = %d, want 2", got)
	}

	close(release)
	if _, ok := r.Wait(context.Background(), job.ID, 5*time.Second); !ok {
		t.Fatalf("Wait on the finished job reported unknown")
	}
	if r.Post(job.ID, "too late") {
		t.Fatalf("Post on a finished job should be refused")
	}
	if got := r.Posted(job.ID); got != 2 {
		t.Fatalf("Posted after a refused post = %d, want 2", got)
	}
}
