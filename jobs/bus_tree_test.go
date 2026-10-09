package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
)

func TestBusTree_ParentOfAndIsLive(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	parent := r.Start(context.Background(), "parent", KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "", false, nil
	})
	child := r.Start(context.Background(), "child", KindSubagent, parent.ID, func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "", false, nil
	})
	tree := r.BusTree()

	if parentID, ok := tree.ParentOf(child.ID); !ok || parentID != parent.ID {
		t.Fatalf("ParentOf(child) = %q, %v; want %q, true", parentID, ok, parent.ID)
	}
	if _, ok := tree.ParentOf("job-unknown"); ok {
		t.Fatal("ParentOf(unknown) reported a known agent")
	}
	if !tree.IsLive(child.ID) {
		t.Fatal("running child is not live")
	}
	close(release)
	r.Wait(context.Background(), child.ID, time.Second)
	if tree.IsLive(child.ID) {
		t.Fatal("finished child is still live")
	}
}

// TestBusTree_PublishFromHookDoesNotDeadlock publishes a job.status message
// from the registry event hook. The bus takes its own lock and then calls
// IsLive, which takes r.mu, so the hook must run without r.mu held.
func TestBusTree_PublishFromHookDoesNotDeadlock(t *testing.T) {
	r := NewRegistry()
	b := bus.New(bus.WithTree(r.BusTree()))
	defer b.Close()
	sub := b.Subscribe("orch", bus.Filter{To: bus.Addr{Type: bus.AddrOrchestrator}, Kinds: []bus.Kind{bus.KindJobStatus}})

	r.SetOnEvent(func(j Job) {
		_, _ = bus.Publish(b, bus.KindJobStatus, bus.Addr{Type: bus.AddrOrchestrator},
			bus.Addr{Type: bus.AddrOrchestrator}, bus.OriginSystem,
			bus.JobStatus{ID: j.ID, Status: string(j.Status)})
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		job := r.Start(context.Background(), "demo", KindSubagent, "", func(ctx context.Context, _ string) (string, bool, error) {
			_, _ = bus.Publish(b, bus.KindAgentMessage, bus.Addr{Type: bus.AddrOrchestrator},
				bus.Addr{Type: bus.AddrAgent, ID: "job-x"}, bus.OriginSystem, bus.AgentMessage{Text: "hi"})
			return "ok", false, nil
		})
		r.Wait(context.Background(), job.ID, time.Second)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing from the event hook deadlocked")
	}
	// job.status is Latest per job, so the subscriber sees the newest status.
	// The completion event fires after Wait returns, so wait for the done status.
	deadline := time.After(5 * time.Second)
	last := ""
	for last != string(StatusDone) {
		for _, m := range sub.Drain() {
			st, err := bus.Decode[bus.JobStatus](m)
			if err != nil {
				t.Fatalf("decode job.status: %v", err)
			}
			last = st.Status
		}
		if last == string(StatusDone) {
			break
		}
		select {
		case <-sub.Ready():
		case <-deadline:
			t.Fatalf("last job.status = %q, want done", last)
		}
	}
}
