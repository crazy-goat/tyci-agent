package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestNonTUI_RunReadsOrchestratorNotices publishes a completion notice with no
// TUI. The NextMessages of tyci run must return it to the model.
func TestNonTUI_RunReadsOrchestratorNotices(t *testing.T) {
	withTestWiring(t)
	publishNotice(appBus, "", "[background job] done", false)

	next := withOrchestratorNotices(nil)
	got := next()
	if len(got) != 1 || bodyOf(got[0]) != "[background job] done" {
		t.Fatalf("NextMessages = %q, want the notice once", got)
	}
	if again := next(); len(again) != 0 {
		t.Fatalf("second NextMessages = %q, want nothing", again)
	}
}

// TestNonTUI_RunKeepsUserLinesFirst checks that the user's own queued lines
// come before a background notice in the same drain.
func TestNonTUI_RunKeepsUserLinesFirst(t *testing.T) {
	withTestWiring(t)
	publishNotice(appBus, "", "[background job] done", false)

	next := withOrchestratorNotices(func() []string { return []string{"user line"} })
	got := next()
	if len(got) != 2 || got[0] != "user line" {
		t.Fatalf("NextMessages = %q, want the user line first", got)
	}
}

// TestNonTUI_DropLeftoverNotices drops the notices that arrived after the last
// model turn and reports their count.
func TestNonTUI_DropLeftoverNotices(t *testing.T) {
	withTestWiring(t)
	publishNotice(appBus, "", "[background job] late", false)

	var out bytes.Buffer
	dropLeftoverNotices(&out)
	if !strings.Contains(out.String(), "1 background notice(s)") {
		t.Fatalf("output = %q, want the dropped count", out.String())
	}
	if left := drainNotices(); len(left) != 0 {
		t.Fatalf("notices left after drop = %q, want none", left)
	}
}

// TestNonTUI_DropLeftoverNotices_NoneIsSilent checks that no line is written
// when nothing is waiting.
func TestNonTUI_DropLeftoverNotices_NoneIsSilent(t *testing.T) {
	withTestWiring(t)

	var out bytes.Buffer
	dropLeftoverNotices(&out)
	if out.Len() != 0 {
		t.Fatalf("output = %q, want nothing", out.String())
	}
}
