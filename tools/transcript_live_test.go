package tools

import (
	"fmt"
	"testing"

	"github.com/crazy-goat/tyci-agent/stream"
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

func TestLiveTranscript_StartTwiceReturnsSameTranscript(t *testing.T) {
	first := startLiveTranscript("live-same")
	first.add(LiveEvent{Kind: "text", Content: "before"})
	second := startLiveTranscript("live-same")
	if first != second {
		t.Fatal("starting an existing job must return its transcript, not a new one")
	}
	events, _ := LiveTranscriptSince("live-same", 0)
	if len(events) != 1 {
		t.Fatalf("starting an existing job lost its events: %+v", events)
	}
}

func TestCopyLiveTranscript_KeepsEarlierEventsOfTheContinuedJob(t *testing.T) {
	RecordLiveEvent("copy-from", LiveEvent{Kind: "text", Content: "first turn"})
	CopyLiveTranscript("copy-from", "copy-to")
	RecordLiveEvent("copy-to", LiveEvent{Kind: "text", Content: "second turn"})

	events, ok := LiveTranscriptSince("copy-to", 0)
	if !ok || len(events) != 2 || events[0].Content != "first turn" || events[1].Content != "second turn" {
		t.Fatalf("copied transcript = %+v, ok=%v; want the earlier turn, then the new one", events, ok)
	}
	if events, _ := LiveTranscriptSince("copy-from", 0); len(events) != 1 {
		t.Fatalf("the source transcript changed: %+v", events)
	}
}

func TestCopyLiveTranscript_UnknownSourceCopiesNothing(t *testing.T) {
	CopyLiveTranscript("copy-missing", "copy-empty")
	events, ok := LiveTranscriptSince("copy-empty", 0)
	if !ok || len(events) != 0 {
		t.Fatalf("got %+v, ok=%v; want an empty transcript", events, ok)
	}
}

// countingSink counts every call it receives, so a test can check that a
// wrapper forwards all of them.
type countingSink struct{ calls int }

func (s *countingSink) Request(string)                     { s.calls++ }
func (s *countingSink) Thinking(string)                    { s.calls++ }
func (s *countingSink) Text(string)                        { s.calls++ }
func (s *countingSink) ToolCallStart(string)               { s.calls++ }
func (s *countingSink) ToolCallDelta(string)               { s.calls++ }
func (s *countingSink) ToolCallEnd(string, string)         { s.calls++ }
func (s *countingSink) ToolFinish()                        { s.calls++ }
func (s *countingSink) ToolBlock(string)                   { s.calls++ }
func (s *countingSink) Summary(stream.Usage, stream.Stats) { s.calls++ }
func (s *countingSink) Total(stream.Usage)                 { s.calls++ }
func (s *countingSink) Error(error)                        { s.calls++ }
func (s *countingSink) End()                               { s.calls++ }

func TestNewLiveSink_ForwardsEveryCallAndRecordsTheConversation(t *testing.T) {
	inner := &countingSink{}
	sink := NewLiveSink("live-sink", inner)
	if !HasLiveTranscript("live-sink") {
		t.Fatal("NewLiveSink must create the transcript before the first event")
	}
	sink.Thinking("plan")
	sink.Text("answer")
	sink.ToolCallStart("bash")
	sink.ToolCallDelta(`{"command":"ls"}`)
	sink.ToolCallEnd("bash", "file.go")
	sink.End()

	if inner.calls != 6 {
		t.Fatalf("inner sink got %d calls, want 6", inner.calls)
	}
	events, ok := LiveTranscriptSince("live-sink", 0)
	want := []LiveEvent{
		{Kind: "thinking", Content: "plan"},
		{Kind: "text", Content: "answer"},
		{Kind: "tool-start", ToolName: "bash"},
		{Kind: "tool-delta", Content: `{"command":"ls"}`},
		{Kind: "tool-end", Content: "file.go"},
	}
	if !ok || len(events) != len(want) {
		t.Fatalf("got %d events (ok=%v), want %d: %+v", len(events), ok, len(want), events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, events[i], want[i])
		}
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
