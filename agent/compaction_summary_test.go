package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crazy-goat/tyci-agent/session"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/internal/ledger"
	"github.com/crazy-goat/tyci-agent/stream"
)

// summaryStub is a model client that answers the summary call with stub and
// sends every other call to the wrapped Fake. The summary call is the one
// whose last message holds compactSummaryInstruction.
type summaryStub struct {
	*connectortest.Fake
	stub func(ctx context.Context) (<-chan stream.Event, error)
}

func (s *summaryStub) Stream(ctx context.Context, req connector.Request) (<-chan stream.Event, error) {
	if isSummaryRequest(req) {
		return s.stub(ctx)
	}
	return s.Fake.Stream(ctx, req)
}

func isSummaryRequest(req connector.Request) bool {
	if len(req.Messages) == 0 {
		return false
	}
	last := req.Messages[len(req.Messages)-1]
	return len(last.Content) > 0 && strings.Contains(last.Content[0].Text, compactSummaryInstruction)
}

// summaryReply answers the summary call with text and a fixed usage.
func summaryReply(text string) func(context.Context) (<-chan stream.Event, error) {
	return func(context.Context) (<-chan stream.Event, error) {
		ch := make(chan stream.Event, 2)
		ch <- stream.TextDelta{Text: text}
		ch <- stream.Finish{Usage: stream.Usage{Input: 1000, Output: 50}}
		close(ch)
		return ch, nil
	}
}

// hardLimitTurn is a model whose first call ends past the hard limit (170000
// of a 200000 window) and whose later calls answer "done".
func hardLimitTurn(stub func(context.Context) (<-chan stream.Event, error)) connector.ModelClient {
	fake := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "working"},
			stream.Finish{Usage: stream.Usage{Input: 180000, Output: 1000}},
		}},
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
	if stub == nil {
		return fake
	}
	return &summaryStub{Fake: fake, stub: stub}
}

// historyOf returns n messages that alternate user and assistant, starting
// with a user message. The first text is "the first question". The turn of a
// test adds one assistant message, so historyOf(9) gives 10 messages, more
// than compactKeepMessages.
func historyOf(n int) []connector.Message {
	msgs := make([]connector.Message, 0, n)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		text := fmt.Sprintf("message %d", i)
		if i == 0 {
			text = "the first question"
		}
		msgs = append(msgs, connector.Message{Role: role, Content: []connector.ContentBlock{{Type: "text", Text: text}}})
	}
	return msgs
}

