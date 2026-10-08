package tools

import (
	"fmt"
	"testing"
)

func TestLiveTranscript_CollectorRecordsEventsInOrder(t *testing.T) {
	c := &streamingCollector{collector: newCollector(), live: startLiveTranscript("live-order")}
	c.Thinking("plan")
	c.Text("answer")
	c.ToolCallStart("bash")
	c.ToolCallDelta(`{"command":"ls"}`)
	c.ToolCallEnd("bash", "file.go")

	events, ok := LiveTranscriptSince("live-order", 0)
	if !ok {
		t.Fatal("expected a transcript for live-order")
	}
	want := []LiveEvent{
		{Kind: "thinking", Content: "plan"},
		{Kind: "text", Content: "answer"},
		{Kind: "tool-start", ToolName: "bash"},
		{Kind: "tool-delta", Content: `{"command":"ls"}`},
		{Kind: "tool-end", Content: "file.go"},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, events[i], want[i])
		}
	}
}

func TestLiveTranscript_SinceReturnsOnlyNewEvents(t *testing.T) {
	RecordLiveEvent("live-since", LiveEvent{Kind: "text", Content: "one"})
	RecordLiveEvent("live-since", LiveEvent{Kind: "text", Content: "two"})

	events, ok := LiveTranscriptSince("live-since", 1)
	if !ok || len(events) != 1 || events[0].Content != "two" {
		t.Fatalf("since 1 = %+v, ok=%v; want only \"two\"", events, ok)
	}
	events, ok = LiveTranscriptSince("live-since", 2)
	if !ok || len(events) != 0 {
		t.Fatalf("since past the end = %+v, ok=%v; want no events and ok", events, ok)
	}
	events, _ = LiveTranscriptSince("live-since", -5)
	if len(events) != 2 {
		t.Fatalf("negative index should start at 0, got %d events", len(events))
	}
}

func TestLiveTranscript_UnknownJobIsNotOK(t *testing.T) {
	if HasLiveTranscript("live-unknown") {
		t.Fatal("HasLiveTranscript reported a transcript for an unknown job")
	}
	if _, ok := LiveTranscriptSince("live-unknown", 0); ok {
		t.Fatal("LiveTranscriptSince reported ok for an unknown job")
	}
}

func TestLiveTranscript_ResumedJobKeepsEarlierEvents(t *testing.T) {
	first := startLiveTranscript("live-resume")
	first.add(LiveEvent{Kind: "text", Content: "before resume"})
	second := startLiveTranscript("live-resume")
	if first != second {
		t.Fatal("starting an existing job must return its transcript, not a new one")
	}
	events, _ := LiveTranscriptSince("live-resume", 0)
	if len(events) != 1 {
		t.Fatalf("resumed job lost its earlier events: %+v", events)
	}
}

func TestLiveTranscript_NilTranscriptRecordsNothing(t *testing.T) {
	// A child without a job id has no transcript; its collector must still work.
	c := &streamingCollector{collector: newCollector()}
	c.Text("no job")
	c.ToolCallStart("bash")
	c.ToolCallEnd("bash", "out")
	if got := c.CollectedText(); got != "no job" {
		t.Fatalf("collected text = %q, want %q", got, "no job")
	}
}

func TestLiveTranscript_OldestTranscriptIsDropped(t *testing.T) {
	for i := 0; i <= liveTranscriptCap; i++ {
		startLiveTranscript(fmt.Sprintf("live-cap-%03d", i))
	}
	if HasLiveTranscript("live-cap-000") {
		t.Fatal("the oldest transcript should be dropped once the cap is exceeded")
	}
	last := fmt.Sprintf("live-cap-%03d", liveTranscriptCap)
	if !HasLiveTranscript(last) {
		t.Fatalf("the newest transcript %s must be kept", last)
	}
}
