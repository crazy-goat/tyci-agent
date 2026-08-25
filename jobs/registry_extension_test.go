package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func startDeadlineJob(t *testing.T, r *Registry, duration time.Duration, release <-chan struct{}) (*Job, <-chan context.Context) {
	t.Helper()
	ctxs := make(chan context.Context, 1)
	job := r.Start(deadlineContext(t, duration), "extension", KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		ctxs <- ctx
		<-release
		return "", false, ctx.Err()
	})
	return job, ctxs
}

// deadlineContext returns a context carrying a deadline, with its cancel
// registered on t rather than discarded — discarding it leaks the timer
// until the deadline fires, which is what `go vet` flagged here. The cancel
// cannot be called before returning (that would cancel the context this
// test hands to a job) so it has to outlive the call, and t.Cleanup is the
// only place that can hold it.
func deadlineContext(t *testing.T, duration time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	t.Cleanup(cancel)
	return ctx
}

func TestExtensionApprovalMovesDeadlineAndContext(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job, ctxs := startDeadlineJob(t, r, 100*time.Millisecond, release)
	ctx := <-ctxs
	before, ok := ctx.Deadline()
	if !ok {
		t.Fatal("job context has no deadline")
	}
	requestID, ok := r.RequestExtension(job.ID, 200*time.Millisecond, "finish current work")
	if !ok {
		t.Fatal("request was refused")
	}
	wait := make(chan bool, 1)
	go func() {
		approved, answered := r.WaitExtension(context.Background(), job.ID, requestID)
		wait <- approved && answered
	}()
	if !r.ResolveExtension(job.ID, requestID, true) {
		t.Fatal("approval was refused")
	}
	if !<-wait {
		t.Fatal("wait did not observe approval")
	}
	after, ok := ctx.Deadline()
	if !ok || after.Before(before.Add(190*time.Millisecond)) {
		t.Fatalf("deadline did not extend from old deadline: before=%v after=%v", before, after)
	}
	select {
	case <-ctx.Done():
		t.Fatal("extended context ended too early")
	case <-time.After(130 * time.Millisecond):
	}
	close(release)
	r.Wait(context.Background(), job.ID, time.Second)
}

func TestExtensionRejectsAndNoAnswerExpires(t *testing.T) {
	for _, test := range []struct {
		name    string
		resolve bool
	}{
		{name: "reject", resolve: true},
		{name: "no answer", resolve: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := NewRegistry()
			release := make(chan struct{})
			job, _ := startDeadlineJob(t, r, 60*time.Millisecond, release)
			requestID, ok := r.RequestExtension(job.ID, 200*time.Millisecond, "reason")
			if !ok {
				t.Fatal("request was refused")
			}
			if test.resolve && !r.ResolveExtension(job.ID, requestID, false) {
				t.Fatal("rejection was refused")
			}
			approved, answered := r.WaitExtension(context.Background(), job.ID, requestID)
			if test.resolve && (!answered || approved) {
				t.Fatalf("rejection result = approved:%v answered:%v", approved, answered)
			}
			if !test.resolve && (answered || approved) {
				t.Fatalf("late timeout result = approved:%v answered:%v", approved, answered)
			}
			close(release)
			r.Wait(context.Background(), job.ID, time.Second)
		})
	}
}

func TestExtensionValidationAndDuplicateResolution(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job, _ := startDeadlineJob(t, r, time.Second, release)
	for _, seconds := range []time.Duration{0, -time.Second, 11 * time.Minute} {
		if id, ok := r.RequestExtension(job.ID, seconds, "reason"); ok || id != "" {
			t.Fatalf("invalid duration accepted: %v %v", id, ok)
		}
	}
	if _, ok := r.RequestExtension(job.ID, time.Second, string(make([]byte, maxExtensionReason+1))); ok {
		t.Fatal("overlong reason accepted")
	}
	requestID, ok := r.RequestExtension(job.ID, time.Second, "reason")
	if !ok {
		t.Fatal("valid request refused")
	}
	if _, ok := r.RequestExtension(job.ID, time.Second, "second"); ok {
		t.Fatal("second pending request accepted")
	}
	if !r.ResolveExtension(job.ID, requestID, false) {
		t.Fatal("rejection refused")
	}
	if r.ResolveExtension(job.ID, requestID, true) {
		t.Fatal("duplicate resolution accepted")
	}
	close(release)
}

func TestExtensionCancelUnblocksWait(t *testing.T) {
	r := NewRegistry()
	job := r.Start(context.Background(), "cancel", KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-ctx.Done()
		return "", false, errors.New("cancelled")
	})
	if _, ok := r.RequestExtension(job.ID, time.Second, "no deadline"); ok {
		t.Fatal("request without deadline accepted")
	}
	if !r.Cancel(job.ID) {
		t.Fatal("cancel refused")
	}
	r.Wait(context.Background(), job.ID, time.Second)
}

func TestExtensionRejectThenAllowsNewRequest(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job, _ := startDeadlineJob(t, r, time.Second, release)
	first, ok := r.RequestExtension(job.ID, time.Second, "first")
	if !ok || !r.ResolveExtension(job.ID, first, false) {
		t.Fatal("initial request was not rejected")
	}
	second, ok := r.RequestExtension(job.ID, time.Second, "second")
	if !ok || second == first {
		t.Fatalf("request after rejection was not accepted: id=%q ok=%v", second, ok)
	}
	if !r.ResolveExtension(job.ID, second, true) {
		t.Fatal("second request approval was refused")
	}
	close(release)
	r.Wait(context.Background(), job.ID, time.Second)
}

func TestExtensionWaitCancellationClearsRequest(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job, _ := startDeadlineJob(t, r, time.Second, release)
	requestID, ok := r.RequestExtension(job.ID, time.Second, "cancel")
	if !ok {
		t.Fatal("request was refused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	approved, answered := r.WaitExtension(ctx, job.ID, requestID)
	if approved || answered {
		t.Fatalf("cancelled wait returned approved=%v answered=%v", approved, answered)
	}
	if r.ResolveExtension(job.ID, requestID, true) {
		t.Fatal("late resolution succeeded after wait cancellation")
	}
	if _, ok := r.RequestExtension(job.ID, time.Second, "replacement"); !ok {
		t.Fatal("replacement request was refused after cancellation")
	}
	close(release)
	r.Wait(context.Background(), job.ID, time.Second)
}

func TestExtensionRejectAfterDeadlineDoesNotResolve(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job, _ := startDeadlineJob(t, r, 20*time.Millisecond, release)
	requestID, ok := r.RequestExtension(job.ID, time.Second, "late")
	if !ok {
		t.Fatal("request was refused")
	}
	time.Sleep(40 * time.Millisecond)
	if r.ResolveExtension(job.ID, requestID, false) {
		t.Fatal("rejection succeeded after deadline")
	}
	close(release)
	r.Wait(context.Background(), job.ID, time.Second)
}