// runHardLimit runs one turn that triggers the automatic compaction and
// returns the compacted history and the usage of the run.
func runHardLimit(t *testing.T, mc connector.ModelClient, d Sink, cfg Config) ([]connector.Message, stream.Usage) {
	t.Helper()
	sess := newAutoCompactSession(t)
	msgs := historyOf(9)
	cfg.MaxRetries = 1
	cfg.ContextLimit = 200000
	cfg.HardLimit = 170000
	cfg.Session = sess
	cfg.Compactor = func(summary, focus string, meta session.CompactMeta) (string, error) {
		return CompactSession(sess, &msgs, summary, focus, meta)
	}
	usage, err := Run(context.Background(), mc, d, &msgs, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return msgs, usage
}

const markerText = "Earlier turns are not repeated here"

func TestCompactSummary_SummaryLeadsCompactedHistory(t *testing.T) {
	msgs, _ := runHardLimit(t, hardLimitTurn(summaryReply("SUMMARY TEXT")), &silentDisplay{}, Config{})
	lead := msgs[0].Content[0].Text
	if !strings.Contains(lead, "Automatic compaction triggered") || !strings.Contains(lead, "SUMMARY TEXT") {
		t.Fatalf("lead = %q, want the trigger line and the model summary", lead)
	}
	if !strings.Contains(lead, "raw session file and its markdown dump at") {
		t.Fatalf("lead = %q, want the pointer to the raw session file and dump after the summary", lead)
	}
	if strings.Contains(lead, markerText) {
		t.Fatalf("lead = %q, must not use the fixed marker when a summary exists", lead)
	}
}

// With 8 messages in total, CompactSession keeps all of them. The summary
// call would add cost and no benefit, so it is not made.
func TestCompactSummary_EightMessagesSkipCall(t *testing.T) {
	sess := newAutoCompactSession(t)
	p := hardLimitTurn(nil).(*connectortest.Fake)
	msgs := historyOf(7)
	if _, err := Run(context.Background(), p, &silentDisplay{}, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
		HardLimit:    170000,
		Session:      sess,
		Compactor: func(summary, focus string, meta session.CompactMeta) (string, error) {
			return CompactSession(sess, &msgs, summary, focus, meta)
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := p.Calls(); got != 1 {
		t.Fatalf("provider Calls() = %d, want 1 (the turn only, no summary call)", got)
	}
	if lead := msgs[0].Content[0].Text; !strings.Contains(lead, markerText) {
		t.Fatalf("lead = %q, want the fixed marker", lead)
	}
}

// A window too small for the summary call (9500 tokens of conversation, the
// overhead and a 1000-token reply do not fit in 10000) uses the fixed marker
// and makes no call.
func TestCompactSummary_SmallWindowSkipsCallAndUsesMarker(t *testing.T) {
	sess := newAutoCompactSession(t)
	p := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "working"},
			stream.Finish{Usage: stream.Usage{Input: 9500, Output: 100}},
		}},
	}
	msgs := historyOf(9)
	if _, err := Run(context.Background(), p, &silentDisplay{}, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 10000,
		Session:      sess,
		Compactor: func(summary, focus string, meta session.CompactMeta) (string, error) {
			return CompactSession(sess, &msgs, summary, focus, meta)
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := p.Calls(); got != 1 {
		t.Fatalf("provider Calls() = %d, want 1 (no summary call in a small window)", got)
	}
	if lead := msgs[0].Content[0].Text; !strings.Contains(lead, markerText) {
		t.Fatalf("lead = %q, want the fixed marker", lead)
	}
}

func TestCompactSummary_ErrorFallsBackToMarker(t *testing.T) {
	stub := func(context.Context) (<-chan stream.Event, error) {
		return nil, errors.New("summary boom")
	}
	msgs, _ := runHardLimit(t, hardLimitTurn(stub), &silentDisplay{}, Config{})
	if lead := msgs[0].Content[0].Text; !strings.Contains(lead, markerText) {
		t.Fatalf("lead = %q, want the fixed marker after a failed summary call", lead)
	}
}

func TestCompactSummary_EmptySummaryFallsBackToMarker(t *testing.T) {
	stub := func(context.Context) (<-chan stream.Event, error) {
		ch := make(chan stream.Event, 1)
		ch <- stream.Finish{Usage: stream.Usage{Input: 1000, Output: 5}}
		close(ch)
		return ch, nil
	}
	msgs, _ := runHardLimit(t, hardLimitTurn(stub), &silentDisplay{}, Config{})
	if lead := msgs[0].Content[0].Text; !strings.Contains(lead, markerText) {
		t.Fatalf("lead = %q, want the fixed marker after an empty summary", lead)
	}
}

// blockingClient ignores its context and never answers until release is
// closed. It shows that the summary call cannot hold the turn past its
// timeout.
type blockingClient struct {
	release chan struct{}
}

func (b *blockingClient) Provider() string { return "block" }
func (b *blockingClient) Model() string    { return "block-1" }
func (b *blockingClient) Stream(context.Context, connector.Request) (<-chan stream.Event, error) {
	ch := make(chan stream.Event)
	go func() {
		<-b.release
		close(ch)
	}()
	return ch, nil
}

func TestSummarizeForCompaction_TimeoutDoesNotBlock(t *testing.T) {
	b := &blockingClient{release: make(chan struct{})}
	t.Cleanup(func() { close(b.release) })
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}

	start := time.Now()
	_, err := summarizeForCompaction(context.Background(), b, msgs, 50*time.Millisecond, compactSummaryMaxTokens)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("summary call took %v after a 50ms timeout", elapsed)
	}
}

