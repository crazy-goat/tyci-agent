package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func startDeadlineJob(r *Registry, duration time.Duration, release <-chan struct{}) (*Job, <-chan context.Context) {
	ctxs := make(chan context.Context, 1)
	job := r.Start(deadlineContext(duration), "extension", KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		ctxs <- ctx
		<-release
		return "", false, ctx.Err()
	})
	return job, ctxs
}

func deadlineContext(duration time.Duration) context.Context {
	ctx, _ := context.WithTimeout(context.Background(), duration)
	return ctx
}

func TestExtensionApprovalMovesDeadlineAndContext(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	job, ctxs := startDeadlineJob(r, 100*time.Millisecond, release)
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
			job, _ := startDeadlineJob(r, 60*time.Millisecond, release)
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
	job, _ := startDeadlineJob(r, time.Second, release)
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
