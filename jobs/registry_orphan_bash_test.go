package jobs

// Tests for #435: when a job ends, the background commands it started
// (KindBash children) are stopped too. Other children, such as nested
// subagents, keep running. To check the fix, remove the bashChildCancelsLocked
// call in Registry.Start: the bash child then keeps running and the first test
// fails at its wait timeout.

import (
	"context"
	"testing"
	"time"
)

func TestEndingJobStopsItsBashChildren(t *testing.T) {
	reg := NewRegistry()
	release := make(chan struct{})
	parent := reg.Start(context.Background(), "subagent", KindSubagent, "", func(context.Context, string) (string, bool, error) {
		<-release
		return "", false, nil
	})
	bash := reg.Start(context.Background(), "bash", KindBash, parent.ID, func(ctx context.Context, _ string) (string, bool, error) {
		<-ctx.Done()
		return "", false, ctx.Err()
	})

	close(release)
	snap, ok := reg.Wait(context.Background(), bash.ID, 10*time.Second)
	if !ok {
		t.Fatalf("bash job %q not found", bash.ID)
	}
	if snap.Status == StatusRunning {
		t.Fatalf("bash child kept running after its parent ended (status %s)", snap.Status)
	}
	if snap.Status != StatusFailed {
		t.Fatalf("expected the stopped bash child to be failed, got %s", snap.Status)
	}
}

func TestEndingJobKeepsNestedSubagentRunning(t *testing.T) {
	reg := NewRegistry()
	parentRelease := make(chan struct{})
	nestedRelease := make(chan struct{})
	defer close(nestedRelease)
	parent := reg.Start(context.Background(), "subagent", KindSubagent, "", func(context.Context, string) (string, bool, error) {
		<-parentRelease
		return "", false, nil
	})
	nested := reg.Start(context.Background(), "nested", KindSubagent, parent.ID, func(context.Context, string) (string, bool, error) {
		<-nestedRelease
		return "", false, nil
	})

	close(parentRelease)
	snap, ok := reg.Wait(context.Background(), nested.ID, 50*time.Millisecond)
	if !ok {
		t.Fatalf("nested job %q not found", nested.ID)
	}
	if snap.Status != StatusRunning {
		t.Fatalf("nested subagent must keep running after its parent ended, got %s", snap.Status)
	}
}
