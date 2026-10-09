package main

import (
	"context"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/jobs"
)

// A late running snapshot that reaches the forwarder after the terminal one
// must not replace it on the job.status queue.
func TestJobEventForwarder_LateSnapshotKeepsTerminalStatus(t *testing.T) {
	prev := appBus
	appBus = newAppBus("")
	t.Cleanup(func() {
		appBus.Close()
		appBus = prev
	})
	sub := appBus.Subscribe("test", bus.Filter{To: bus.Addr{Type: bus.AddrTUI}, Kinds: []bus.Kind{bus.KindJobStatus}})

	f := &jobEventForwarder{}
	f.JobEvent(jobs.Job{ID: "job-1", Status: jobs.StatusRunning, EventSeq: 1})
	f.JobEvent(jobs.Job{ID: "job-1", Status: jobs.StatusDone, EventSeq: 3})
	f.JobEvent(jobs.Job{ID: "job-1", Status: jobs.StatusRunning, Progress: "late", EventSeq: 2})

	msgs := sub.Drain()
	if len(msgs) != 1 {
		t.Fatalf("expected one Latest job.status message, got %d", len(msgs))
	}
	got, err := bus.Decode[bus.JobStatus](msgs[0])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != string(jobs.StatusDone) {
		t.Fatalf("expected the terminal status to survive, got %q", got.Status)
	}
}

// A job that runs on the package-level JobRegistry, built by production code,
// must publish its status on the bus. This fails if JobRegistry is built with
// a nil publisher.
func TestJobRegistry_ProductionRegistryPublishesJobStatus(t *testing.T) {
	prev := appBus
	appBus = newAppBus("")
	t.Cleanup(func() {
		appBus.Close()
		appBus = prev
	})
	sub := subscribeJobStatus(appBus)
	defer sub.Close()

	job := JobRegistry.Start(context.Background(), "production registry", jobs.KindOther, "", func(context.Context, string) (string, bool, error) {
		return "ok", false, nil
	})

	got := collectJobStatus(t, sub, job.ID, 2*time.Second)
	if len(got) == 0 || got[len(got)-1] != jobs.StatusDone {
		t.Fatalf("job.status messages for the production registry = %v, want to end with done", got)
	}
}