func TestCompactSummary_RequestHasNoTools(t *testing.T) {
	sess := newAutoCompactSession(t)
	p := hardLimitTurn(nil).(*connectortest.Fake)
	msgs := historyOf(9)
	schema := json.RawMessage(`[{"type":"function","function":{"name":"read"}}]`)
	if _, err := Run(context.Background(), p, &silentDisplay{}, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
		HardLimit:    170000,
		Schema:       schema,
		Session:      sess,
		Compactor: func(summary, focus string, meta session.CompactMeta) (string, error) {
			return CompactSession(sess, &msgs, summary, focus, meta)
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := p.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want the turn and the summary call", len(reqs))
	}
	if len(reqs[0].Tools) == 0 {
		t.Fatalf("turn request has no tools, want the schema so the test proves the summary call differs")
	}
	if len(reqs[1].Tools) != 0 {
		t.Fatalf("summary call Tools = %s, want none", reqs[1].Tools)
	}
}

func TestCompactSummary_InputIsCurrentConversation(t *testing.T) {
	sess := newAutoCompactSession(t)
	p := hardLimitTurn(nil).(*connectortest.Fake)
	msgs := historyOf(9)
	if _, err := Run(context.Background(), p, &silentDisplay{}, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: 200000,
		HardLimit:    170000,
		Session:      sess,
		Compactor: func(summary, focus string, meta session.CompactMeta) (string, error) {
			return CompactSession(sess, &msgs, summary, focus, meta)
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := p.Requests()
	if len(reqs) < 2 {
		t.Fatalf("requests = %d, want the summary call", len(reqs))
	}
	summaryReq := reqs[1]
	if len(summaryReq.Messages) != 1 || summaryReq.Messages[0].Role != "user" {
		t.Fatalf("summary call has %d messages, want one user transcript", len(summaryReq.Messages))
	}
	text := summaryReq.Messages[0].Content[0].Text
	for _, want := range []string{"the first question", "working", compactSummaryInstruction} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary input misses %q:\n%s", want, text)
		}
	}
}

// summaryRecorder counts the Summary events, which is where the ledger
// records a call.
type summaryRecorder struct {
	silentDisplay
	summaries int
}

func (s *summaryRecorder) Summary(stream.Usage, stream.Stats) { s.summaries++ }

func TestCompactSummary_UsageAndCostIncludeSummary(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)
	rec := &summaryRecorder{}
	sink := ledger.Watch(rec, ledger.Main, "count", "count-1", "")

	_, usage := runHardLimit(t, hardLimitTurn(summaryReply("SUMMARY TEXT")), sink, Config{})

	if usage.Input != 181000 || usage.Output != 1050 {
		t.Fatalf("run usage = %+v, want Input 181000 and Output 1050 (turn plus summary call)", usage)
	}
	if rec.summaries != 2 {
		t.Fatalf("Summary events = %d, want 2 (turn and summary call)", rec.summaries)
	}
	var found bool
	for _, r := range ledger.Get().Rows {
		if r.Kind == ledger.Main && r.Model == "count-1" {
			found = true
			if r.Usage.Input != 181000 || r.Usage.Output != 1050 || r.Runs != 2 {
				t.Fatalf("ledger row = %+v, want the turn plus the summary call", r)
			}
		}
	}
	if !found {
		t.Fatal("no main ledger row for the run")
	}
}

