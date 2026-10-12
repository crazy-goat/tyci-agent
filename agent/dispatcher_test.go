package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
)

// isolateHome points HOME at an empty directory, so that a test never reads
// or writes the real home of the machine.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// waitToolResult waits until sub has a KindToolResult message for callID.
func waitToolResult(t *testing.T, sub *bus.Sub, callID string) bus.ToolResult {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-sub.Ready():
			for _, m := range sub.Drain() {
				got, err := bus.Decode[bus.ToolResult](m)
				if err != nil {
					t.Fatal(err)
				}
				if got.CallID == callID {
					return got
				}
			}
		case <-deadline:
			t.Fatalf("no tool.result for %s on the bus", callID)
		}
	}
}

func TestAwaitInlineFastResult(t *testing.T) {
	isolateHome(t)
	d := newDispatcher(nil, "a1", nil)
	id := d.Fire(context.Background(), "read", func(context.Context) (string, error) {
		return "ok", nil
	})
	if id != "c1" {
		t.Fatalf("call id = %q, want c1", id)
	}

	out := d.Await(context.Background(), id, time.Second)
	if out.Background || out.Failed || out.Text != "ok" {
		t.Errorf("outcome = %+v, want inline ok", out)
	}
}

func TestAwaitTimeoutBackground(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	defer b.Close()
	self := bus.Addr{Type: bus.AddrAgent, ID: "a1"}
	sub := b.Subscribe("test", bus.Filter{To: self, Kinds: []bus.Kind{bus.KindToolResult}})

	d := newDispatcher(b, "a1", nil)
	release := make(chan struct{})
	id := d.Fire(context.Background(), "bash", func(context.Context) (string, error) {
		<-release
		return "late", nil
	})

	out := d.Await(context.Background(), id, 20*time.Millisecond)
	if !out.Background || out.Text != "started, id="+id {
		t.Fatalf("outcome = %+v, want placeholder", out)
	}

	close(release)
	got := waitToolResult(t, sub, id)
	if got.Content != "late" || got.Failed {
		t.Errorf("tool.result = %+v, want content late", got)
	}
}

func TestAwaitBusMessageBackground(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	defer b.Close()
	self := bus.Addr{Type: bus.AddrAgent, ID: "a1"}
	sub := b.Subscribe("test", bus.Filter{To: self, Kinds: []bus.Kind{bus.KindToolResult}})

	inbox := make(chan bus.Message, 1)
	d := newDispatcher(b, "a1", inbox)
	release := make(chan struct{})
	id := d.Fire(context.Background(), "bash", func(context.Context) (string, error) {
		<-release
		return "late", nil
	})

	inbox <- bus.Message{Kind: bus.KindAgentMessage}
	start := time.Now()
	out := d.Await(context.Background(), id, 5*time.Second)
	if !out.Background {
		t.Fatalf("outcome = %+v, want placeholder", out)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("Await waited %v; the message should have woken it at once", time.Since(start))
	}

	close(release)
	if got := waitToolResult(t, sub, id); got.Content != "late" {
		t.Errorf("tool.result content = %q, want late", got.Content)
	}
}

func TestAwaitIgnoresJobStatus(t *testing.T) {
	isolateHome(t)
	inbox := make(chan bus.Message, 1)
	d := newDispatcher(nil, "a1", inbox)
	id := d.Fire(context.Background(), "read", func(context.Context) (string, error) {
		time.Sleep(30 * time.Millisecond)
		return "done", nil
	})

	inbox <- bus.Message{Kind: bus.KindJobStatus}
	out := d.Await(context.Background(), id, 5*time.Second)
	if out.Background || out.Text != "done" {
		t.Errorf("outcome = %+v, want inline done (job.status must not wake)", out)
	}
}

func TestWakesInlineTable(t *testing.T) {
	cases := []struct {
		kind bus.Kind
		want bool
	}{
		{bus.KindToolResult, true},
		{bus.KindAgentMessage, true},
		{bus.KindAskRequest, true},
		{bus.KindPingMissed, true},
		{bus.KindJobStatus, false},
		{bus.KindNoticeCompletion, false},
		{bus.KindBtwAnswer, false},
		{"user.message", false},
	}
	for _, tc := range cases {
		if got := wakesInline(bus.Message{Kind: tc.kind}); got != tc.want {
			t.Errorf("wakesInline(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// TestAwaitRaceResultAndWake fires a call that ends at once and sends a
// waking message at the same time. Either the result or the placeholder is
// returned, and a placeholder always gets its tool.result on the bus.
func TestAwaitRaceResultAndWake(t *testing.T) {
	isolateHome(t)
	b := bus.New()
	defer b.Close()
	self := bus.Addr{Type: bus.AddrAgent, ID: "a1"}
	sub := b.Subscribe("test", bus.Filter{To: self, Kinds: []bus.Kind{bus.KindToolResult}})

	inbox := make(chan bus.Message, 1)
	d := newDispatcher(b, "a1", inbox)
	for range 50 {
		id := d.Fire(context.Background(), "read", func(context.Context) (string, error) {
			return "fast", nil
		})
		sent := make(chan struct{})
		go func() {
			inbox <- bus.Message{Kind: bus.KindAgentMessage}
			close(sent)
		}()

		out := d.Await(context.Background(), id, time.Second)
		if out.Background {
			if out.Text != "started, id="+id {
				t.Fatalf("placeholder = %q", out.Text)
			}
			if got := waitToolResult(t, sub, id); got.Content != "fast" {
				t.Fatalf("tool.result content = %q, want fast", got.Content)
			}
		} else if out.Text != "fast" {
			t.Fatalf("inline outcome = %+v, want fast", out)
		}
		// Wait for the send, then drop a wake message that Await did not read,
		// so that it cannot wake the next round.
		<-sent
		select {
		case <-inbox:
		default:
		}
	}
}

func TestPanicBecomesFailedResult(t *testing.T) {
	isolateHome(t)
	d := newDispatcher(nil, "a1", nil)
	id := d.Fire(context.Background(), "bash", func(context.Context) (string, error) {
		panic("boom")
	})

	out := d.Await(context.Background(), id, time.Second)
	if out.Background || !out.Failed || !strings.HasPrefix(out.Text, "Error: tool panicked: boom") {
		t.Errorf("outcome = %+v, want failed panic result", out)
	}
}

func TestTimeoutFor(t *testing.T) {
	cases := map[string]time.Duration{
		"read":       2500 * time.Millisecond,
		"bash":       60 * time.Second,
		"lua":        60 * time.Second,
		"subagent":   0,
		"ask_parent": 0,
	}
	for name, want := range cases {
		if got := timeoutFor(name); got != want {
			t.Errorf("timeoutFor(%q) = %v, want %v", name, got, want)
		}
	}
}
