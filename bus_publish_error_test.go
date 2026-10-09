package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/jobs"
)

// TestPublishErrorLoggedJobContinues runs a real job that publishes on a
// closed bus. The publish error is logged, and the job still finishes as done.
func TestPublishErrorLoggedJobContinues(t *testing.T) {
	reg, _ := withTestWiring(t)
	var log bytes.Buffer
	orig := busLog
	busLog = &log
	t.Cleanup(func() { busLog = orig })

	closed := newAppBus("")
	closed.Close()
	job := reg.Start(context.Background(), "publishes on a closed bus", jobs.KindSubagent, "",
		func(_ context.Context, id string) (string, bool, error) {
			publishAsk(closed, "", id, 1, "[background job] Q7")
			return "finished anyway", false, nil
		})

	done, ok := reg.Wait(context.Background(), job.ID, 2*time.Second)
	if !ok {
		t.Fatal("job did not finish")
	}
	if done.Status != jobs.StatusDone {
		t.Fatalf("status = %s, want %s", done.Status, jobs.StatusDone)
	}
	if !strings.Contains(log.String(), "not published") {
		t.Fatalf("log = %q, want the publish error", log.String())
	}
}
