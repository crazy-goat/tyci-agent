package tools

import (
	"context"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeKindWatch records the arguments of the watch and reports arrival of a
// notice of the kinds once arrive is raised.
type fakeKindWatch struct {
	arrive  atomic.Bool
	caller  string
	kinds   []string
	stopped atomic.Bool
}

func (f *fakeKindWatch) watch(caller string, kinds []string) (func() bool, func()) {
	f.caller, f.kinds = caller, kinds
	return f.arrive.Load, func() { f.stopped.Store(true) }
}

// TestWaitKinds: wait(kinds=...) wakes only on a notice of those kinds. Other
// notices do not end it, and the watch is stopped when the call ends.
func TestWaitKinds(t *testing.T) {
	t.Run("other notice does not wake", func(t *testing.T) {
		mb := &postingMailbox{}
		SetJobMailbox(mb)
		t.Cleanup(func() { SetJobMailbox(nil) })
		fw := &fakeKindWatch{}
		SetNoticeKindWatch(fw.watch)
		t.Cleanup(func() { SetNoticeKindWatch(nil) })

		go func() {
			time.Sleep(50 * time.Millisecond)
			mb.n.Add(1)
		}()
		ctx := context.WithValue(context.Background(), JobIDCtxKey{}, "job-1-1")
		start := time.Now()
		res := (&WaitTool{}).Run(ctx, map[string]any{"seconds": 1, "kinds": []any{"ask.reply"}})
		if !res.Success {
			t.Fatalf("wait failed: %s", res.Error)
		}
		if time.Since(start) < 900*time.Millisecond {
			t.Fatalf("a notice of another kind ended the wait after %s", time.Since(start))
		}
		if strings.Contains(res.Content, "notice arrived") {
			t.Fatalf("content %q reports a notice that was not of the kind", res.Content)
		}
		if fw.caller != "job-1-1" || !reflect.DeepEqual(fw.kinds, []string{"ask.reply"}) {
			t.Fatalf("watch got caller=%q kinds=%v", fw.caller, fw.kinds)
		}
		if !fw.stopped.Load() {
			t.Fatal("the watch was not stopped when wait returned")
		}
	})

	t.Run("matching notice wakes", func(t *testing.T) {
		fw := &fakeKindWatch{}
		SetNoticeKindWatch(fw.watch)
		t.Cleanup(func() { SetNoticeKindWatch(nil) })
		go func() {
			time.Sleep(50 * time.Millisecond)
			fw.arrive.Store(true)
		}()
		start := time.Now()
		res := (&WaitTool{}).Run(context.Background(), map[string]any{"kinds": []any{"ask.reply"}})
		if !res.Success || !strings.Contains(res.Content, "notice arrived") {
			t.Fatalf("unexpected result: %+v", res)
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("wait did not stop at the matching notice (took %s)", time.Since(start))
		}
	})

	t.Run("repeated ids count once", func(t *testing.T) {
		got := uniqueIDs([]string{"job-1", "job-2", "job-1"})
		if !reflect.DeepEqual(got, []string{"job-1", "job-2"}) {
			t.Fatalf("uniqueIDs = %v", got)
		}
	})
}
