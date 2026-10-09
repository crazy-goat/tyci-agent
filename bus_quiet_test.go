package main

import "testing"

// TestQuietNotice_WaitsForNextDrain publishes a quiet notice. It is delivered
// by the next drain, and it does not wake an idle chat.
func TestQuietNotice_WaitsForNextDrain(t *testing.T) {
	withTestWiring(t)
	publishNotice(appBus, "", "[scheduled job] quiet", true)

	if got := wakeNotices(); got != nil {
		t.Fatalf("wakeNotices = %q, want nothing for a quiet notice alone", got)
	}
	if got := drainNotices(); len(got) != 1 || bodyOf(got[0]) != "[scheduled job] quiet" {
		t.Fatalf("drainNotices = %q, want the quiet notice once", got)
	}
}

// TestQuietNotice_WithLoudNoticeWakes publishes a quiet and a loud notice. The
// wake-up returns both, in order.
func TestQuietNotice_WithLoudNoticeWakes(t *testing.T) {
	withTestWiring(t)
	publishNotice(appBus, "", "[scheduled job] quiet", true)
	publishNotice(appBus, "", "[background command] loud", false)

	got := wakeNotices()
	if len(got) != 2 || bodyOf(got[0]) != "[scheduled job] quiet" || bodyOf(got[1]) != "[background command] loud" {
		t.Fatalf("wakeNotices = %q, want both notices in order", got)
	}
}

// TestQuietNotice_ClearedByNew checks that clearNotices (used by /new) also
// drops a quiet notice that waits for the next drain.
func TestQuietNotice_ClearedByNew(t *testing.T) {
	withTestWiring(t)
	publishNotice(appBus, "", "[scheduled job] quiet", true)
	if got := wakeNotices(); got != nil {
		t.Fatalf("wakeNotices = %q, want nothing", got)
	}
	clearNotices()

	if got := drainNotices(); len(got) != 0 {
		t.Fatalf("drainNotices after clearNotices = %q, want nothing", got)
	}
}