// #304 follow-up: a subagent without a session keeps the summary of the
// removed messages in the note, after its task.
func TestCompactSummary_InLoopNoteContainsSummary(t *testing.T) {
	runner := newMockToolRunner()
	runner.SetResult("read", "file content")
	fake := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{
			toolRound("t1", 1000), toolRound("t2", 1000), toolRound("t3", 1000),
			toolRound("t4", 1000), toolRound("t5", 180000),
		},
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
	mc := &summaryStub{Fake: fake, stub: summaryReply("SUMMARY TEXT")}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "the task"}}},
	}
	if _, err := Run(context.Background(), mc, &silentDisplay{}, &msgs, Config{
		MaxRetries:       1,
		ContextLimit:     1000000,
		SoftLimit:        150000,
		HardLimit:        170000,
		Tools:            runner,
		InLoopCompaction: true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if msgs[0].Content[0].Text != "the task" {
		t.Fatalf("msgs[0] = %q, want the task", msgs[0].Content[0].Text)
	}
	note := msgs[1].Content[0].Text
	if !strings.Contains(note, "automated context compaction") || !strings.Contains(note, "SUMMARY TEXT") {
		t.Fatalf("note = %q, want the compaction note with the summary", note)
	}
	if strings.Index(note, "automated context compaction") > strings.Index(note, "SUMMARY TEXT") {
		t.Fatalf("note = %q, the summary must come after the note text", note)
	}
	// 5 tool rounds and the answer after the compaction. The summary call
	// goes to the stub, not to the Fake, so it is not counted here.
	if got := fake.Calls(); got != 6 {
		t.Fatalf("provider calls = %d, want 6", got)
	}
}

func TestCompactSummary_InLoopFallsBackToNoteWithoutSummary(t *testing.T) {
	runner := newMockToolRunner()
	runner.SetResult("read", "file content")
	fake := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{
			toolRound("t1", 1000), toolRound("t2", 1000), toolRound("t3", 1000),
			toolRound("t4", 1000), toolRound("t5", 180000),
		},
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
	mc := &summaryStub{Fake: fake, stub: func(context.Context) (<-chan stream.Event, error) {
		return nil, errors.New("summary boom")
	}}
	msgs := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "the task"}}},
	}
	if _, err := Run(context.Background(), mc, &silentDisplay{}, &msgs, Config{
		MaxRetries:       1,
		ContextLimit:     1000000,
		HardLimit:        170000,
		Tools:            runner,
		InLoopCompaction: true,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	note := msgs[1].Content[0].Text
	if !strings.Contains(note, "automated context compaction") || strings.Contains(note, "Summary of the removed") {
		t.Fatalf("note = %q, want the plain note without a summary", note)
	}
}

// runAtDefaultLimit runs one turn that ends with used tokens in a window of
// window tokens. It sets no hard limit, so the default limit of the window
// applies. It returns the history after the run and the provider.
func runAtDefaultLimit(t *testing.T, window, used int, stub func(context.Context) (<-chan stream.Event, error)) ([]connector.Message, *connectortest.Fake) {
	t.Helper()
	sess := newAutoCompactSession(t)
	fake := &connectortest.Fake{
		ProviderName: "count",
		ModelName:    "count-1",
		Turns: [][]stream.Event{{
			stream.TextDelta{Text: "working"},
			stream.Finish{Usage: stream.Usage{Input: used - 1000, Output: 1000}},
		}},
		OnExhausted: []stream.Event{
			stream.TextDelta{Text: "done"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}
	var mc connector.ModelClient = fake
	if stub != nil {
		mc = &summaryStub{Fake: fake, stub: stub}
	}
	msgs := historyOf(9)
	if _, err := Run(context.Background(), mc, &silentDisplay{}, &msgs, Config{
		MaxRetries:   1,
		ContextLimit: window,
		Session:      sess,
		Compactor: func(summary, focus string, meta session.CompactMeta) (string, error) {
			return CompactSession(sess, &msgs, summary, focus, meta)
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return msgs, fake
}

// TestCompactSummary_DefaultLimitsCallWithRoom checks that the summary call is
// made when the conversation is a few thousand tokens past the default hard
// limit and the window still has room for the reply.
func TestCompactSummary_DefaultLimitsCallWithRoom(t *testing.T) {
	for _, window := range []int{200000, 128000} {
		_, hardAt := compactThresholds(window, 0, 0, 0)
		msgs, _ := runAtDefaultLimit(t, window, hardAt+3000, summaryReply("SUMMARY TEXT"))
		if lead := msgs[0].Content[0].Text; !strings.Contains(lead, "SUMMARY TEXT") {
			t.Fatalf("window %d: lead = %q, want the model summary", window, lead)
		}
	}
}

// TestCompactSummary_DefaultLimitsSkipWithoutRoom checks that the call is not
// made when the conversation leaves less than compactSummaryMinTokens of room,
// and that the fixed marker is used.
func TestCompactSummary_DefaultLimitsSkipWithoutRoom(t *testing.T) {
	for _, window := range []int{200000, 128000} {
		_, hardAt := compactThresholds(window, 0, 0, 0)
		used := hardAt + 10000
		if summaryBudget(used, window) >= compactSummaryMinTokens {
			t.Fatalf("window %d: test setup has room for the summary", window)
		}
		msgs, fake := runAtDefaultLimit(t, window, used, nil)
		if got := fake.Calls(); got != 1 {
			t.Fatalf("window %d: provider Calls() = %d, want 1 (no summary call)", window, got)
		}
		if lead := msgs[0].Content[0].Text; !strings.Contains(lead, markerText) {
			t.Fatalf("window %d: lead = %q, want the fixed marker", window, lead)
		}
	}
}

func TestSummaryBudget(t *testing.T) {
	tests := []struct {
		name         string
		used, window int
		want         int
	}{
		{"unknown window", 50000, 0, compactSummaryMaxTokens},
		{"plenty of room", 100000, 200000, compactSummaryMaxTokens},
		{"room below the cap", 193000, 200000, 200000 - 193000 - compactSummaryOverhead},
		{"no room", 200000, 200000, -compactSummaryOverhead},
	}
	for _, tt := range tests {
		if got := summaryBudget(tt.used, tt.window); got != tt.want {
			t.Errorf("%s: summaryBudget(%d, %d) = %d, want %d", tt.name, tt.used, tt.window, got, tt.want)
		}
	}
}
