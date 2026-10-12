package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// A child dispatcher (a subagent) never backgrounds a call. When the Await
// of a call is cancelled, Await waits until the call really ends, even when the
// call ignores its context. A replacement (#202) relies on this.
func TestChildCancelWaitsForLateCall(t *testing.T) {
	d := NewChildDispatcher()
	var finished atomic.Bool
	id := d.Fire(context.Background(), func(context.Context) (string, bool) {
		time.Sleep(200 * time.Millisecond)
		finished.Store(true)
		return "late", false
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.Await(ctx, id, noDeadline)
	if !finished.Load() {
		t.Fatal("Await returned while the cancelled call was still running")
	}
}
