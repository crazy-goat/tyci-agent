package main

import (
	"testing"

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
